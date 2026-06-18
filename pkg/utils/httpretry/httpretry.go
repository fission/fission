// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package httpretry provides a small retrying http.RoundTripper that replaces
// hashicorp/go-retryablehttp. Its defaults mirror retryablehttp's so swapping
// it in is behavior-preserving: up to 4 retries with exponential backoff
// between RetryWaitMin and RetryWaitMax, retrying network errors, 429, and 5xx
// (except 501).
//
// It is the OUTERMOST transport in Fission's internal clients so that the HMAC
// signer (pkg/auth/hmac) below it re-signs each attempt with a fresh
// timestamp. Because the signer mutates the request it is handed (it reads and
// replaces r.Body and sets signature headers), every attempt is made on a
// fresh clone of the original request with the body rewound via req.GetBody —
// a request whose body cannot be rewound (GetBody == nil) is sent once, never
// retried.
package httpretry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Defaults mirror hashicorp/go-retryablehttp's DefaultClient so behavior is
// unchanged when swapping it out.
const (
	DefaultRetryMax     = 4
	DefaultRetryWaitMin = 1 * time.Second
	DefaultRetryWaitMax = 30 * time.Second

	// respReadLimit bounds how much of a to-be-retried response body is drained
	// before close, so the connection can be reused without reading an
	// arbitrarily large error body. Matches retryablehttp's default.
	respReadLimit = 4096
)

// Options configures the retrying RoundTripper.
type Options struct {
	// RetryMax is the maximum number of retries (0 disables retries, making a
	// single attempt).
	RetryMax int
	// RetryWaitMin and RetryWaitMax bound the exponential backoff.
	RetryWaitMin time.Duration
	RetryWaitMax time.Duration
}

// DefaultOptions returns the retryablehttp-equivalent defaults.
func DefaultOptions() Options {
	return Options{
		RetryMax:     DefaultRetryMax,
		RetryWaitMin: DefaultRetryWaitMin,
		RetryWaitMax: DefaultRetryWaitMax,
	}
}

type roundTripper struct {
	base http.RoundTripper
	opts Options
}

// New wraps base with retry behavior. A nil base uses http.DefaultTransport.
func New(base http.RoundTripper, opts Options) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if opts.RetryWaitMin <= 0 {
		opts.RetryWaitMin = DefaultRetryWaitMin
	}
	if opts.RetryWaitMax <= 0 {
		opts.RetryWaitMax = DefaultRetryWaitMax
	}
	return &roundTripper{base: base, opts: opts}
}

// RoundTrip implements http.RoundTripper.
func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	// A body that cannot be rewound cannot be re-sent safely: make one attempt.
	// http.NoBody (the empty body GET/DELETE requests carry) reports a nil
	// GetBody but is trivially re-sendable, so it does not count.
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return rt.base.RoundTrip(req)
	}

	var (
		resp *http.Response
		err  error
	)
	for attempt := 0; ; attempt++ {
		// Clone per attempt: the signer below mutates the request it is given,
		// so each attempt needs its own copy with a freshly-rewound body.
		attemptReq := req.Clone(ctx)
		if req.GetBody != nil {
			body, gErr := req.GetBody()
			if gErr != nil {
				return nil, gErr
			}
			attemptReq.Body = body
		}

		resp, err = rt.base.RoundTrip(attemptReq)

		if attempt >= rt.opts.RetryMax || !retryable(resp, err) {
			return resp, err
		}

		wait := backoff(rt.opts, attempt, resp)
		// Drain (bounded) and close so the connection can be reused.
		if resp != nil {
			drainAndClose(resp.Body)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// retryable reports whether the result of an attempt warrants a retry: a
// non-context transport error, HTTP 429, or 5xx other than 501 Not Implemented
// (matching retryablehttp's DefaultRetryPolicy).
func retryable(resp *http.Response, err error) bool {
	if err != nil {
		return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	if resp == nil {
		return false
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return resp.StatusCode >= 500 && resp.StatusCode != http.StatusNotImplemented
}

// backoff returns the wait before the next attempt: the server's Retry-After
// (on 429/503) when present, else exponential RetryWaitMin*2^attempt capped at
// RetryWaitMax.
func backoff(opts Options, attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if ra := retryAfter(resp); ra > 0 {
			return min(ra, opts.RetryWaitMax)
		}
	}
	wait := opts.RetryWaitMin * time.Duration(1<<uint(attempt))
	if wait <= 0 || wait > opts.RetryWaitMax {
		wait = opts.RetryWaitMax
	}
	return wait
}

// retryAfter parses a Retry-After header (delta-seconds or HTTP-date) on
// 429/503 responses. Returns 0 when absent, unparsable, or in the past.
func retryAfter(resp *http.Response) time.Duration {
	if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable {
		return 0
	}
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, respReadLimit))
	_ = body.Close()
}

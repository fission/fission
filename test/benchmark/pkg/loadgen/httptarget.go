// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package loadgen

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"time"
)

// HTTPTargetConfig describes an HTTP request to issue repeatedly under load.
type HTTPTargetConfig struct {
	URL     string
	Method  string      // default GET
	Body    []byte      // optional request body
	Headers http.Header // optional static headers

	KeepAlive   bool          // false -> a fresh connection per request
	Concurrency int           // sizes the idle-connection pool
	Timeout     time.Duration // per-request timeout; 0 -> no client timeout

	// WrapTransport, if set, wraps the tuned base transport before it is used.
	// It is the injection point for HMAC signing of the router internal
	// listener; loadgen itself stays free of Fission auth code.
	WrapTransport func(http.RoundTripper) http.RoundTripper

	// ObserveHeader names a response header whose value is captured per
	// request and handed to Observe. Loadgen stays protocol-generic: the
	// caller decides what the header means (Fission's upgrade scoring reads
	// the RFC-0015 X-Fission-Component attribution header through this).
	ObserveHeader string
	// Observe, if set, is called once per issued request from the request
	// goroutine. status is the HTTP status code, or 0 when the request
	// produced no response at all (dial error, connection reset, client
	// timeout) — the transport-level failures a status-only view cannot
	// distinguish from the load driver's own saturation accounting.
	// headerVal is the response's ObserveHeader value ("" when absent or on
	// transport error). Observe must be safe for concurrent use.
	Observe func(status int, headerVal string, err error)
}

// HTTPTarget is a reusable, tuned HTTP client bound to one request shape. Its
// Do method satisfies Doer.
type HTTPTarget struct {
	cfg    HTTPTargetConfig
	client *http.Client
}

// NewHTTPTarget builds an HTTPTarget with a transport sized to the expected
// concurrency.
func NewHTTPTarget(cfg HTTPTargetConfig) *HTTPTarget {
	if cfg.Method == "" {
		cfg.Method = http.MethodGet
	}
	pool := max(cfg.Concurrency*2, 2)
	tr := &http.Transport{
		MaxIdleConns:        pool,
		MaxIdleConnsPerHost: pool,
		DisableKeepAlives:   !cfg.KeepAlive,
		IdleConnTimeout:     90 * time.Second,
	}
	var rt http.RoundTripper = tr
	if cfg.WrapTransport != nil {
		rt = cfg.WrapTransport(tr)
	}
	return &HTTPTarget{
		cfg:    cfg,
		client: &http.Client{Transport: rt, Timeout: cfg.Timeout},
	}
}

// Do issues one request and returns the number of response bytes read. A
// non-2xx status is reported as an error so it counts against the error rate.
func (t *HTTPTarget) Do(ctx context.Context) (int64, error) {
	var body io.Reader
	if len(t.cfg.Body) > 0 {
		body = bytes.NewReader(t.cfg.Body)
	}
	req, err := http.NewRequestWithContext(ctx, t.cfg.Method, t.cfg.URL, body)
	if err != nil {
		return 0, err
	}
	if t.cfg.Headers != nil {
		req.Header = maps.Clone(t.cfg.Headers)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		if t.cfg.Observe != nil {
			t.cfg.Observe(0, "", err)
		}
		return 0, err
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	var statusErr error
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		statusErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	if t.cfg.Observe != nil {
		t.cfg.Observe(resp.StatusCode, resp.Header.Get(t.cfg.ObserveHeader), statusErr)
	}
	return n, statusErr
}

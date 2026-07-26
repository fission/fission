// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/utils"
)

// Deliverer performs one delivery attempt of an async invocation and reports the
// outcome the settle matrix keys on. It is an interface so the dispatcher's
// settle logic is unit-testable with a scripted deliverer — no HTTP server.
type Deliverer interface {
	Deliver(ctx context.Context, env Envelope, invocationID string, attempt int) DeliveryResult
}

// DeliveryResult is one delivery attempt's outcome. Err is set for a transport
// failure (dial error, timeout, canceled context) where no HTTP response was
// received; StatusCode carries the HTTP status when a response arrived (Err nil).
type DeliveryResult struct {
	StatusCode int
	Err        error
	// Body is the function's response body, captured up to MaxPayloadBytes for a
	// destination result envelope (empty on a transport error, or when the function
	// declares no destination). BodyTruncated is true when the body was cut at the
	// cap or a mid-stream read left it incomplete.
	Body          []byte
	BodyTruncated bool
	// routeMiss is true when a 404 response carried the router's route-miss
	// marker (utils.HeaderRouteMiss) — i.e. the router itself reported "no such
	// route", as opposed to a 404 the function returned as its response. Only
	// the deliverer's own version-pin fallback keys on it; the dispatcher's
	// settle matrix never sees it (a function 404 settles like any other 4xx).
	routeMiss bool
}

// httpDeliverer POSTs to the router internal listener, byte-identical to the
// timer/mqtrigger publishers, and reports the response status. It does not log
// delivery outcomes: the dispatcher owns failure logging, where the invocation
// id / function / attempt context lives. The one exception is the RFC-0025
// version-pinned-route fallback below, which is a deliverer-internal retry the
// dispatcher never sees as a separate attempt, so it is the only place that can
// log it.
type httpDeliverer struct {
	client  *http.Client
	baseURL string
	logger  logr.Logger
}

// NewHTTPDeliverer builds a Deliverer that POSTs to the router internal listener
// at baseURL, HMAC-signing each request with the ServiceRouterInternal key when
// master is non-empty (the same signer the timer/mqtrigger publishers use, so
// the router's internal verifier accepts it). An empty master leaves requests
// unsigned (pass-through mode). A nil transport uses http.DefaultTransport.
func NewHTTPDeliverer(baseURL string, master []byte, transport http.RoundTripper, logger logr.Logger) Deliverer {
	if transport == nil {
		transport = http.DefaultTransport
	}
	if len(master) > 0 {
		transport = hmacauth.ServiceSigner(master, hmacauth.ServiceRouterInternal, transport, time.Now)
	}
	return &httpDeliverer{
		client:  &http.Client{Transport: transport},
		baseURL: strings.TrimRight(baseURL, "/"),
		logger:  logger,
	}
}

func (h *httpDeliverer) Deliver(ctx context.Context, env Envelope, invocationID string, attempt int) DeliveryResult {
	// Deliver at the function's canonical internal URL (UrlForFunction folds the
	// default namespace), preserving the query. The original trigger path is kept
	// in the envelope for inspection but not replayed as a subpath in phase 1 —
	// async delivery invokes the function, the body carries the event.
	funcPath := utils.UrlForFunction(env.Function, env.Namespace)

	// RFC-0025 Task 5: a version-pinned envelope tries the versioned internal
	// route (`:<version>` suffix, the same grammar buildInternalAliasHandler's
	// routes register at) first. A MARKED 404 there means the version's route
	// was GC'd between enqueue and this delivery attempt -- fall back to the
	// bare-name route immediately, as part of the SAME attempt, not a
	// redelivery cycle (the dispatcher's attempt/backoff accounting never
	// sees this as a retry).
	//
	// MARKER CONTRACT (closes the old HONESTY NOTE's double-invoke hole): the
	// router's internal listener stamps utils.HeaderRouteMiss on its OWN
	// not-found 404s -- internalRouteMissHandler, wired inside
	// newListenerMuxes so it survives the atomic mux swap -- and strips the
	// header from every function-proxied response (functionHandler's
	// ModifyResponse), so a function cannot spoof it. The fallback fires ONLY
	// on a marked 404 (result.routeMiss). A PLAIN 404 is the function's own
	// response: it is delivered exactly once and settles through the normal
	// non-2xx matrix (classify: permanent 4xx → dead-letter), never
	// re-delivered -- a function whose business logic legitimately answers
	// 404 is no longer double-invoked on the version-pinned path.
	if env.FunctionVersion != "" {
		result := h.deliverOnce(ctx, env, invocationID, attempt, h.targetURL(utils.UrlForFunctionRef(env.Function, env.Namespace, env.FunctionVersion), env.Query))
		if result.Err == nil && result.StatusCode == http.StatusNotFound && result.routeMiss {
			recordVersionFallback(ctx)
			h.logger.Info("async delivery: versioned route not found, falling back to bare function route",
				"namespace", env.Namespace, "function", env.Function, "version", env.FunctionVersion,
				"invocationId", invocationID, "attempt", attempt)
			return h.deliverOnce(ctx, env, invocationID, attempt, h.targetURL(funcPath, env.Query))
		}
		return result
	}
	return h.deliverOnce(ctx, env, invocationID, attempt, h.targetURL(funcPath, env.Query))
}

// targetURL joins the deliverer's baseURL with an internal-listener function
// path (from utils.UrlForFunction, optionally suffixed `:<version>`) and an
// optional query string.
func (h *httpDeliverer) targetURL(funcPath, query string) string {
	target := h.baseURL + "/" + strings.TrimPrefix(funcPath, "/")
	if query != "" {
		target += "?" + query
	}
	return target
}

// deliverOnce performs one HTTP attempt against target -- the primary
// versioned URL, or the bare-name fallback. Broken out of Deliver so the
// version-fallback retry above is a second call, not a duplicated request
// build (a bytes.Reader can only be sent once, so each call gets its own
// fresh one over env.Body).
func (h *httpDeliverer) deliverOnce(ctx context.Context, env Envelope, invocationID string, attempt int, target string) DeliveryResult {
	method := env.Method
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(env.Body))
	if err != nil {
		return DeliveryResult{Err: err}
	}
	for k, v := range env.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set(HeaderInvocationID, invocationID)
	req.Header.Set(HeaderInvocationAttempt, strconv.Itoa(attempt))
	req.Header.Set(HeaderInvocationDepth, strconv.Itoa(env.Depth))

	resp, err := h.client.Do(req)
	if err != nil {
		return DeliveryResult{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	// The router's route-miss marker (see the MARKER CONTRACT in Deliver): only
	// meaningful on a 404, and trustworthy because the router strips it from
	// function-proxied responses.
	routeMiss := resp.StatusCode == http.StatusNotFound && resp.Header.Get(utils.HeaderRouteMiss) != ""
	// Capture the body only for the destination this outcome can actually fire: a
	// 2xx settles to OnSuccess, any other status settles (now or on exhaustion) to
	// OnFailure. So skip the up-to-64KiB read when the relevant destination is unset
	// — a 2xx with only OnFailure, or a non-2xx with only OnSuccess, feeds nothing.
	// This only ever skips a body no destination would consume (it never drops one a
	// fire needs), and drains for keep-alive either way.
	is2xx := resp.StatusCode >= 200 && resp.StatusCode < 300
	needBody := (is2xx && env.OnSuccess != nil) || (!is2xx && env.OnFailure != nil)
	if !needBody {
		_, _ = io.Copy(io.Discard, resp.Body)
		return DeliveryResult{StatusCode: resp.StatusCode, routeMiss: routeMiss}
	}
	// Capture up to MaxPayloadBytes for a destination result envelope, flagging any
	// truncation (over the cap, or a mid-stream read error that leaves the body
	// incomplete), then drain the remainder so keep-alive can reuse the connection.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxPayloadBytes+1))
	truncated := readErr != nil || len(body) > MaxPayloadBytes
	if len(body) > MaxPayloadBytes {
		body = body[:MaxPayloadBytes]
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return DeliveryResult{StatusCode: resp.StatusCode, Body: body, BodyTruncated: truncated, routeMiss: routeMiss}
}

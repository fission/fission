//go:build integration

package framework

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

// RouterClient wraps an HTTP client pointed at the Fission router (typically
// the port-forwarded svc/router on 127.0.0.1:8888 in CI).
//
// Requests to /fission-function/... are unconditionally routed to the
// framework's `routerInternal` URL (typically 127.0.0.1:8889) and
// signed with HMAC-SHA256 because of the GHSA-3g33-6vg6-27m8 listener
// split — the public listener no longer hosts those routes. The
// framework reads FISSION_INTERNAL_AUTH_SECRET at startup; an empty
// secret leaves requests unsigned, which works against clusters where
// internalAuth.enabled=false (the verifier short-circuits to
// pass-through).
type RouterClient struct {
	baseURL  string
	internal string
	http     *http.Client
}

// Router returns an HTTP client targeting FISSION_ROUTER (or 127.0.0.1:8888).
func (f *Framework) Router(t *testing.T) *RouterClient {
	t.Helper()
	// The persistent http.Client uses a transport that signs requests
	// to /fission-function/... with the master HMAC key (when
	// configured). Other paths (HTTPTriggers, /router-healthz) go
	// through unsigned to match end-user behaviour against the public
	// listener.
	rt := http.DefaultTransport
	if len(f.internalAuthSecret) > 0 {
		rt = &routerSigningTransport{
			master: f.internalAuthSecret,
			inner:  rt,
		}
	}
	return &RouterClient{
		baseURL:  f.router,
		internal: f.routerInternal,
		http:     &http.Client{Timeout: 30 * time.Second, Transport: rt},
	}
}

// routerSigningTransport wraps the framework's outbound transport so
// that any request whose path begins with /fission-function/ is signed
// with the ServiceRouterInternal HMAC key. Other paths pass through
// unsigned.
type routerSigningTransport struct {
	master []byte
	inner  http.RoundTripper
}

func (t *routerSigningTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(r.URL.Path, "/fission-function/") {
		return t.inner.RoundTrip(r)
	}
	signer := hmacauth.ServiceSigner(t.master, hmacauth.ServiceRouterInternal, t.inner, time.Now)
	return signer.RoundTrip(r)
}

// BaseURL returns the configured router base URL (e.g. "http://127.0.0.1:8888").
func (r *RouterClient) BaseURL() string { return r.baseURL }

// Get performs a single GET against `path` (joined to BaseURL) and returns the
// body. Non-2xx is returned as an error.
func (r *RouterClient) Get(ctx context.Context, path string) (status int, body string, err error) {
	return r.do(ctx, http.MethodGet, path, "", nil)
}

// Post performs a single POST against `path` with the given content type and
// body. Returns status, response body, and any transport-level error.
func (r *RouterClient) Post(ctx context.Context, path, contentType string, body []byte) (status int, respBody string, err error) {
	return r.do(ctx, http.MethodPost, path, contentType, body)
}

func (r *RouterClient) do(ctx context.Context, method, path, contentType string, body []byte) (int, string, error) {
	// /fission-function/... lives only on the internal listener after
	// GHSA-3g33-6vg6-27m8; route to the internal base URL.
	// `r.internal` is always non-empty after framework setup
	// (defaults to http://127.0.0.1:8889 from
	// routerInternalURLFromEnv).
	base := r.baseURL
	p := ensureLeadingSlash(path)
	if strings.HasPrefix(p, "/fission-function/") {
		base = r.internal
	}
	url := base + p
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return 0, "", err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(b), nil
}

// routerPollTimeout is the budget GetEventually/PostEventually give for the
// router to converge to the expected response. It must cover the slowest
// reasonable case: function-update propagation under parallel CI load, which
// requires the poolmgr to observe the Function CRD update, invalidate its
// functionServiceMap entry, take a fresh pod from the pool, and re-specialize
// it with the new package. The previous 60s was tight on k8s 1.32/1.34 with a
// pool size of 3 and many concurrent tests competing for pool slots; 120s
// gives ~2× headroom while remaining short enough that genuinely broken
// tests still fail fast.
const routerPollTimeout = 120 * time.Second

// GetEventually polls a GET until the response satisfies `check` or the
// timeout elapses. Use this in place of bash's `curl --retry` after creating
// a route. 1s tick. Returns the last response body.
func (r *RouterClient) GetEventually(
	t *testing.T,
	ctx context.Context,
	path string,
	check ResponseCheck,
) string {
	t.Helper()
	var lastBody string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, body, err := r.Get(ctx, path)
		if !assert.NoErrorf(c, err, "router GET %q", path) {
			return
		}
		lastBody = body
		assert.Truef(c, check(status, body),
			"router GET %q: status=%d, body=%q did not satisfy check",
			path, status, truncate(body, 200))
	}, routerPollTimeout, 1*time.Second)
	return lastBody
}

// PostEventually polls a POST until the response satisfies `check` or the
// timeout elapses. The body bytes are reused on every retry.
func (r *RouterClient) PostEventually(
	t *testing.T,
	ctx context.Context,
	path, contentType string,
	body []byte,
	check ResponseCheck,
) string {
	t.Helper()
	var lastBody string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, respBody, err := r.Post(ctx, path, contentType, body)
		if !assert.NoErrorf(c, err, "router POST %q", path) {
			return
		}
		lastBody = respBody
		assert.Truef(c, check(status, respBody),
			"router POST %q: status=%d, body[:200]=%q did not satisfy check",
			path, status, truncate(respBody, 200))
	}, routerPollTimeout, 1*time.Second)
	return lastBody
}

// ResponseCheck inspects a router response and returns true if the test should
// stop polling.
type ResponseCheck func(status int, body string) bool

// BodyContains returns a ResponseCheck that succeeds when status is 2xx and the
// body contains the substring (case-insensitive).
func BodyContains(substr string) ResponseCheck {
	low := strings.ToLower(substr)
	return func(status int, body string) bool {
		return status >= 200 && status < 300 && strings.Contains(strings.ToLower(body), low)
	}
}

// LoadLoop fires GETs to path with a small inter-request gap until ctx is
// cancelled. Used by tests that need to feed sustained traffic to the canary
// controller while polling for its decisions to settle. Errors and non-2xx
// responses are silently ignored — the goal is sustained traffic, not
// measurement.
//
// Typical usage: spawn a goroutine, attach a t.Cleanup that cancels.
//
//	loadCtx, stopLoad := context.WithCancel(ctx)
//	t.Cleanup(stopLoad)
//	go f.Router(t).LoadLoop(loadCtx, "/myroute")
//	ns.WaitForFunctionWeight(...)
func (r *RouterClient) LoadLoop(ctx context.Context, path string) {
	tk := time.NewTicker(100 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			_, _, _ = r.Get(ctx, path)
		}
	}
}

func ensureLeadingSlash(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("...(%d more bytes)", len(s)-n)
}

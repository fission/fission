//go:build integration

package framework

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RouterClient wraps an HTTP client pointed at the Fission router (typically
// the port-forwarded svc/router on 127.0.0.1:8888 in CI).
type RouterClient struct {
	baseURL string
	http    *http.Client
}

// Router returns an HTTP client targeting FISSION_ROUTER (or 127.0.0.1:8888).
func (f *Framework) Router(t *testing.T) *RouterClient {
	t.Helper()
	return &RouterClient{
		baseURL: f.router,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// BaseURL returns the configured router base URL (e.g. "http://127.0.0.1:8888").
func (r *RouterClient) BaseURL() string { return r.baseURL }

// Get performs a single GET against `path` (joined to BaseURL) and returns the
// body. Non-2xx is returned as an error.
func (r *RouterClient) Get(ctx context.Context, path string) (status int, body string, err error) {
	url := r.baseURL + ensureLeadingSlash(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
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

// GetEventually polls a GET until the response satisfies `check` or the
// timeout elapses. Use this in place of bash's `curl --retry` after creating
// a route. 60s timeout, 1s tick. Returns the last response body.
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
	}, 60*time.Second, 1*time.Second)
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

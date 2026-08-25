// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// main_route_test.go composes the REAL production mux wiring for the
// highest-privilege route — POST /agents/{namespace}/{name} behind
// authz.Middleware(dispatcher.Handler()), exactly as main.go:594 mounts it —
// rather than testing Middleware against a trivial okHandler() stand-in
// (authz_test.go) or the Dispatcher alone with no auth in front of it
// (dispatcher_test.go). Neither of those proves the auth check actually runs
// BEFORE a turn reaches the dispatcher and the upstream function on the real
// route; this file does, using an upstream-hit counter as the falsifiable
// signal.
package agentruntime

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
)

// TestDispatchRouteAuthComposition asserts the triple the brief calls the
// highest-value coverage for the dispatch route: a correctly-scoped token
// reaches the upstream function (200), a wrong-namespace token is rejected
// by authz.Middleware BEFORE the dispatcher ever runs (403, zero upstream
// hits), and a token that fails verification entirely — garbage or expired —
// is rejected the same way (401, zero upstream hits).
func TestDispatchRouteAuthComposition(t *testing.T) {
	t.Parallel()
	key := []byte("route-composition-test-key")

	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)

	d, view, _ := newTestDispatcher(t, upstream.URL)
	view.Upsert(headerEntry("ns-a", "fn", 0))
	authz := NewAuthorizer(key)

	// The exact wiring main.go registers: the outer mux performs the
	// {namespace}/{name} match (setting the path values authz.Middleware
	// reads) before calling into the auth-wrapped dispatcher, which then
	// matches the same pattern again inside Handler().
	mux := http.NewServeMux()
	mux.Handle("POST /agents/{namespace}/{name}", authz.Middleware(d.Handler()))

	scopedTok := mintToken(t, key, jwt.MapClaims{
		"sub": "caller-1", "allowed_namespaces": []any{"ns-a"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	wrongNSTok := mintToken(t, key, jwt.MapClaims{
		"sub": "caller-1", "allowed_namespaces": []any{"ns-b"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	expiredTok := mintToken(t, key, jwt.MapClaims{
		"sub": "caller-1", "allowed_namespaces": []any{"ns-a"},
		"exp": time.Now().Add(-time.Hour).Unix(),
	})

	tests := []struct {
		name         string
		bearer       string
		wantStatus   int
		wantUpstream int64
	}{
		{"correctly-scoped token dispatches through to the upstream function", scopedTok, http.StatusOK, 1},
		{"wrong-namespace token is forbidden before dispatch", wrongNSTok, http.StatusForbidden, 0},
		{"garbage token is unauthorized before dispatch", "not-a-jwt", http.StatusUnauthorized, 0},
		{"expired token is unauthorized before dispatch", expiredTok, http.StatusUnauthorized, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstreamHits.Store(0)
			req := httptest.NewRequest(http.MethodPost, "/agents/ns-a/fn", strings.NewReader("hello"))
			req.Header.Set("Authorization", "Bearer "+tt.bearer)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			assert.Equalf(t, tt.wantStatus, rec.Code, "response status for %s", tt.name)
			assert.EqualValuesf(t, tt.wantUpstream, upstreamHits.Load(),
				"upstream hit count for %s — a rejected auth check must never reach the upstream function", tt.name)
		})
	}
}

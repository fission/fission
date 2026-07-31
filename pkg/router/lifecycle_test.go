// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"

	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/utils/httpmux"
)

func TestJitter(t *testing.T) {
	assert.Equal(t, time.Duration(0), jitter(0))
	assert.Equal(t, time.Duration(-5), jitter(-5))

	base := 100 * time.Millisecond
	maxJittered := base + time.Duration(0.2*float64(base))
	for range 1000 {
		j := jitter(base)
		assert.GreaterOrEqual(t, j, base, "jitter must never shorten the backoff")
		assert.LessOrEqual(t, j, maxJittered, "jitter must stay within +20%")
	}
}

func TestPanicRecoveryMiddleware(t *testing.T) {
	mw := panicRecoveryMiddleware(logr.Discard())

	t.Run("recovers panic and returns 502", func(t *testing.T) {
		h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("boom")
		}))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		assert.NotPanics(t, func() { h.ServeHTTP(rec, req) })
		assert.Equal(t, http.StatusBadGateway, rec.Code)
	})

	t.Run("re-panics ErrAbortHandler for net/http to handle", func(t *testing.T) {
		h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		}))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		assert.PanicsWithValue(t, http.ErrAbortHandler, func() { h.ServeHTTP(rec, req) })
	})

	t.Run("passes through non-panicking handlers", func(t *testing.T) {
		h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		assert.Equal(t, http.StatusTeapot, rec.Code)
	})
}

// TestRouterReadinessHandler pins the readiness gate: /readyz reports
// 200 only after the first successful mux build flips ready, keeping a
// freshly started or rolling pod out of the Service endpoints until its mux is
// populated.
func TestRouterReadinessHandler(t *testing.T) {
	tests := []struct {
		name  string
		ready bool
		// synced is nil when the slice-fed data plane is off, matching the
		// nil endpointsSynced the router leaves in that mode.
		synced *bool
		want   int
	}{
		{"mux built, slice plane off -> ready", true, nil, http.StatusOK},
		{"mux not built -> unavailable", false, nil, http.StatusServiceUnavailable},
		{"mux built, index synced -> ready", true, new(true), http.StatusOK},
		// The window this closes: the mux is serving, but the endpoint index
		// has not seen its informer replay, so warm traffic would fall back to
		// the executor — a latency cliff mid-upgrade (RFC-0028 §2).
		{"mux built, index still filling -> unavailable", true, new(false), http.StatusServiceUnavailable},
		{"index synced but mux not built -> unavailable", false, new(true), http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := &HTTPTriggerSet{}
			ts.ready.Store(tc.ready)
			if tc.synced != nil {
				synced := *tc.synced
				ts.endpointsSynced = func() bool { return synced }
			}
			rec := httptest.NewRecorder()
			ts.routerReadinessHandler(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestAuthMiddlewareExemptsProbes(t *testing.T) {
	fc := &config.FeatureConfig{}
	fc.AuthConfig.IsEnabled = true
	fc.AuthConfig.AuthUriPath = "/auth/login"

	h := authMiddleware(fc)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Unauthenticated probe endpoints must pass through so the kubelet
	// liveness/readiness probes work when auth is enabled.
	for _, p := range []string{"/readyz", "/router-healthz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		assert.Equalf(t, http.StatusOK, rec.Code, "%s must bypass auth", p)
	}

	// A normal function route with no token is still rejected.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some-function", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMutableRouterNilReturns503(t *testing.T) {
	mr := newMutableRouter(logr.Discard(), httpmux.New().Handler())
	mr.handler.Store(nil) // simulate the (should-never-happen) uninitialized state

	rec := httptest.NewRecorder()
	assert.NotPanics(t, func() {
		mr.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	})
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

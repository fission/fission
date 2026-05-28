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
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	k8sCache "k8s.io/client-go/tools/cache"
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

// fakeInformer embeds the SharedIndexInformer interface (nil) and overrides
// only HasSynced, which is all routerReadinessHandler calls.
type fakeInformer struct {
	k8sCache.SharedIndexInformer
	synced bool
}

func (f fakeInformer) HasSynced() bool { return f.synced }

func TestRouterReadinessHandler(t *testing.T) {
	synced := func(b bool) map[string]k8sCache.SharedIndexInformer {
		return map[string]k8sCache.SharedIndexInformer{"ns": fakeInformer{synced: b}}
	}

	tests := []struct {
		name    string
		trigger map[string]k8sCache.SharedIndexInformer
		fn      map[string]k8sCache.SharedIndexInformer
		want    int
	}{
		{"all synced", synced(true), synced(true), http.StatusOK},
		{"trigger not synced", synced(false), synced(true), http.StatusServiceUnavailable},
		{"function not synced", synced(true), synced(false), http.StatusServiceUnavailable},
		{"empty maps ready", map[string]k8sCache.SharedIndexInformer{}, map[string]k8sCache.SharedIndexInformer{}, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := &HTTPTriggerSet{triggerInformer: tc.trigger, funcInformer: tc.fn}
			rec := httptest.NewRecorder()
			ts.routerReadinessHandler(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestMutableRouterNilReturns503(t *testing.T) {
	mr := newMutableRouter(logr.Discard(), mux.NewRouter())
	mr.router.Store(nil) // simulate the (should-never-happen) uninitialized state

	rec := httptest.NewRecorder()
	assert.NotPanics(t, func() {
		mr.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	})
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

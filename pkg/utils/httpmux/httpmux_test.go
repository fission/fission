// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpmux

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ok is a handler that writes a marker so tests can assert which route served.
func ok(marker string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(marker))
	}
}

func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, target, nil))
	return rr
}

func TestExactAndMethod(t *testing.T) {
	t.Parallel()
	m := New()
	m.Handle("/v2/getServiceForFunction", ok("svc")).Methods("POST")
	m.Handle("/healthz", ok("health")).Methods("GET")
	h := m.Handler()

	t.Run("exact POST", func(t *testing.T) {
		rr := do(t, h, http.MethodPost, "/v2/getServiceForFunction")
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "svc", rr.Body.String())
	})
	t.Run("wrong method 405", func(t *testing.T) {
		rr := do(t, h, http.MethodGet, "/v2/getServiceForFunction")
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})
	t.Run("unknown path 404", func(t *testing.T) {
		rr := do(t, h, http.MethodGet, "/nope")
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})
	t.Run("exact / is not a catch-all", func(t *testing.T) {
		m2 := New()
		m2.Handle("/", ok("root")).Methods("GET")
		rr := do(t, m2.Handler(), http.MethodGet, "/other")
		assert.Equal(t, http.StatusNotFound, rr.Code, "Handle(\"/\") is exact, unlike stdlib's catch-all")
	})
}

func TestMultipleMethodsAndQueryDispatch(t *testing.T) {
	t.Parallel()
	// Mirrors storagesvc: GET and HEAD on the same path go to different handlers.
	m := New()
	m.Handle("/v1/archive", ok("get")).Methods("GET")
	m.Handle("/v1/archive", ok("head")).Methods("HEAD")
	m.Handle("/v1/archive", ok("post")).Methods("POST")
	h := m.Handler()

	assert.Equal(t, "get", do(t, h, http.MethodGet, "/v1/archive?id=x").Body.String())
	assert.Equal(t, "post", do(t, h, http.MethodPost, "/v1/archive").Body.String())
	// HEAD has its own route — GET must not swallow it (unlike stdlib's GET→HEAD).
	headRR := do(t, h, http.MethodHead, "/v1/archive")
	assert.Equal(t, http.StatusOK, headRR.Code)
}

func TestPrefix(t *testing.T) {
	t.Parallel()
	m := New()
	m.HandlePrefix("/api/", ok("api")).Methods("GET")
	m.Handle("/api", ok("exact")).Methods("GET")
	h := m.Handler()

	assert.Equal(t, "exact", do(t, h, http.MethodGet, "/api").Body.String())
	assert.Equal(t, "api", do(t, h, http.MethodGet, "/api/things").Body.String())
}

func TestHostMatching(t *testing.T) {
	t.Parallel()
	m := New()
	m.Handle("/x", ok("hosted")).Methods("GET").Host("api.example.com")
	m.Handle("/x", ok("hostless")).Methods("GET")
	h := m.Handler()

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Host = "api.example.com"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, "hosted", rr.Body.String(), "host-specific route wins when host matches")

	assert.Equal(t, "hostless", do(t, h, http.MethodGet, "/x").Body.String(),
		"falls through to the host-less route otherwise")
}

func TestFirstMatchWins(t *testing.T) {
	t.Parallel()
	m := New()
	m.Handle("/dup", ok("first")).Methods("GET")
	m.Handle("/dup", ok("second")).Methods("GET")
	assert.Equal(t, "first", do(t, m.Handler(), http.MethodGet, "/dup").Body.String(),
		"registration order is precedence")
}

func TestPatternInContext(t *testing.T) {
	t.Parallel()
	var got string
	m := New()
	m.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		got = Pattern(r)
		w.WriteHeader(http.StatusOK)
	}).Methods("GET")
	do(t, m.Handler(), http.MethodGet, "/healthz")
	assert.Equal(t, "/healthz", got)
}

func TestMiddlewareOrder(t *testing.T) {
	t.Parallel()
	var order []string
	mw := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	m := New(WithMiddleware(mw("outer"), mw("inner")))
	m.HandleFunc("/x", func(w http.ResponseWriter, _ *http.Request) {
		order = append(order, "handler")
	}).Methods("GET")
	do(t, m.Handler(), http.MethodGet, "/x")
	assert.Equal(t, []string{"outer", "inner", "handler"}, order,
		"first-added middleware runs first (outermost)")
}

func TestEncodedPath(t *testing.T) {
	t.Parallel()
	m := New(WithEncodedPath())
	m.Handle("/a%2Fb", ok("raw")).Methods("GET")
	// httptest request keeps the raw path; EscapedPath returns "/a%2Fb".
	req := httptest.NewRequest(http.MethodGet, "/a%2Fb", nil)
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "raw", rr.Body.String())
}

// fakeRecorder records the metrics calls so tests can assert instrumentation.
type fakeRecorder struct {
	mu       sync.Mutex
	inflight int
	maxFlt   int
	observed []string // "pattern method code"
}

func (f *fakeRecorder) InFlightInc(_, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight++
	if f.inflight > f.maxFlt {
		f.maxFlt = f.inflight
	}
}
func (f *fakeRecorder) InFlightDec(_, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight--
}
func (f *fakeRecorder) Observe(pattern, method string, code int, _ time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed = append(f.observed, fmt.Sprintf("%s %s %d", pattern, method, code))
}

func TestMetricsRecorded(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	m := New(WithMetrics(rec))
	m.HandleFunc("/v1/archive", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}).Methods("POST")
	do(t, m.Handler(), http.MethodPost, "/v1/archive")

	assert.Equal(t, []string{"/v1/archive POST 201"}, rec.observed,
		"records the route pattern (not the raw URL), method, and status")
	assert.Equal(t, 0, rec.inflight, "in-flight gauge balanced")
	assert.Equal(t, 1, rec.maxFlt, "in-flight incremented during the request")
}

func TestWebsocketBypassesMetrics(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	m := New(WithMetrics(rec))
	m.Handle("/ws", ok("")).Methods("GET")
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "keep-alive, Upgrade")
	m.Handler().ServeHTTP(httptest.NewRecorder(), req)
	assert.Empty(t, rec.observed, "websocket upgrades are not instrumented")
}

func TestFlushPassthrough(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	m := New(WithMetrics(rec))
	flushed := make(chan struct{}, 1)
	m.HandleFunc("/stream", func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		require.True(t, ok, "instrumented ResponseWriter must still be a Flusher")
		_, _ = w.Write([]byte("chunk"))
		f.Flush()
		flushed <- struct{}{}
	}).Methods("GET")
	do(t, m.Handler(), http.MethodGet, "/stream")
	select {
	case <-flushed:
	default:
		t.Fatal("handler did not flush")
	}
}

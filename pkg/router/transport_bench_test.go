// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// newTestParams builds one tsRoundTripperParams the way production wiring
// does: shared across every request of a trigger set, so connection pooling
// (RFC-0014) is observable across RetryingRoundTripper instances.
func newTestParams(maxRetries, svcAddrRetryCount int) *tsRoundTripperParams {
	return &tsRoundTripperParams{
		timeout:           50 * time.Millisecond,
		timeoutExponent:   2,
		keepAliveTime:     30 * time.Second,
		maxRetries:        maxRetries,
		svcAddrRetryCount: svcAddrRetryCount,
	}
}

// connCountingServer wraps an httptest server counting NEW TCP connections —
// the direct observable for keep-alive pooling.
func connCountingServer(t *testing.T, handler http.Handler) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var newConns atomic.Int64
	srv := httptest.NewUnstartedServer(handler)
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			newConns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, &newConns
}

func drain(t testing.TB, resp *http.Response) {
	t.Helper()
	_, err := io.Copy(io.Discard, resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

// TestTransportReusesConnections is the RFC-0014 headline regression guard:
// serialized warm requests through the SAME shared params (as production
// shares them across all of a trigger set's requests) must reuse pooled
// connections instead of dialing per request. Before the shared transport,
// every request built a fresh http.Transport and dialed fresh — the count
// here equaled the request count.
func TestTransportReusesConnections(t *testing.T) {
	t.Parallel()
	srv, newConns := connCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)

	params := newTestParams(3, 2)
	const requests = 20
	for range requests {
		// Fresh per-request RetryingRoundTripper, exactly like the handler.
		rrt := &RetryingRoundTripper{
			logger:      loggerfactory.GetLogger(),
			resolver:    &scriptedResolver{answers: []*url.URL{u}},
			tapper:      &nopTapper{},
			fn:          poolmgrFnForTransport(),
			params:      params,
			funcTimeout: 5 * time.Second,
		}
		req := httptest.NewRequest("GET", "http://router.example/fission-function/fn", nil)
		resp, err := rrt.RoundTrip(req)
		require.NoError(t, err)
		drain(t, resp)
	}

	assert.LessOrEqual(t, newConns.Load(), int64(2),
		"serialized warm requests must reuse pooled connections (got %d new conns for %d requests)",
		newConns.Load(), requests)
}

// BenchmarkRoundTripWarm measures the per-request cost of the transport path
// with a warm upstream: allocations and wall time per proxied request,
// constructing a fresh RetryingRoundTripper per iteration exactly like the
// handler does.
func BenchmarkRoundTripWarm(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		b.Fatal(err)
	}

	params := newTestParams(3, 2)
	logger := loggerfactory.GetLogger()
	fn := poolmgrFnForTransport()

	b.ReportAllocs()
	for b.Loop() {
		rrt := &RetryingRoundTripper{
			logger:      logger,
			resolver:    &scriptedResolver{answers: []*url.URL{u}},
			tapper:      &nopTapper{},
			fn:          fn,
			params:      params,
			funcTimeout: 5 * time.Second,
		}
		req := httptest.NewRequest("GET", "http://router.example/fission-function/fn", nil)
		resp, err := rrt.RoundTrip(req)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}

// benchHandler builds a functionHandler against upstream; hoisted toggles the
// RFC-0014 phase-2 precomputation (the fallback path is the pre-hoist
// behavior, so the two benchmarks compare old vs new in one binary).
func benchHandler(b *testing.B, upstream *url.URL, hoisted bool) functionHandler {
	logger := loggerfactory.GetLogger()
	fn := poolmgrFnForTransport()
	params := newTestParams(3, 2)
	fh := functionHandler{
		logger:               logger,
		resolver:             &scriptedResolver{answers: []*url.URL{upstream}, fromCache: false},
		tapper:               &nopTapper{},
		function:             fn,
		tsRoundTripperParams: params,
		functionTimeoutMap:   map[crd.CacheKeyUG]int{},
	}
	if hoisted {
		fh.rtLogger = logger.WithName("roundtripper")
		fh.policyByUID = precomputePolicies(map[string]*fv1.Function{fn.Name: fn}, fh.functionTimeoutMap, params.streamIdleDefault)
	}
	return fh
}

func benchProxyHandler(b *testing.B, hoisted bool) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		b.Fatal(err)
	}
	fh := benchHandler(b, u, hoisted)

	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest("GET", "http://router.example/fn", nil)
		rec := httptest.NewRecorder()
		fh.handler(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("unexpected status %d", rec.Code)
		}
	}
}

// BenchmarkProxyHandlerHoisted measures the full handler path with the
// phase-2 precomputed logger/policy; BenchmarkProxyHandlerPreHoist exercises
// the fallback (per-request computation) path for comparison.
func BenchmarkProxyHandlerHoisted(b *testing.B)  { benchProxyHandler(b, true) }
func BenchmarkProxyHandlerPreHoist(b *testing.B) { benchProxyHandler(b, false) }

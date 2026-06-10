// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// scriptedResolver returns pre-scripted answers per Resolve call and records
// invalidations — the fake seam that makes the retry transport testable
// without a live executor stub (impossible before the RFC-0002 extraction).
type scriptedResolver struct {
	answers     []*url.URL // consumed per call; last answer repeats
	fromCache   bool
	calls       atomic.Int64
	invalidated atomic.Int64
}

func (s *scriptedResolver) Resolve(_ context.Context, _ *fv1.Function) (ResolvedEntry, error) {
	n := int(s.calls.Add(1)) - 1
	if n >= len(s.answers) {
		n = len(s.answers) - 1
	}
	return ResolvedEntry{SvcURL: s.answers[n], FromCache: s.fromCache}, nil
}

func (s *scriptedResolver) Invalidate(*fv1.Function, *url.URL) { s.invalidated.Add(1) }

// nopTapper records untaps.
type nopTapper struct {
	taps   atomic.Int64
	untaps atomic.Int64
}

func (n *nopTapper) Tap(*fv1.Function, *url.URL) { n.taps.Add(1) }
func (n *nopTapper) UnTap(context.Context, *fv1.Function, *url.URL) error {
	n.untaps.Add(1)
	return nil
}

func poolmgrFnForTransport() *fv1.Function {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default"}}
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypePoolmgr
	return fn
}

func newTestRRT(resolver AddressResolver, tapper Tapper, maxRetries, svcAddrRetryCount int) *RetryingRoundTripper {
	return &RetryingRoundTripper{
		logger:   loggerfactory.GetLogger(),
		resolver: resolver,
		tapper:   tapper,
		fn:       poolmgrFnForTransport(),
		params: &tsRoundTripperParams{
			timeout:           50 * time.Millisecond,
			timeoutExponent:   2,
			keepAliveTime:     time.Second,
			maxRetries:        maxRetries,
			svcAddrRetryCount: svcAddrRetryCount,
		},
		funcTimeout: 5 * time.Second,
	}
}

func TestRoundTripProxiesToResolvedAddress(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)

	tapper := &nopTapper{}
	rrt := newTestRRT(&scriptedResolver{answers: []*url.URL{u}}, tapper, 3, 2)
	req := httptest.NewRequest("GET", "http://router.example/fission-function/fn", nil)

	resp, err := rrt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusTeapot, resp.StatusCode)

	// Classic poolmgr requests untap (async) once the round trip returns.
	assert.Eventually(t, func() bool { return tapper.untaps.Load() == 1 }, time.Second, 10*time.Millisecond)
}

// TestRoundTripRetriesDialErrorThenSucceeds: a connection-refused address is
// re-resolved on the next attempt (without cache invalidation — only timeout
// errors climb the invalidation ladder), and the second (live) address serves
// the request. Golden behavior locked by this test.
func TestRoundTripRetriesDialErrorThenSucceeds(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	live, err := url.Parse(upstream.URL)
	require.NoError(t, err)

	// A dead-but-fast-failing address: a closed port on localhost.
	dead := mustParseURL(t, "http://127.0.0.1:1")

	resolver := &scriptedResolver{answers: []*url.URL{dead, live}, fromCache: true}
	rrt := newTestRRT(resolver, &nopTapper{}, 5, 1)
	req := httptest.NewRequest("GET", "http://router.example/fission-function/fn", nil)

	resp, err := rrt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.GreaterOrEqual(t, resolver.calls.Load(), int64(2), "must re-resolve after the dead address")
	assert.Zero(t, resolver.invalidated.Load(), "connection refused does not invalidate (timeout-only ladder)")
}

// TestRoundTripInvalidatesCacheOnTimeouts: dial timeouts increment the retry
// counter; at svcAddrRetryCount the cached address is invalidated and the next
// attempt re-resolves.
func TestRoundTripInvalidatesCacheOnTimeouts(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	live, err := url.Parse(upstream.URL)
	require.NoError(t, err)

	// A blackholed address (TEST-NET-1, RFC 5737): dials time out rather than
	// being refused, exercising the timeout ladder.
	blackhole := mustParseURL(t, "http://192.0.2.1:80")

	resolver := &scriptedResolver{answers: []*url.URL{blackhole, live}, fromCache: true}
	rrt := newTestRRT(resolver, &nopTapper{}, 6, 1) // svcAddrRetryCount=1 → invalidate after first timeout
	req := httptest.NewRequest("GET", "http://router.example/fission-function/fn", nil)

	resp, err := rrt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.GreaterOrEqual(t, resolver.invalidated.Load(), int64(1), "cached timing-out address must be invalidated at the threshold")
	assert.GreaterOrEqual(t, resolver.calls.Load(), int64(2), "must re-resolve after invalidation")
}

// TestRoundTripExhaustsRetries: dial errors all the way exhaust maxRetries and
// surface an error.
func TestRoundTripExhaustsRetries(t *testing.T) {
	t.Parallel()
	dead := mustParseURL(t, "http://127.0.0.1:1")
	resolver := &scriptedResolver{answers: []*url.URL{dead}, fromCache: true}
	rrt := newTestRRT(resolver, &nopTapper{}, 3, 1)
	req := httptest.NewRequest("GET", "http://router.example/fission-function/fn", nil)

	resp, err := rrt.RoundTrip(req) //nolint:bodyclose // resp is nil on error
	require.Error(t, err)
	assert.Nil(t, resp)
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

// releaseTrackingResolver scripts answers with per-resolution release tracking.
type releaseTrackingResolver struct {
	answers  []*url.URL
	calls    atomic.Int64
	released []*atomic.Int64
	mu       sync.Mutex
}

func (s *releaseTrackingResolver) Resolve(_ context.Context, _ *fv1.Function) (ResolvedEntry, error) {
	n := int(s.calls.Add(1)) - 1
	if n >= len(s.answers) {
		n = len(s.answers) - 1
	}
	counter := &atomic.Int64{}
	s.mu.Lock()
	s.released = append(s.released, counter)
	s.mu.Unlock()
	var once sync.Once
	return ResolvedEntry{
		SvcURL:    s.answers[n],
		FromCache: true,
		Release:   func() { once.Do(func() { counter.Add(1) }) },
	}, nil
}

func (s *releaseTrackingResolver) Invalidate(*fv1.Function, *url.URL) {}

// TestStreamingRetryReleasesAbandonedSlots guards the RFC-0002 admission-slot
// leak: a streaming request whose first admitted endpoint fails to dial must
// release that slot when it re-resolves — the handler defer only ever sees the
// LAST resolution, and a leaked slot pins the pod's in-flight counter forever.
func TestStreamingRetryReleasesAbandonedSlots(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	live, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	dead := mustParseURL(t, "http://127.0.0.1:1")

	resolver := &releaseTrackingResolver{answers: []*url.URL{dead, live}}
	rrt := newTestRRT(resolver, &nopTapper{}, 5, 1)
	rrt.policy = proxyPolicy{streaming: true} // streaming: no per-resolve untap defers

	req := httptest.NewRequest("GET", "http://router.example/fission-function/fn", nil)
	resp, err := rrt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.GreaterOrEqual(t, resolver.calls.Load(), int64(2), "the dead endpoint must force a re-resolve")

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	for i, counter := range resolver.released[:len(resolver.released)-1] {
		assert.Equalf(t, int64(1), counter.Load(), "abandoned slot %d must be released on re-resolve", i)
	}
	last := resolver.released[len(resolver.released)-1]
	assert.Zero(t, last.Load(), "the serving slot is held until the handler-level release (stream drain)")
}

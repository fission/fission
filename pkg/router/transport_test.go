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

// nopTapper records taps/untaps and their target URLs.
type nopTapper struct {
	taps      atomic.Int64
	untaps    atomic.Int64
	lastTap   atomic.Pointer[url.URL]
	lastUntap atomic.Pointer[url.URL]
}

func (n *nopTapper) Tap(_ *fv1.Function, u *url.URL) {
	n.taps.Add(1)
	n.lastTap.Store(u)
}
func (n *nopTapper) UnTap(_ context.Context, _ *fv1.Function, u *url.URL) error {
	n.untaps.Add(1)
	n.lastUntap.Store(u)
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
	answers     []*url.URL
	tapURL      *url.URL // optional: entries carry it (endpoint-LB shape)
	calls       atomic.Int64
	invalidated atomic.Int64
	released    []*atomic.Int64
	mu          sync.Mutex
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
		TapURL:    s.tapURL,
		FromCache: true,
		Release:   func() { once.Do(func() { counter.Add(1) }) },
	}, nil
}

func (s *releaseTrackingResolver) Invalidate(*fv1.Function, *url.URL) { s.invalidated.Add(1) }

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
	require.GreaterOrEqual(t, resolver.invalidated.Load(), int64(1),
		"a dial failure on an index-admitted endpoint must invalidate (quarantine) it, not just retry")

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	for i, counter := range resolver.released[:len(resolver.released)-1] {
		assert.Equalf(t, int64(1), counter.Load(), "abandoned slot %d must be released on re-resolve", i)
	}
	last := resolver.released[len(resolver.released)-1]
	assert.Zero(t, last.Load(), "the serving slot is held until the handler-level release (stream drain)")
}

// TestSettleReleasesEndpointLBSlots: a newdeploy endpoint-LB entry carries a
// router-local release; settle must return it at request completion and must
// NOT fire the poolmgr UnTap RPC for it. (Before settle, both dispatch sites
// were poolmgr-gated, so LB slots would never have been returned.)
func TestSettleReleasesEndpointLBSlots(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	live, err := url.Parse(upstream.URL)
	require.NoError(t, err)

	resolver := &releaseTrackingResolver{answers: []*url.URL{live}}
	tapper := &nopTapper{}
	rrt := newTestRRT(resolver, tapper, 3, 2)
	rrt.fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypeNewdeploy

	req := httptest.NewRequest("GET", "http://router.example/fission-function/fn", nil)
	resp, err := rrt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	require.Len(t, resolver.released, 1)
	assert.Equal(t, int64(1), resolver.released[0].Load(), "the LB slot must be released at RoundTrip return")
	assert.Zero(t, tapper.untaps.Load(), "no UnTap RPC for router-admitted non-poolmgr entries")
}

// TestSettleDispatchMatrix pins the full release-vs-UnTap dispatch table:
// router-admitted entries (release != nil) return the slot and never UnTap,
// regardless of executor type; executor-resolved poolmgr entries UnTap (on
// the tap target, not the dial target); deploy-backed VIP entries do nothing.
// The newdeploy/release-nil cell is the load-bearing one — the executor-type
// check inside settle is the ONLY guard since the call sites stopped being
// poolmgr-gated.
func TestSettleDispatchMatrix(t *testing.T) {
	t.Parallel()
	dial := mustParseURL(t, "http://10.0.0.9:8888")
	tap := mustParseURL(t, "http://svc-fn.default:80")

	cases := []struct {
		name         string
		executorType fv1.ExecutorType
		withRelease  bool
		wantUntaps   int64
		wantRelease  bool
	}{
		{"poolmgr executor-resolved unTaps", fv1.ExecutorTypePoolmgr, false, 1, false},
		{"poolmgr router-admitted releases", fv1.ExecutorTypePoolmgr, true, 0, true},
		{"newdeploy VIP is a no-op", fv1.ExecutorTypeNewdeploy, false, 0, false},
		{"newdeploy endpoint-LB releases", fv1.ExecutorTypeNewdeploy, true, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tapper := &nopTapper{}
			rrt := newTestRRT(&scriptedResolver{answers: []*url.URL{dial}}, tapper, 1, 1)
			rrt.fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = tc.executorType

			var released atomic.Int64
			var release func()
			if tc.withRelease {
				release = func() { released.Add(1) }
			}
			rrt.settle(release, tap)

			if tc.wantUntaps > 0 {
				assert.Eventually(t, func() bool { return tapper.untaps.Load() == tc.wantUntaps },
					time.Second, 10*time.Millisecond)
				assert.Equal(t, tap.Host, tapper.lastUntap.Load().Host,
					"UnTap must target the tap URL (Service for LB entries), not the dial URL")
			} else {
				assert.Never(t, func() bool { return tapper.untaps.Load() > 0 },
					200*time.Millisecond, 20*time.Millisecond,
					"no UnTap RPC may fire for this cell")
			}
			assert.Equal(t, tc.wantRelease, released.Load() == 1)
		})
	}
}

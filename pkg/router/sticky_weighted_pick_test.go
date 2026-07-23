// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// weightedAliasHandlerForPickAdmitTest builds a functionHandler shaped
// exactly like buildTriggerHandler/buildInternalAliasHandler would for a
// weighted FunctionAlias: two backend snapshots in functionMap, a 50/50
// fnWeightDistributionList, and a stickySource distinct from either
// snapshot (the RFC-0025 Task 5 "live function" source) declaring sticky
// routing via the given header name.
func weightedAliasHandlerForPickAdmitTest(t *testing.T, resolver *scriptedResolver, headerName string) functionHandler {
	t.Helper()
	// Distinct Names (unlike a real weighted alias's primary/secondary, which
	// share Name/UID and differ only in Generation) so scriptedResolver's
	// lastFnName test seam can tell which one Resolve actually saw.
	primary := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "hello-primary", Namespace: "default"}}
	secondary := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "hello-secondary", Namespace: "default"}}
	live := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"},
		Spec: fv1.FunctionSpec{
			State: &fv1.StateConfig{Sticky: &fv1.StickyConfig{Source: fv1.StickySourceHeader, Name: headerName}},
		},
	}
	functionMap := map[string]*fv1.Function{"primary": primary, "secondary": secondary}
	dist := []functionWeightDistribution{
		{name: "primary", weight: 50, sumPrefix: 50},
		{name: "secondary", weight: 50, sumPrefix: 100},
	}
	params := newTestParams(1, 1)
	fh := functionHandler{
		logger:                   loggerfactory.GetLogger(),
		resolver:                 resolver,
		tapper:                   &nopTapper{},
		functionMap:              functionMap,
		fnWeightDistributionList: dist,
		stickySource:             live,
		tsRoundTripperParams:     params,
		functionTimeoutMap:       map[crd.CacheKeyUG]int{},
	}
	fh.rtLogger = fh.logger.WithName("roundtripper")
	fh.policyByUID = precomputePolicies(functionMap, fh.functionTimeoutMap, params.streamIdleDefault)
	return fh
}

// TestHandler_PickKeyEqualsAdmitKey is the RFC-0025 Task 5 headline
// consistency test: for a weighted route, the sticky key the deterministic
// pick consumed and the sticky key the resolver's Admit ranking received are
// literally the same value, for many distinct keys -- because handler()
// computes it once and both consumers read the same variable.
func TestHandler_PickKeyEqualsAdmitKey(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)

	const headerName = "X-Session-Id"
	resolver := &scriptedResolver{answers: []*url.URL{u}}
	fh := weightedAliasHandlerForPickAdmitTest(t, resolver, headerName)

	for i := range 200 {
		key := fmt.Sprintf("session-%d", i)
		req := httptest.NewRequest("GET", "http://router.example/hello", nil)
		req.Header.Set(headerName, key)
		rec := httptest.NewRecorder()

		fh.handler(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		gotKey := resolver.lastStickyKey.Load()
		require.NotNil(t, gotKey, "resolver.Resolve must have been called")
		assert.Equal(t, key, *gotKey, "the key Admit ranked by must equal the sticky key the request declared")

		// Independently recompute the pick the SAME way getCanaryBackend does,
		// off the identical key, and confirm it agrees with what actually
		// went out: the resolver's lastFnName (the fn handler() actually
		// resolved through, i.e. what got proxied to) must equal the
		// recomputed pick's name.
		wantFn := getCanaryBackend(fh.functionMap, fh.fnWeightDistributionList, key)
		require.NotNil(t, wantFn)
		gotFnName := resolver.lastFnName.Load()
		require.NotNil(t, gotFnName, "resolver.Resolve must have been called with a function")
		assert.Equal(t, wantFn.Name, *gotFnName, "the backend actually resolved through must match the independently recomputed pick")
	}
}

// TestHandler_PickKeyEqualsAdmitKey_SameKeyStableAcrossRequests re-confirms
// the fixed-key stability property (already pinned at the getCanaryBackend
// unit level) end to end through the full handler path, including the
// resolver's Admit-side key.
func TestHandler_PickKeyEqualsAdmitKey_SameKeyStableAcrossRequests(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)

	const headerName = "X-Session-Id"
	const key = "sticky-user-42"
	resolver := &scriptedResolver{answers: []*url.URL{u}}
	fh := weightedAliasHandlerForPickAdmitTest(t, resolver, headerName)

	first := getCanaryBackend(fh.functionMap, fh.fnWeightDistributionList, key).Name
	for range 100 {
		req := httptest.NewRequest("GET", "http://router.example/hello", nil)
		req.Header.Set(headerName, key)
		rec := httptest.NewRecorder()
		fh.handler(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		gotKey := resolver.lastStickyKey.Load()
		require.NotNil(t, gotKey)
		assert.Equal(t, key, *gotKey)
		assert.Equal(t, first, getCanaryBackend(fh.functionMap, fh.fnWeightDistributionList, key).Name,
			"the same key must pick the same backend on every request")
	}
}

// TestHandler_UnkeyedWeightedRoute_StillAdmitsUnkeyed proves the "unkeyed
// keeps rand" behavior extends through the full handler path: with no
// sticky-declared header on the request (or no StickyConfig at all), the
// resolver's Admit ranking still receives "" (default endpoint pick), not a
// spurious key.
func TestHandler_UnkeyedWeightedRoute_StillAdmitsUnkeyed(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)

	resolver := &scriptedResolver{answers: []*url.URL{u}}
	fh := weightedAliasHandlerForPickAdmitTest(t, resolver, "X-Session-Id")

	req := httptest.NewRequest("GET", "http://router.example/hello", nil) // no header set
	rec := httptest.NewRecorder()
	fh.handler(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	gotKey := resolver.lastStickyKey.Load()
	require.NotNil(t, gotKey)
	assert.Empty(t, *gotKey, "a declared-but-missing sticky key admits unkeyed")
}

// legacyCanaryHandlerForStickyTest builds a functionHandler shaped like the
// legacy FunctionReferenceTypeFunctionWeights canary: two distinct, arbitrarily-
// named functions in functionMap/dist, stickySource left nil (resolveByFunctionWeights
// never sets it -- there is no single canonical sticky config across two
// independent functions to key the PICK on).
func legacyCanaryHandlerForStickyTest(t *testing.T, resolver *scriptedResolver, functionMap map[string]*fv1.Function, dist []functionWeightDistribution) functionHandler {
	t.Helper()
	params := newTestParams(1, 1)
	fh := functionHandler{
		logger:                   loggerfactory.GetLogger(),
		resolver:                 resolver,
		tapper:                   &nopTapper{},
		functionMap:              functionMap,
		fnWeightDistributionList: dist,
		// stickySource intentionally nil: resolveByFunctionWeights never sets it.
		tsRoundTripperParams: params,
		functionTimeoutMap:   map[crd.CacheKeyUG]int{},
	}
	fh.rtLogger = fh.logger.WithName("roundtripper")
	fh.policyByUID = precomputePolicies(functionMap, fh.functionTimeoutMap, params.streamIdleDefault)
	return fh
}

// TestHandler_LegacyFunctionWeights_PreservesPrePickStickyBehavior pins the
// hybrid fix for the legacy FunctionReferenceTypeFunctionWeights canary
// (distinct named functions, no single canonical sticky-config source BEFORE
// the pick): the PICK itself stays unkeyed/random (stickySource is nil, so
// getCanaryBackend never sees a key), but AFTER the pick lands,
// functionHandler.handler() recomputes the sticky key from the CHOSEN
// backend's own StickyConfig -- restoring the pre-Task-5 Admit-side behavior
// byte-for-byte, where whichever canary function won the random draw had its
// own declared StickyConfig honored for endpoint ranking.
//
// The weight is pinned 100/0 (not 50/50) so the pick is deterministic (the
// Task-5 boundary fix guarantees a 100-weight entry always wins), making the
// test non-flaky without needing statistical tolerance.
func TestHandler_LegacyFunctionWeights_PreservesPrePickStickyBehavior(t *testing.T) {
	t.Parallel()

	// Each subtest gets its OWN upstream server: t.Run subtests below call
	// t.Parallel() themselves, which pauses them until this outer function
	// body returns -- a server shared via this function's own defer would
	// already be closed by the time a parallel child actually dials it.
	newUpstream := func(t *testing.T) *url.URL {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)
		u, err := url.Parse(srv.URL)
		require.NoError(t, err)
		return u
	}

	t.Run("chosen backend's own StickyConfig drives the key", func(t *testing.T) {
		t.Parallel()
		u := newUpstream(t)
		withSticky := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "default"},
			Spec: fv1.FunctionSpec{
				State: &fv1.StateConfig{Sticky: &fv1.StickyConfig{Source: fv1.StickySourceHeader, Name: "X-Session-Id"}},
			},
		}
		other := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "default"}}
		functionMap := map[string]*fv1.Function{"a": withSticky, "b": other}
		// 100/0: the pick always lands on "a", the sticky-configured backend.
		dist := []functionWeightDistribution{
			{name: "a", weight: 100, sumPrefix: 100},
			{name: "b", weight: 0, sumPrefix: 100},
		}
		resolver := &scriptedResolver{answers: []*url.URL{u}}
		fh := legacyCanaryHandlerForStickyTest(t, resolver, functionMap, dist)

		req := httptest.NewRequest("GET", "http://router.example/fn", nil)
		req.Header.Set("X-Session-Id", "some-user")
		rec := httptest.NewRecorder()
		fh.handler(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		gotKey := resolver.lastStickyKey.Load()
		require.NotNil(t, gotKey)
		assert.Equal(t, "some-user", *gotKey,
			"the chosen backend's own StickyConfig must drive the Admit-side key, restoring pre-Task-5 behavior")
	})

	t.Run("chosen backend with no StickyConfig admits unkeyed", func(t *testing.T) {
		t.Parallel()
		u := newUpstream(t)
		withSticky := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "default"},
			Spec: fv1.FunctionSpec{
				State: &fv1.StateConfig{Sticky: &fv1.StickyConfig{Source: fv1.StickySourceHeader, Name: "X-Session-Id"}},
			},
		}
		other := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "default"}}
		functionMap := map[string]*fv1.Function{"a": withSticky, "b": other}
		// 0/100: the pick always lands on "b", which declares no StickyConfig.
		dist := []functionWeightDistribution{
			{name: "a", weight: 0, sumPrefix: 0},
			{name: "b", weight: 100, sumPrefix: 100},
		}
		resolver := &scriptedResolver{answers: []*url.URL{u}}
		fh := legacyCanaryHandlerForStickyTest(t, resolver, functionMap, dist)

		req := httptest.NewRequest("GET", "http://router.example/fn", nil)
		req.Header.Set("X-Session-Id", "some-user") // present, but "b" doesn't declare Sticky at all
		rec := httptest.NewRecorder()
		fh.handler(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		gotKey := resolver.lastStickyKey.Load()
		require.NotNil(t, gotKey)
		assert.Empty(t, *gotKey, "the chosen backend has no StickyConfig, so Admit stays unkeyed")
	})
}

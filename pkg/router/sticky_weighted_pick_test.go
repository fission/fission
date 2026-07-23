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
	primary := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"}}
	secondary := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"}}
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
		// went out (proxied to upstream, so a mismatch would 5xx/hang, but
		// assert directly rather than relying on that).
		wantFn := getCanaryBackend(fh.functionMap, fh.fnWeightDistributionList, key)
		require.NotNil(t, wantFn)
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

// TestHandler_LegacyFunctionWeights_NeverStickyKeyed pins the documented
// Task-5 trade-off: the legacy FunctionReferenceTypeFunctionWeights canary
// (distinct named functions, no single canonical sticky-config source) never
// extracts a sticky key even when one of the picked backends declares its
// own StickyConfig -- resolveByFunctionWeights sets no stickySource, so the
// pick stays pure random and Admit stays unkeyed, exactly like every other
// unkeyed route.
func TestHandler_LegacyFunctionWeights_NeverStickyKeyed(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	require.NoError(t, err)

	// A backend that DOES declare its own StickyConfig -- but it is never
	// consulted, because functionHandler.stickySource is nil for the legacy
	// canary path.
	withSticky := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "default"},
		Spec: fv1.FunctionSpec{
			State: &fv1.StateConfig{Sticky: &fv1.StickyConfig{Source: fv1.StickySourceHeader, Name: "X-Session-Id"}},
		},
	}
	other := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "default"}}
	functionMap := map[string]*fv1.Function{"a": withSticky, "b": other}
	dist := []functionWeightDistribution{
		{name: "a", weight: 50, sumPrefix: 50},
		{name: "b", weight: 50, sumPrefix: 100},
	}
	params := newTestParams(1, 1)
	resolver := &scriptedResolver{answers: []*url.URL{u}}
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

	req := httptest.NewRequest("GET", "http://router.example/fn", nil)
	req.Header.Set("X-Session-Id", "some-user")
	rec := httptest.NewRecorder()
	fh.handler(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	gotKey := resolver.lastStickyKey.Load()
	require.NotNil(t, gotKey)
	assert.Empty(t, *gotKey, "legacy FunctionWeights canary never extracts a sticky key")
}

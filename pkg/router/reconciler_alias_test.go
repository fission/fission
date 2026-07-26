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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/router/routetable"
)

// incrAlias builds a FunctionAlias CR whose Status.ResolvedVersion is
// pre-set, as if the leader-elected pkg/versioning.AliasReconciler had
// already resolved it — the router reconciler never writes this status, only
// reads it.
func incrAlias(name, ns, fnName, resolvedVersion string, gen int64) *fv1.FunctionAlias {
	return &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: gen, UID: types.UID("alias-" + name)},
		Spec:       fv1.FunctionAliasSpec{FunctionName: fnName},
		Status:     fv1.FunctionAliasStatus{ResolvedVersion: resolvedVersion},
	}
}

// incrAliasTrigger builds an HTTPTrigger pinned to a FunctionAlias (an
// RFC-0025 Alias reference).
func incrAliasTrigger(name, ns string, gen int64, url, fnName, alias string) *fv1.HTTPTrigger {
	return &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: gen, UID: types.UID("trig-" + name)},
		Spec: fv1.HTTPTriggerSpec{
			RelativeURL: url,
			Methods:     []string{http.MethodGet},
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName, Name: fnName, Alias: alias,
			},
		},
	}
}

// internalSpecFor returns the live InternalSpec materialized at
// (ns, fnName, suffix), failing the test if it is not present.
func internalSpecFor(t *testing.T, ts *HTTPTriggerSet, ns, fnName, suffix string) routetable.InternalSpec {
	t.Helper()
	for _, s := range ts.routeTable.InternalSnapshot() {
		if s.Key.Namespace == ns && s.Key.Name == fnName && s.Key.Suffix == suffix {
			return s
		}
	}
	t.Fatalf("no internal route for %s/%s:%s", ns, fnName, suffix)
	return routetable.InternalSpec{}
}

// TestAliasReconcilerCreateMaterializesBothNamespaceForms pins the internal
// route grammar (RFC-0025 deliverable 3): a live FunctionAlias resolves to a
// materialized `:<alias>` internal route reachable through BOTH the
// namespace-qualified and (for the default namespace) folded forms, mirroring
// utils.UrlForFunction's own folding.
func TestAliasReconcilerCreateMaterializesBothNamespaceForms(t *testing.T) {
	fn := incrFn("hello", "default", 1)
	v := incrVersion("hello-v1", "default", "hello", fn.UID, 1, 1)
	alias := incrAlias("prod", "default", "hello", "hello-v1", 1)
	ts, cl := newIncrementalTS(t, fn, v, alias)

	r := &functionAliasReconciler{logger: ts.logger, client: cl, ts: ts}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "prod"}})
	require.NoError(t, err)
	requireSignal(t, ts)
	ts.materialize(t.Context())

	_, internal := muxes(ts)
	assert.True(t, muxMatches(internal, http.MethodPost, "/fission-function/hello:prod"),
		"folded default-namespace form must serve")
	assert.True(t, muxMatches(internal, http.MethodPost, "/fission-function/default/hello:prod"),
		"namespace-qualified form must serve")
	assert.False(t, muxMatches(internal, http.MethodPost, "/fission-function/hello"),
		"the plain (unsuffixed) function route is untouched by the alias")
}

// TestAliasReconcilerNonDefaultNamespace pins the namespace-qualified-only
// form for a non-default namespace (utils.UrlForFunction never folds it).
func TestAliasReconcilerNonDefaultNamespace(t *testing.T) {
	fn := incrFn("hello", "myns", 1)
	v := incrVersion("hello-v1", "myns", "hello", fn.UID, 1, 1)
	alias := incrAlias("prod", "myns", "hello", "hello-v1", 1)
	ts, cl := newIncrementalTS(t, fn, v, alias)

	r := &functionAliasReconciler{logger: ts.logger, client: cl, ts: ts}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "myns", Name: "prod"}})
	require.NoError(t, err)
	requireSignal(t, ts)
	ts.materialize(t.Context())

	_, internal := muxes(ts)
	assert.True(t, muxMatches(internal, http.MethodPost, "/fission-function/myns/hello:prod"))
	assert.False(t, muxMatches(internal, http.MethodPost, "/fission-function/hello:prod"),
		"a non-default namespace is never folded")
}

// TestAliasRepointIsHandlerSwapOnlyZeroDrift is the RFC-mandated code-level
// zero-drift gate: repointing an already-materialized alias (Status.
// ResolvedVersion moving from one FunctionVersion to another, as the
// leader-elected alias resolver does on a rollout) must be a pure
// HandlerSwapped — no materializer signal — and a subsequent resync pass
// must find ZERO drift, proving the incremental apply already left the table
// fully converged.
func TestAliasRepointIsHandlerSwapOnlyZeroDrift(t *testing.T) {
	fn := incrFn("hello", "default", 1)
	// Distinct FunctionGeneration per version (versioning.VersionedFunction's
	// invariant: one (UID, Generation) maps to at most one FunctionVersion) —
	// this is what makes ApplyFunction's generation-keyed change detection
	// see the repoint as a real backend change.
	v1 := incrVersion("hello-v1", "default", "hello", fn.UID, 1, 1)
	v2 := incrVersion("hello-v2", "default", "hello", fn.UID, 2, 2)
	alias := incrAlias("prod", "default", "hello", "hello-v1", 1)
	ts, cl := newIncrementalTS(t, fn, v1, v2, alias)
	r := &functionAliasReconciler{logger: ts.logger, client: cl, ts: ts}

	// Seed: an initial resync establishes the fully-reconciled baseline
	// (functions, versions, aliases, triggers all applied once) — exactly
	// the state production is in by the time an alias event fires, so the
	// zero-drift assertion below actually isolates "did the repoint alone
	// leave the table converged" instead of also catching objects this test
	// never separately reconciled.
	_, err := ts.resync(t.Context(), true)
	require.NoError(t, err)
	ts.materialize(t.Context())
	drainSignals(ts) // the initial population's debounced signal(s) must not leak into the assertions below

	before := internalSpecFor(t, ts, "default", "hello", "prod")

	// Repoint: update the alias's status in the fake client (simulating the
	// leader-elected resolver) and re-reconcile.
	repointed := alias.DeepCopy()
	repointed.Status.ResolvedVersion = "hello-v2"
	require.NoError(t, cl.Update(t.Context(), repointed))

	_, err = r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "prod"}})
	require.NoError(t, err)
	requireNoSignal(t, ts) // the headline assertion: no materializer run

	after := internalSpecFor(t, ts, "default", "hello", "prod")
	assert.Same(t, before.Handler, after.Handler, "the HandlerRef identity is stable across a repoint (atomic swap, not a new route)")
	// Revision is now aliasRouteGeneration's hash (folds alias.Generation +
	// every resolved target's Generation), not a literal Generation value —
	// it must simply have MOVED, proving the swap actually picked up v2.
	assert.NotEqual(t, before.Revision, after.Revision, "the SAME ref now serves the v2 target")

	// Zero-drift: a resync pass immediately after must find nothing to
	// correct.
	drift, err := ts.resync(t.Context(), false)
	require.NoError(t, err)
	assert.Zero(t, drift, "the incremental repoint already left the table fully converged")
	requireNoSignal(t, ts)
}

// TestAliasCascadeRepointsHTTPTriggerHandlerSwapped pins deliverable 2(a): an
// alias repoint cascades through TriggersForAlias to re-apply every
// HTTPTrigger consuming it, and — because the shape is unchanged — the
// result is HandlerSwapped, never ShapeChanged/a materializer signal.
func TestAliasCascadeRepointsHTTPTriggerHandlerSwapped(t *testing.T) {
	fn := incrFn("hello", "default", 1)
	v1 := incrVersion("hello-v1", "default", "hello", fn.UID, 1, 1)
	v2 := incrVersion("hello-v2", "default", "hello", fn.UID, 2, 2)
	alias := incrAlias("prod", "default", "hello", "hello-v1", 1)
	trigger := incrAliasTrigger("t1", "default", 1, "/hello", "hello", "prod")
	ts, cl := newIncrementalTS(t, fn, v1, v2, alias, trigger)
	r := &functionAliasReconciler{logger: ts.logger, client: cl, ts: ts}

	// Seed: the trigger admits through the alias, AND the alias reconciler
	// materializes its own :<alias> internal route — the fully-reconciled
	// baseline a repoint event arrives against in production.
	res, err := ts.applyTriggerIncremental(t.Context(), trigger)
	require.NoError(t, err)
	require.Equal(t, routetable.ShapeChanged, res, "first apply resolves through the alias and admits")
	_, err = r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "prod"}})
	require.NoError(t, err)
	requireSignal(t, ts)
	ts.materialize(t.Context())

	snap := ts.routeTable.Snapshot()
	require.Len(t, snap, 1)
	assert.Contains(t, snap[0].FnGens, "hello@hello-v1", "the route's FnGens key names the alias's CURRENT target")
	assert.Equal(t, map[string]int64{"prod": 1}, snap[0].AliasGens,
		"the route is indexed under the alias it consumed, at its observed generation")

	// Repoint the alias and reconcile it: the cascade must re-apply t1 as a
	// pure HandlerSwapped (same shape — /hello is unchanged).
	repointed := alias.DeepCopy()
	repointed.Status.ResolvedVersion = "hello-v2"
	require.NoError(t, cl.Update(t.Context(), repointed))

	_, err = r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "prod"}})
	require.NoError(t, err)
	requireNoSignal(t, ts)

	snap = ts.routeTable.Snapshot()
	require.Len(t, snap, 1)
	assert.Contains(t, snap[0].FnGens, "hello@hello-v2", "the cascade re-resolved the trigger onto the NEW target")
	assert.NotContains(t, snap[0].FnGens, "hello@hello-v1")
}

// TestAliasReconcilerDeleteRemovesInternalRouteAndDropsTriggers pins
// deliverable 2's alias-DELETE contract: the materialized `:<alias>` route is
// removed, and every trigger that consumed the alias goes unresolved (the
// existing errFunctionNotFound path) and stops serving.
func TestAliasReconcilerDeleteRemovesInternalRouteAndDropsTriggers(t *testing.T) {
	fn := incrFn("hello", "default", 1)
	v1 := incrVersion("hello-v1", "default", "hello", fn.UID, 1, 1)
	alias := incrAlias("prod", "default", "hello", "hello-v1", 1)
	trigger := incrAliasTrigger("t1", "default", 1, "/hello", "hello", "prod")
	ts, cl := newIncrementalTS(t, fn, v1, alias, trigger)
	fc := fissionfake.NewSimpleClientset(trigger)
	ts.fissionClient = fc

	r := &functionAliasReconciler{logger: ts.logger, client: cl, ts: ts}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "prod"}})
	require.NoError(t, err)
	_, err = ts.applyTriggerIncremental(t.Context(), trigger)
	require.NoError(t, err)
	requireSignal(t, ts)
	ts.materialize(t.Context())

	public, internal := muxes(ts)
	require.True(t, muxMatches(public, http.MethodGet, "/hello"))
	require.True(t, muxMatches(internal, http.MethodPost, "/fission-function/hello:prod"))

	require.NoError(t, cl.Delete(t.Context(), alias))
	_, err = r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "prod"}})
	require.NoError(t, err)
	requireSignal(t, ts)
	ts.materialize(t.Context())

	public, internal = muxes(ts)
	assert.False(t, muxMatches(internal, http.MethodPost, "/fission-function/hello:prod"),
		"the deleted alias's internal route must drop")
	assert.False(t, muxMatches(public, http.MethodGet, "/hello"),
		"a trigger consuming the deleted alias must stop serving")

	got, err := fc.CoreV1().HTTPTriggers("default").Get(t.Context(), "t1", metav1.GetOptions{})
	require.NoError(t, err)
	requireCondition(t, got, fv1.HTTPTriggerConditionRouteAdmitted, metav1.ConditionFalse, fv1.HTTPTriggerReasonFunctionNotFound)
}

// TestVersionReconcilerMaterializesAndDeletes pins the `:<version>` half of
// deliverable 3: a live FunctionVersion materializes both namespace forms of
// its internal route, and its DELETE event removes it.
func TestVersionReconcilerMaterializesAndDeletes(t *testing.T) {
	fn := incrFn("hello", "default", 1)
	v := incrVersion("hello-v1", "default", "hello", fn.UID, 1, 1)
	ts, cl := newIncrementalTS(t, fn, v)

	r := &functionVersionReconciler{logger: ts.logger, client: cl, ts: ts}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "hello-v1"}})
	require.NoError(t, err)
	requireSignal(t, ts)
	ts.materialize(t.Context())

	_, internal := muxes(ts)
	assert.True(t, muxMatches(internal, http.MethodPost, "/fission-function/hello:hello-v1"))
	assert.True(t, muxMatches(internal, http.MethodPost, "/fission-function/default/hello:hello-v1"))

	require.NoError(t, cl.Delete(t.Context(), v))
	_, err = r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "hello-v1"}})
	require.NoError(t, err)
	requireSignal(t, ts)
	ts.materialize(t.Context())

	_, internal = muxes(ts)
	assert.False(t, muxMatches(internal, http.MethodPost, "/fission-function/hello:hello-v1"))
}

// TestResyncHealsMissedAliasEvent extends TestIncrementalResyncHealsDrift to
// FunctionAlias: an alias created behind the reconciler's back (no
// functionAliasReconciler in sight) must still be materialized by the
// periodic resync — the drift guard covers the alias/version internal-route
// sweep, not just triggers/functions.
func TestResyncHealsMissedAliasEvent(t *testing.T) {
	fn := incrFn("hello", "default", 1)
	v := incrVersion("hello-v1", "default", "hello", fn.UID, 1, 1)
	alias := incrAlias("prod", "default", "hello", "hello-v1", 1)
	ts, _ := newIncrementalTS(t, fn, v, alias)

	drift, err := ts.resync(t.Context(), true)
	require.NoError(t, err)
	assert.Positive(t, drift, "the initial pass reports what it populated, including the alias route")
	ts.materialize(t.Context())

	_, internal := muxes(ts)
	assert.True(t, muxMatches(internal, http.MethodPost, "/fission-function/hello:prod"),
		"the resync sweep must materialize the alias route with no reconciler involved")

	// A second pass, with nothing changed, must be fully converged.
	drift, err = ts.resync(t.Context(), false)
	require.NoError(t, err)
	assert.Zero(t, drift, "a converged table has nothing left for resync to correct")
}

// versionRecordingResolver is a minimal AddressResolver that always resolves
// to the same upstream (an httptest.Server) but records which resolved
// version (fv1.FUNCTION_VERSION label) each call carried — the direct
// observable for "did the weighted pick actually alternate backends" without
// needing two distinguishable upstream servers.
type versionRecordingResolver struct {
	mu    sync.Mutex
	url   *url.URL
	calls map[string]int
}

func (r *versionRecordingResolver) Resolve(_ context.Context, fn *fv1.Function, _ string) (ResolvedEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[fn.Labels[fv1.FUNCTION_VERSION]]++
	return ResolvedEntry{SvcURL: r.url}, nil
}

func (r *versionRecordingResolver) Invalidate(*fv1.Function, *url.URL, InvalidateReason) {}

// snapshotAndReset returns the per-version call counts recorded so far and
// clears them, so a test can assert on each traffic phase independently.
func (r *versionRecordingResolver) snapshotAndReset() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.calls
	r.calls = nil
	return out
}

// wireVersionRecordingProxy points ts's proxy plumbing (address resolver,
// tapper, round-tripper params) at a stub 200-upstream and returns the
// recording resolver, so a test can drive a route's live handler and observe
// which resolved version each request landed on.
func wireVersionRecordingProxy(t *testing.T, ts *HTTPTriggerSet) *versionRecordingResolver {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	upstreamURL, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	resolver := &versionRecordingResolver{url: upstreamURL}
	ts.addressResolver = resolver
	ts.tapper = &nopTapper{}
	ts.tsRoundTripperParams = newTestParams(1, 1)
	return resolver
}

// TestInternalAliasRouteServesWeightedSplit is the fix for the reported spec
// gap: a weighted FunctionAlias's materialized `:<alias>` internal route
// must serve the SAME split an HTTPTrigger referencing it would, not just the
// primary target — the RFC's "weighted aliases work uniformly on all trigger
// type for free, because the weighted pick happens router-side" applies to
// MQ/timer/kubewatcher/MCP invocations too, and those all land on this
// internal route (never an HTTPTrigger). Drives the materialized route's live
// handler directly N times and asserts BOTH backends were resolved.
func TestInternalAliasRouteServesWeightedSplit(t *testing.T) {
	fn := incrFn("hello", "default", 1)
	v1 := incrVersion("hello-v1", "default", "hello", fn.UID, 1, 1)
	v2 := incrVersion("hello-v2", "default", "hello", fn.UID, 2, 2)
	alias := incrAlias("prod", "default", "hello", "hello-v1", 1)
	alias.Spec.Weight = new(50)
	alias.Spec.SecondaryVersion = "hello-v2"
	ts, _ := newIncrementalTS(t, fn, v1, v2, alias)
	resolver := wireVersionRecordingProxy(t, ts)

	res, err := ts.applyAliasInternalRoute(t.Context(), alias)
	require.NoError(t, err)
	require.Equal(t, routetable.ShapeChanged, res)

	spec := internalSpecFor(t, ts, "default", "hello", "prod")

	const n = 200
	for range n {
		rr := httptest.NewRecorder()
		spec.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/fission-function/hello:prod", nil))
		require.Equal(t, http.StatusOK, rr.Code)
	}

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	assert.Positive(t, resolver.calls["hello-v1"], "the primary target must receive traffic")
	assert.Positive(t, resolver.calls["hello-v2"], "the secondary target must receive traffic too — the split, not primary-only")
	assert.Equal(t, n, resolver.calls["hello-v1"]+resolver.calls["hello-v2"], "every request resolved to one of the two targets")
}

// TestInternalAliasRouteWeightChangeIsHandlerSwapped pins that
// aliasRouteGeneration reacts to a WEIGHT-only edit (same two targets, new
// split) — a case a naive "just the resolved target's Generation" scheme
// would miss (neither target's Generation moves on a weight edit).
func TestInternalAliasRouteWeightChangeIsHandlerSwapped(t *testing.T) {
	fn := incrFn("hello", "default", 1)
	v1 := incrVersion("hello-v1", "default", "hello", fn.UID, 1, 1)
	v2 := incrVersion("hello-v2", "default", "hello", fn.UID, 2, 2)
	alias := incrAlias("prod", "default", "hello", "hello-v1", 1)
	alias.Spec.Weight = new(90)
	alias.Spec.SecondaryVersion = "hello-v2"
	ts, cl := newIncrementalTS(t, fn, v1, v2, alias)

	res, err := ts.applyAliasInternalRoute(t.Context(), alias)
	require.NoError(t, err)
	require.Equal(t, routetable.ShapeChanged, res)
	before := internalSpecFor(t, ts, "default", "hello", "prod")

	// Weight-only edit: same primary/secondary targets, new split, and a
	// spec write bumps alias.Generation (Status is untouched). Persisted to
	// the client (not just the in-memory copy) so applyAliasInternalRoute's
	// resolveByAlias — which always re-Gets the alias — actually sees it,
	// exactly as the reconciler's own re-Get would in production.
	reweighted := alias.DeepCopy()
	reweighted.Generation = 2
	reweighted.Spec.Weight = new(10)
	require.NoError(t, cl.Update(t.Context(), reweighted))

	res, err = ts.applyAliasInternalRoute(t.Context(), reweighted)
	require.NoError(t, err)
	assert.Equal(t, routetable.HandlerSwapped, res, "a weight-only edit must swap the handler (never a no-op)")

	after := internalSpecFor(t, ts, "default", "hello", "prod")
	assert.Same(t, before.Handler, after.Handler, "still an atomic swap, not a new route")
	assert.NotEqual(t, before.Revision, after.Revision, "the weight edit must be visible in the change-detection value")
}

// hasInternalRoute reports whether the table currently materializes an
// internal route at (ns, fnName, suffix).
func hasInternalRoute(ts *HTTPTriggerSet, ns, fnName, suffix string) bool {
	for _, s := range ts.routeTable.InternalSnapshot() {
		if s.Key.Namespace == ns && s.Key.Name == fnName && s.Key.Suffix == suffix {
			return true
		}
	}
	return false
}

// TestDeleteAliasScopedToOwnFunctionCrossFunctionSuffixCollision pins the
// invariant that an alias named "hello-v1" for function "world" and a
// FunctionVersion literally named "hello-v1" for function "hello" share the
// same Suffix but are DISTINCT InternalKeys (different function names).
// Deleting the alias must remove ONLY its own route (world:hello-v1) and
// leave function hello's own hello-v1 version route untouched — an unscoped
// DeleteInternalBySuffix would nuke both, self-healing only via the 60s
// resync (a real drift-metric increment, violating the zero-drift bar). A
// resync run immediately after the delete must find nothing left to correct.
func TestDeleteAliasScopedToOwnFunctionCrossFunctionSuffixCollision(t *testing.T) {
	fnHello := incrFn("hello", "default", 1)
	vHelloV1 := incrVersion("hello-v1", "default", "hello", fnHello.UID, 1, 1)
	fnWorld := incrFn("world", "default", 1)
	vWorldV1 := incrVersion("world-v1", "default", "world", fnWorld.UID, 1, 1)
	// The alias's OWN name happens to collide with function "hello"'s
	// "hello-v1" FunctionVersion name — allowed by the webhook, since the
	// guard only rejects a collision with the alias's OWN function's scheme
	// ("world-v<seq>", not "hello-v<seq>").
	alias := incrAlias("hello-v1", "default", "world", "world-v1", 1)

	ts, cl := newIncrementalTS(t, fnHello, vHelloV1, fnWorld, vWorldV1, alias)

	// Seed: an initial resync establishes the fully-reconciled baseline
	// (BOTH functions, BOTH versions, and the alias all applied once) —
	// exactly the state production is in by the time a delete event fires,
	// so the zero-drift assertion below isolates "did the delete alone
	// leave the table converged" instead of also catching objects this test
	// never separately populated.
	_, err := ts.resync(t.Context(), true)
	require.NoError(t, err)
	require.True(t, hasInternalRoute(ts, "default", "hello", "hello-v1"), "precondition: function hello's version route exists")
	require.True(t, hasInternalRoute(ts, "default", "world", "hello-v1"), "precondition: function world's alias route exists")

	// Delete the ALIAS (not the version) — from the client too, matching a
	// real DELETE event (the reconciler's Get returns NotFound and THEN
	// calls deleteAliasIncremental; leaving the object live in the client
	// would make the next resync re-materialize it, masking this test).
	require.NoError(t, cl.Delete(t.Context(), alias))
	err = ts.deleteAliasIncremental(t.Context(), types.NamespacedName{Namespace: "default", Name: "hello-v1"})
	require.NoError(t, err)

	assert.False(t, hasInternalRoute(ts, "default", "world", "hello-v1"), "the deleted alias's own route must be gone")
	assert.True(t, hasInternalRoute(ts, "default", "hello", "hello-v1"), "function hello's UNRELATED version route must survive")

	// Zero drift: the surviving route was never actually broken, so a
	// resync immediately after must find nothing to correct.
	drift, err := ts.resync(t.Context(), false)
	require.NoError(t, err)
	assert.Zero(t, drift, "the innocent function's route must not have been touched, so there is nothing to heal")
}

// TestAliasWeightOnlyEditReachesHTTPTriggerHandlerSwapped pins the C2 fix: a
// WEIGHT-only FunctionAlias edit (`fission alias update --weight`, or an
// alias-mode canary stepping its split) changes neither the resolved
// BackendKey set nor any pinned snapshot's Generation — only the alias's own
// spec Generation moves. Folding that Generation into the trigger's route
// identity (RouteSpec.AliasGens) makes the alias event's cascade land as a
// HandlerSwapped whose new closure serves the new distribution; pre-fix the
// re-apply answered NoChange and PUBLIC traffic stayed on the stale split for
// the whole rollout (the internal `:<alias>` route already folded
// alias.Generation via aliasRouteGeneration — only the public path was
// missed). Also asserts the drift-symmetry half: once the edit settles, a
// resync pass recomputes the identical identity and corrects NOTHING.
func TestAliasWeightOnlyEditReachesHTTPTriggerHandlerSwapped(t *testing.T) {
	fn := incrFn("hello", "default", 1)
	v1 := incrVersion("hello-v1", "default", "hello", fn.UID, 1, 1)
	v2 := incrVersion("hello-v2", "default", "hello", fn.UID, 2, 2)
	alias := incrAlias("prod", "default", "hello", "hello-v1", 1)
	alias.Spec.Weight = new(100)
	alias.Spec.SecondaryVersion = "hello-v2"
	trigger := incrAliasTrigger("t1", "default", 1, "/hello", "hello", "prod")
	ts, cl := newIncrementalTS(t, fn, v1, v2, alias, trigger)
	resolver := wireVersionRecordingProxy(t, ts)

	// Seed the fully-reconciled baseline (trigger route + internal routes) —
	// the state production is in by the time a weight edit lands.
	_, err := ts.resync(t.Context(), true)
	require.NoError(t, err)
	ts.materialize(t.Context())
	drainSignals(ts)

	snap := ts.routeTable.Snapshot()
	require.Len(t, snap, 1)
	handler := snap[0].Handler
	drive := func(n int) {
		t.Helper()
		for range n {
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/hello", nil))
			require.Equal(t, http.StatusOK, rr.Code)
		}
	}

	drive(40)
	calls := resolver.snapshotAndReset()
	require.Equal(t, 40, calls["hello-v1"], "weight 100 must send every request to the primary")

	// Weight-only edit: same {primary, secondary} target set, new split. A
	// spec write bumps ONLY alias.Generation.
	reweighted := &fv1.FunctionAlias{}
	require.NoError(t, cl.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "prod"}, reweighted))
	reweighted.Generation = 2
	reweighted.Spec.Weight = new(0)
	require.NoError(t, cl.Update(t.Context(), reweighted))

	// The alias event's trigger cascade lands exactly here
	// (reapplyTriggersForAlias → applyTriggerIncremental): the re-apply must
	// be a HandlerSwapped — pre-fix it was NoChange.
	res, err := ts.applyTriggerIncremental(t.Context(), trigger)
	require.NoError(t, err)
	assert.Equal(t, routetable.HandlerSwapped, res,
		"a weight-only alias edit must swap the public trigger's handler (pre-fix: NoChange)")
	requireNoSignal(t, ts) // the split lives in the handler closure, never in the mux shape

	snap = ts.routeTable.Snapshot()
	require.Len(t, snap, 1)
	assert.Same(t, handler, snap[0].Handler, "atomic swap on the SAME ref, not a re-registration")

	drive(40)
	calls = resolver.snapshotAndReset()
	assert.Zero(t, calls["hello-v1"], "the stale 100/0 split must be gone from public traffic")
	assert.Equal(t, 40, calls["hello-v2"], "the new 0/100 split must serve immediately")

	// Settle the alias's own internal route too (a real alias event does both
	// halves via applyAliasIncremental), then drift symmetry: the resync
	// rebuilds every RouteSpec from the live cache through the same resolver,
	// so a settled weight-only edit must show ZERO drift — the CI bar.
	_, err = ts.applyAliasIncremental(t.Context(), reweighted)
	require.NoError(t, err)
	drift, err := ts.resync(t.Context(), false)
	require.NoError(t, err)
	assert.Zero(t, drift, "a settled weight-only edit must not register as resync drift")
	requireNoSignal(t, ts)
}

// TestLiveFunctionStickyEditReachesAliasRoutedTrigger pins the stickySource
// half of the C2 fix: an alias resolution's sticky-config source is the LIVE
// function (resolveResult.stickySource), whose Generation appears nowhere in
// FnGens (only the pinned snapshots' do) — so a live Spec.State.Sticky edit
// used to cascade to the alias-routed trigger via fnIndex only for
// ApplyTrigger to answer NoChange, leaving the handler closed over the stale
// (or absent) sticky config forever. RouteSpec.StickyGen makes the cascade a
// HandlerSwapped whose new closure honors the edited config.
func TestLiveFunctionStickyEditReachesAliasRoutedTrigger(t *testing.T) {
	fn := incrFn("hello", "default", 1)
	v1 := incrVersion("hello-v1", "default", "hello", fn.UID, 1, 1)
	v2 := incrVersion("hello-v2", "default", "hello", fn.UID, 2, 2)
	alias := incrAlias("prod", "default", "hello", "hello-v1", 1)
	alias.Spec.Weight = new(50)
	alias.Spec.SecondaryVersion = "hello-v2"
	trigger := incrAliasTrigger("t1", "default", 1, "/hello", "hello", "prod")
	ts, cl := newIncrementalTS(t, fn, v1, v2, alias, trigger)
	resolver := wireVersionRecordingProxy(t, ts)

	_, err := ts.resync(t.Context(), true)
	require.NoError(t, err)
	ts.materialize(t.Context())
	drainSignals(ts)

	snap := ts.routeTable.Snapshot()
	require.Len(t, snap, 1)
	require.Equal(t, int64(1), snap[0].StickyGen,
		"the live function (gen 1) is the alias resolution's sticky-config source")
	handler := snap[0].Handler
	drive := func(n int) {
		t.Helper()
		for range n {
			req := httptest.NewRequest(http.MethodGet, "/hello", nil)
			req.Header.Set("X-Session-Id", "session-abc")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			require.Equal(t, http.StatusOK, rr.Code)
		}
	}

	// No sticky config yet: the keyed header is ignored, the 50/50 pick is
	// random, and both backends receive traffic.
	drive(100)
	calls := resolver.snapshotAndReset()
	require.Positive(t, calls["hello-v1"], "unkeyed 50/50 must reach the primary")
	require.Positive(t, calls["hello-v2"], "unkeyed 50/50 must reach the secondary")

	// Sticky edit on the LIVE function (a spec change bumps its Generation).
	// The pinned snapshots are untouched, so FnGens cannot see this.
	live := &fv1.Function{}
	require.NoError(t, cl.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "hello"}, live))
	live.Generation = 2
	live.Spec.State = &fv1.StateConfig{Sticky: &fv1.StickyConfig{Source: fv1.StickySourceHeader, Name: "X-Session-Id"}}
	require.NoError(t, cl.Update(t.Context(), live))

	// The function event cascades to the alias-routed trigger via fnIndex
	// (BackendKeys are stripped back to the live function's name) and must
	// swap its handler — pre-fix the re-apply answered NoChange because no
	// identity field carried the live function's Generation.
	_, err = ts.applyFunctionIncremental(t.Context(), live)
	require.NoError(t, err)
	requireNoSignal(t, ts)

	snap = ts.routeTable.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, int64(2), snap[0].StickyGen,
		"the cascade must re-resolve the trigger against the edited live function (pre-fix: identity unchanged, NoChange)")
	assert.Same(t, handler, snap[0].Handler, "still an atomic swap on the same ref")

	// The swapped handler honors the sticky key: every request carrying the
	// same key now lands on ONE deterministic backend.
	drive(100)
	calls = resolver.snapshotAndReset()
	require.Equal(t, 100, calls["hello-v1"]+calls["hello-v2"], "every request resolved to one of the two targets")
	assert.True(t, calls["hello-v1"] == 100 || calls["hello-v2"] == 100,
		"a keyed request stream must stick to one backend after the sticky edit, got %v", calls)

	// Drift symmetry: the settled sticky edit must not register as drift.
	drift, err := ts.resync(t.Context(), false)
	require.NoError(t, err)
	assert.Zero(t, drift, "a settled sticky edit must not register as resync drift")
	requireNoSignal(t, ts)
}

// TestResyncSparesAliasRouteCreatedAfterList pins the C7 fix: resync's
// stale-route cleanup for suffixed (alias/version) internal routes must
// re-check the live object before deleting — exactly like the plain-function
// branch does — because a FunctionAlias created AFTER the resync captured its
// LIST snapshot (but applied by its reconciler before the cleanup walk) is
// absent from the snapshot while its route is legitimately live. Pre-fix the
// cleanup tore the route down unconditionally (and signalled a materialize),
// self-healing only at the alias's next event or the next resync — a
// drift-metric increment violating the zero-drift bar. A genuinely deleted
// alias (Get NotFound too) must still be cleaned up.
func TestResyncSparesAliasRouteCreatedAfterList(t *testing.T) {
	fn := incrFn("hello", "default", 1)
	v1 := incrVersion("hello-v1", "default", "hello", fn.UID, 1, 1)
	alias := incrAlias("prod", "default", "hello", "hello-v1", 1)

	// The interceptor hides the alias from every LIST while leaving Get
	// untouched — the observable state of an alias created after the LIST
	// snapshot was taken.
	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(fn, v1, alias).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if err := c.List(ctx, list, opts...); err != nil {
					return err
				}
				if al, ok := list.(*fv1.FunctionAliasList); ok {
					kept := al.Items[:0]
					for _, item := range al.Items {
						if item.Name != "prod" {
							kept = append(kept, item)
						}
					}
					al.Items = kept
				}
				return nil
			},
		}).Build()
	ts := newIncrementalTSWithClient(t, cl)

	// Baseline: function + version converged; the hidden alias is not
	// materialized by the list-driven pass.
	_, err := ts.resync(t.Context(), true)
	require.NoError(t, err)
	require.False(t, hasInternalRoute(ts, "default", "hello", "prod"),
		"precondition: the LIST-hidden alias is not materialized by resync")

	// The alias reconciler applies the just-created alias (the event path
	// does not LIST, so the interceptor does not affect it).
	_, err = ts.applyAliasIncremental(t.Context(), alias)
	require.NoError(t, err)
	require.True(t, hasInternalRoute(ts, "default", "hello", "prod"),
		"precondition: the alias reconciler materialized the route")
	drainSignals(ts)

	// A resync whose LIST snapshot predates the alias: the live-object
	// re-check must spare the route. Pre-fix: deleted, drift++, materialize
	// signalled.
	drift, err := ts.resync(t.Context(), false)
	require.NoError(t, err)
	assert.True(t, hasInternalRoute(ts, "default", "hello", "prod"),
		"resync must not tear down a route whose alias is live but missing from the LIST snapshot")
	assert.Zero(t, drift, "sparing a live route is not a correction")
	requireNoSignal(t, ts)

	// Genuinely deleted (Get returns NotFound too): the cleanup must proceed.
	require.NoError(t, cl.Delete(t.Context(), alias))
	drift, err = ts.resync(t.Context(), false)
	require.NoError(t, err)
	assert.False(t, hasInternalRoute(ts, "default", "hello", "prod"),
		"a genuinely deleted alias's route must still be cleaned up")
	assert.Equal(t, 1, drift, "the missed delete is exactly one correction")
	requireSignal(t, ts)
}

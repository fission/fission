// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
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
	assert.EqualValues(t, 1, before.FunctionGen, "route initially serves the v1 target's generation")

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
	assert.EqualValues(t, 2, after.FunctionGen, "the SAME ref now serves the v2 target's generation")

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
	assert.Equal(t, []string{"prod"}, snap[0].Aliases, "the route is indexed under the alias it consumed")

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

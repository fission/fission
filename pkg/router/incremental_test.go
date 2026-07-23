// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bep/debounce"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	config "github.com/fission/fission/pkg/featureconfig"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/router/routetable"
	"github.com/fission/fission/pkg/utils/httpmux"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// newIncrementalTS builds an HTTPTriggerSet driving the incremental route path,
// backed by a fake cache client holding objs, with both mutable routers wired
// so materialize() has swap targets.
func newIncrementalTS(t testing.TB, objs ...client.Object) (*HTTPTriggerSet, client.Client) {
	t.Helper()
	logger := loggerfactory.GetLogger()
	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
	ts := &HTTPTriggerSet{
		logger:                     logger.WithName("incremental_test"),
		client:                     cl,
		updateRouterRequestChannel: make(chan struct{}, 10),
		syncDebouncer:              debounce.New(time.Millisecond),
		resolver:                   makeFunctionReferenceResolver(logger, cl),
	}
	ts.initIncrementalRoutes()
	ts.mutableRouter = newMutableRouter(logger, httpmux.New().Handler())
	ts.internalMutableRouter = newMutableRouter(logger, httpmux.New().Handler())
	return ts, cl
}

// muxes rebuilds the listener muxes from the current route-table snapshot — the
// same registration materialize() last swapped in — so tests can introspect
// registered routes via httpmux.Match without driving the proxy handlers. Every
// caller materializes first, so the rebuilt muxes match what is being served.
func muxes(ts *HTTPTriggerSet) (public, internal *httpmux.Mux) {
	fc, err := ts.featureConfigFn(ts.logger)
	if err != nil {
		panic(fmt.Errorf("muxes: feature config: %w", err))
	}
	return ts.buildIncrementalMuxes(fc, ts.routeTable.Materialization())
}

// requireSignal / requireNoSignal assert on the debounced materializer
// channel. The debouncer fires after 1ms in tests; 250ms is comfortably past
// it without slowing the suite on the negative path more than necessary.
func requireSignal(t *testing.T, ts *HTTPTriggerSet) {
	t.Helper()
	select {
	case <-ts.updateRouterRequestChannel:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected a materialize signal, got none")
	}
	// A batch of shape changes may emit more than one send: the debouncer
	// coalesces calls within its window, but two changes spaced just over the
	// (1ms test) window — easy under -race load — each fire onto the buffered
	// channel. The test cares only that a materialize was signalled, not how
	// many times, so drain the stragglers; otherwise a later requireNoSignal is
	// tripped by a leftover from this batch.
	drainSignals(ts)
}

// drainSignals empties the materializer channel, settling briefly so a
// debounced send still in flight (≤ the 1ms test window) is also consumed.
func drainSignals(ts *HTTPTriggerSet) {
	for {
		select {
		case <-ts.updateRouterRequestChannel:
		case <-time.After(10 * time.Millisecond):
			return
		}
	}
}

func requireNoSignal(t *testing.T, ts *HTTPTriggerSet) {
	t.Helper()
	select {
	case <-ts.updateRouterRequestChannel:
		t.Fatal("got a materialize signal for a handler-only change")
	case <-time.After(250 * time.Millisecond):
	}
}

func incrFn(name, ns string, gen int64) *fv1.Function {
	return &fv1.Function{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: ns, Generation: gen, UID: types.UID("fn-" + name),
	}}
}

func incrTrigger(name, ns string, gen int64, url, fnName string) *fv1.HTTPTrigger {
	return &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: gen, UID: types.UID("trig-" + name)},
		Spec: fv1.HTTPTriggerSpec{
			RelativeURL: url,
			Methods:     []string{http.MethodGet},
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName, Name: fnName,
			},
		},
	}
}

// TestIncrementalTriggerLifecycle drives insert → materialize → delete →
// materialize through the real apply path and asserts what the listeners
// serve at each step.
func TestIncrementalTriggerLifecycle(t *testing.T) {
	fn := incrFn("fn", "default", 1)
	trigger := incrTrigger("t1", "default", 1, "/hello", "fn")
	ts, _ := newIncrementalTS(t, fn, trigger)

	res, err := ts.applyTriggerIncremental(t.Context(), trigger)
	require.NoError(t, err)
	assert.Equal(t, routetable.ShapeChanged, res)
	requireSignal(t, ts)
	ts.materialize(t.Context())

	public, _ := muxes(ts)
	assert.True(t, muxMatches(public, http.MethodGet, "/hello"), "route must serve after materialize")
	assert.True(t, ts.ready.Load(), "first materialize must flip readiness")

	// Identical re-apply: no change, no signal.
	res, err = ts.applyTriggerIncremental(t.Context(), trigger)
	require.NoError(t, err)
	assert.Equal(t, routetable.NoChange, res)
	requireNoSignal(t, ts)

	// Delete: the route must drop out after the next materialize.
	res2 := ts.deleteTriggerIncremental(types.NamespacedName{Namespace: "default", Name: "t1"})
	assert.Equal(t, routetable.ShapeChanged, res2)
	requireSignal(t, ts)
	ts.materialize(t.Context())
	public, _ = muxes(ts)
	assert.False(t, muxMatches(public, http.MethodGet, "/hello"), "deleted trigger must not serve")
}

// TestIncrementalCanaryTickIsHandlerSwapOnly is the RFC's headline
// acceptance: a canary FunctionWeights rewrite (or any same-shape trigger
// update) must be a handler swap with ZERO materializer signals — at any
// trigger count, a weight tick no longer rebuilds anything.
func TestIncrementalCanaryTickIsHandlerSwapOnly(t *testing.T) {
	fnA := incrFn("fn-a", "default", 1)
	fnB := incrFn("fn-b", "default", 1)
	canary := &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "canary", Namespace: "default", Generation: 1, UID: "trig-canary"},
		Spec: fv1.HTTPTriggerSpec{
			RelativeURL: "/canary",
			Methods:     []string{http.MethodGet},
			FunctionReference: fv1.FunctionReference{
				Type:            fv1.FunctionReferenceTypeFunctionWeights,
				FunctionWeights: map[string]int{"fn-a": 90, "fn-b": 10},
			},
		},
	}
	ts, _ := newIncrementalTS(t, fnA, fnB, canary)

	res, err := ts.applyTriggerIncremental(t.Context(), canary)
	require.NoError(t, err)
	require.Equal(t, routetable.ShapeChanged, res, "first apply inserts")
	requireSignal(t, ts)
	ts.materialize(t.Context())

	// 50 weight ticks: every one must be a pure handler swap.
	for i := range 50 {
		tick := canary.DeepCopy()
		tick.Generation = int64(i + 2)
		tick.Spec.FunctionReference.FunctionWeights = map[string]int{"fn-a": 90 - i, "fn-b": 10 + i}
		res, err := ts.applyTriggerIncremental(t.Context(), tick)
		require.NoError(t, err)
		require.Equal(t, routetable.HandlerSwapped, res, "weight tick %d must be a handler swap", i)
	}
	requireNoSignal(t, ts)

	public, _ := muxes(ts)
	assert.True(t, muxMatches(public, http.MethodGet, "/canary"), "the route keeps serving through the ticks")
}

// TestIncrementalFunctionEventCascade pins the fn-index path: a function
// update swaps the internal route's handler AND re-applies the referencing
// trigger as a handler swap; a function create/delete is an internal shape
// change.
func TestIncrementalFunctionEventCascade(t *testing.T) {
	fn := incrFn("fn", "myns", 1)
	trigger := incrTrigger("t1", "myns", 1, "/hello", "fn")
	ts, cl := newIncrementalTS(t, fn, trigger)

	// Seed: function insert (internal shape change) + trigger insert.
	res, err := ts.applyFunctionIncremental(t.Context(), fn)
	require.NoError(t, err)
	assert.Equal(t, routetable.ShapeChanged, res, "first sighting adds the internal route")
	_, err = ts.applyTriggerIncremental(t.Context(), trigger)
	require.NoError(t, err)
	requireSignal(t, ts)
	ts.materialize(t.Context())
	_, internal := muxes(ts)
	require.True(t, muxMatches(internal, http.MethodPost, "/fission-function/myns/fn"))

	// Function update: internal handler swap + trigger handler swap, no signal.
	fn2 := &fv1.Function{}
	require.NoError(t, cl.Get(t.Context(), types.NamespacedName{Namespace: "myns", Name: "fn"}, fn2))
	fn2.Generation = 2 // a spec change bumps the generation — the handler-swap key
	require.NoError(t, cl.Update(t.Context(), fn2))
	res, err = ts.applyFunctionIncremental(t.Context(), fn2)
	require.NoError(t, err)
	assert.Equal(t, routetable.HandlerSwapped, res)
	requireNoSignal(t, ts)

	// Function delete: internal route drops; the trigger's resolve now fails
	// NotFound so its route drops too.
	require.NoError(t, cl.Delete(t.Context(), fn2))
	require.NoError(t, ts.deleteFunctionIncremental(t.Context(), types.NamespacedName{Namespace: "myns", Name: "fn"}))
	requireSignal(t, ts)
	ts.materialize(t.Context())
	public, internal := muxes(ts)
	assert.False(t, muxMatches(internal, http.MethodPost, "/fission-function/myns/fn"),
		"deleted function's internal route must drop")
	assert.False(t, muxMatches(public, http.MethodGet, "/hello"),
		"trigger resolving through the deleted function must stop serving")
}

// TestIncrementalConditionsViaReconciler drives the REAL reconcilers (the
// production entry points) against an incremental trigger set and asserts
// the conditions: admitted triggers get RouteAdmitted=True after
// materialize; a trigger whose function is missing gets
// RouteAdmitted=False/FunctionNotFound.
func TestIncrementalConditionsViaReconciler(t *testing.T) {
	fn := incrFn("fn", "default", 1)
	good := incrTrigger("good", "default", 1, "/good", "fn")
	orphan := incrTrigger("orphan", "default", 1, "/orphan", "ghost")
	ts, cl := newIncrementalTS(t, fn, good, orphan)
	fc := fissionfake.NewSimpleClientset(good, orphan)
	ts.fissionClient = fc

	r := &httpTriggerReconciler{logger: ts.logger, client: cl, ts: ts}
	for _, name := range []string{"good", "orphan"} {
		_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: name}})
		require.NoError(t, err)
	}
	requireSignal(t, ts)
	ts.materialize(t.Context())

	public, _ := muxes(ts)
	assert.True(t, muxMatches(public, http.MethodGet, "/good"))
	assert.False(t, muxMatches(public, http.MethodGet, "/orphan"), "unresolvable trigger must not serve")

	gotGood, err := fc.CoreV1().HTTPTriggers("default").Get(t.Context(), "good", metav1.GetOptions{})
	require.NoError(t, err)
	requireCondition(t, gotGood, fv1.HTTPTriggerConditionRouteAdmitted, metav1.ConditionTrue, fv1.HTTPTriggerReasonRouteAdmitted)

	gotOrphan, err := fc.CoreV1().HTTPTriggers("default").Get(t.Context(), "orphan", metav1.GetOptions{})
	require.NoError(t, err)
	requireCondition(t, gotOrphan, fv1.HTTPTriggerConditionRouteAdmitted, metav1.ConditionFalse, fv1.HTTPTriggerReasonFunctionNotFound)
}

func requireCondition(t *testing.T, trigger *fv1.HTTPTrigger, condType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	for _, c := range trigger.Status.Conditions {
		if c.Type == condType {
			assert.Equal(t, status, c.Status, "condition %s status", condType)
			assert.Equal(t, reason, c.Reason, "condition %s reason", condType)
			return
		}
	}
	t.Fatalf("condition %s not found on trigger %s (have %+v)", condType, trigger.Name, trigger.Status.Conditions)
}

// TestIncrementalResyncHealsDrift simulates missed watch events: objects
// created and deleted behind the reconcilers' back are corrected by the
// periodic resync.
func TestIncrementalResyncHealsDrift(t *testing.T) {
	fn := incrFn("fn", "default", 1)
	t1 := incrTrigger("t1", "default", 1, "/one", "fn")
	ts, cl := newIncrementalTS(t, fn, t1)

	// Initial resync populates (not drift).
	_, err := ts.resync(t.Context(), true)
	require.NoError(t, err)
	pub, internal := ts.routeTable.Sizes()
	assert.Equal(t, 1, pub)
	assert.Equal(t, 1, internal)

	// A second pass is fully converged: every apply must be NoChange (this
	// is exactly what the drift counter counts after startup).
	_, err = ts.resync(t.Context(), false)
	require.NoError(t, err)
	pub, internal = ts.routeTable.Sizes()
	assert.Equal(t, 1, pub)
	assert.Equal(t, 1, internal)

	// Missed events: t1 deleted, t2 created, with no reconciler in sight.
	require.NoError(t, cl.Delete(t.Context(), t1))
	t2 := incrTrigger("t2", "default", 1, "/two", "fn")
	t2.ResourceVersion = "" // the fake client rejects RVs on Create and assigns its own
	require.NoError(t, cl.Create(t.Context(), t2))

	_, err = ts.resync(t.Context(), false)
	require.NoError(t, err)
	ts.materialize(t.Context())
	public, _ := muxes(ts)
	assert.False(t, muxMatches(public, http.MethodGet, "/one"), "resync must drop the missed delete")
	assert.True(t, muxMatches(public, http.MethodGet, "/two"), "resync must add the missed create")
}

// TestBuildMuxesIncrementalParity builds the same world through BOTH builders —
// the one-shot buildMuxes and the incremental table + materialize — and
// asserts a corpus of requests is dispatched identically on both listeners.
// This is the contract that guarantees the one-shot builder (used by the
// shape/security tests and the test-scaffolding path) and the production
// incremental materializer register identical routes.
func TestBuildMuxesIncrementalParity(t *testing.T) {
	prefix := "/api"
	slashPrefix := "/files/"
	functions := []fv1.Function{
		*incrFn("fn", "default", 1),
		*incrFn("other", "myns", 1),
	}
	triggers := []fv1.HTTPTrigger{
		*incrTrigger("exact", "default", 1, "/hello", "fn"),
		*incrTrigger("home", "default", 1, "/", "fn"),
		{
			ObjectMeta: metav1.ObjectMeta{Name: "dual", Namespace: "default", Generation: 1, UID: "trig-dual"},
			Spec: fv1.HTTPTriggerSpec{
				Prefix:            &prefix,
				Methods:           []string{http.MethodGet, http.MethodPost},
				FunctionReference: fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slashpfx", Namespace: "default", Generation: 1, UID: "trig-slashpfx"},
			Spec: fv1.HTTPTriggerSpec{
				Prefix:            &slashPrefix,
				Methods:           []string{http.MethodGet},
				FunctionReference: fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "hosted", Namespace: "default", Generation: 1, UID: "trig-hosted"},
			Spec: fv1.HTTPTriggerSpec{
				RelativeURL:       "/hosted",
				Host:              "api.example.com",
				Methods:           []string{http.MethodGet},
				FunctionReference: fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "cors", Namespace: "default", Generation: 1, UID: "trig-cors"},
			Spec: fv1.HTTPTriggerSpec{
				RelativeURL: "/cors",
				Methods:     []string{http.MethodGet},
				CorsConfig:  &fv1.HTTPTriggerCorsConfig{AllowOrigins: []string{"https://app.example.com"}},
				FunctionReference: fv1.FunctionReference{
					Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn",
				},
			},
		},
		*incrTrigger("orphan", "default", 1, "/orphan", "ghost"), // skipped by both paths
		*incrTrigger("regex", "default", 1, `/bank/{html:[a-zA-Z0-9\.\/]+}`, "fn"),
		*incrTrigger("var", "default", 1, "/sessions/{id}", "fn"),
	}

	// One-shot builder (buildMuxes).
	oneShotTS := newShapeTS(t, functions, triggers)
	oneShotPublic, oneShotInternal, err := oneShotTS.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	// Incremental path: applies + materialize.
	objs := make([]client.Object, 0, len(functions)+len(triggers))
	for i := range functions {
		objs = append(objs, &functions[i])
	}
	for i := range triggers {
		objs = append(objs, &triggers[i])
	}
	incrTS, _ := newIncrementalTS(t, objs...)
	_, err = incrTS.resync(t.Context(), true)
	require.NoError(t, err)
	incrTS.materialize(t.Context())
	incrPublic, incrInternal := muxes(incrTS)

	type probe struct {
		method, path, host string
	}
	corpus := []probe{
		{http.MethodGet, "/hello", ""}, {http.MethodPost, "/hello", ""}, {http.MethodGet, "/hello/x", ""},
		{http.MethodGet, "/", ""}, {http.MethodOptions, "/", ""},
		{http.MethodGet, "/api", ""}, {http.MethodPost, "/api", ""}, {http.MethodGet, "/api/", ""},
		{http.MethodGet, "/api/v1", ""}, {http.MethodGet, "/apifoo", ""}, {http.MethodDelete, "/api", ""},
		{http.MethodGet, "/files/", ""}, {http.MethodGet, "/files/a/b", ""}, {http.MethodGet, "/files", ""},
		{http.MethodGet, "/hosted", "api.example.com"}, {http.MethodGet, "/hosted", "other.example.com"}, {http.MethodGet, "/hosted", ""},
		{http.MethodGet, "/cors", ""}, {http.MethodOptions, "/cors", ""},
		{http.MethodGet, "/orphan", ""},
		{http.MethodGet, "/bank/index.html", ""}, {http.MethodGet, "/bank/css/app.css", ""},
		{http.MethodGet, "/bank/oops!", ""}, {http.MethodPost, "/bank/index.html", ""},
		{http.MethodGet, "/sessions/abc", ""}, {http.MethodGet, "/sessions/abc/x", ""},
		{http.MethodGet, "/router-healthz", ""}, {http.MethodGet, "/readyz", ""}, {http.MethodGet, "/_version", ""},
		{http.MethodGet, "/no-such-route", ""},
	}
	matchOn := func(m *httpmux.Mux, p probe) bool {
		req := httptest.NewRequest(p.method, p.path, nil)
		if p.host != "" {
			req.Host = p.host
		}
		_, ok := m.Match(req)
		return ok
	}
	for _, p := range corpus {
		assert.Equal(t, matchOn(oneShotPublic, p), matchOn(incrPublic, p),
			"public dispatch parity for %s %s host=%q", p.method, p.path, p.host)
	}

	internalCorpus := []probe{
		{http.MethodPost, "/fission-function/fn", ""},
		{http.MethodPost, "/fission-function/fn/sub", ""},
		{http.MethodPost, "/fission-function/default/fn", ""},
		{http.MethodPost, "/fission-function/myns/other", ""},
		{http.MethodPost, "/fission-function/myns/other/sub", ""},
		{http.MethodPost, "/fission-function/myns/otherX", ""},
		{http.MethodGet, "/router-healthz", ""},
	}
	for _, p := range internalCorpus {
		assert.Equal(t, matchOn(oneShotInternal, p), matchOn(incrInternal, p),
			"internal dispatch parity for %s %s", p.method, p.path)
	}
}

// TestIncrementalNoRouteFlap extends the mutablemux swap guarantee to the
// full incremental machinery: sustained requests against one stable route
// while canary handler swaps and unrelated shape changes (trigger adds) +
// materializes churn concurrently. Zero non-200s allowed. Run with -race.
func TestIncrementalNoRouteFlap(t *testing.T) {
	ts, _ := newIncrementalTS(t)

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	stable := &routetable.RouteSpec{
		TriggerUID: "stable", Namespace: "default", Name: "stable",
		TriggerGen: 1, ExactPath: "/stable", Methods: []string{http.MethodGet},
	}
	ts.routeTable.ApplyTrigger(stable, func() http.Handler { return ok })
	ts.materialize(t.Context())

	stop := make(chan struct{})
	churnDone := make(chan struct{})
	go func() {
		defer close(churnDone)
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			i++
			// Handler swap on the stable route (same shape, new RV).
			swap := *stable
			swap.TriggerGen = int64(i + 1)
			ts.routeTable.ApplyTrigger(&swap, func() http.Handler { return ok })
			// Unrelated shape change + immediate materialize (worst case:
			// no debounce coalescing at all).
			noise := &routetable.RouteSpec{
				TriggerUID: types.UID(fmt.Sprintf("noise-%d", i%17)),
				Namespace:  "default", Name: fmt.Sprintf("noise-%d", i%17),
				TriggerGen: int64(i),
				ExactPath:  fmt.Sprintf("/noise-%d", i%17), Methods: []string{http.MethodGet},
			}
			ts.routeTable.ApplyTrigger(noise, func() http.Handler { return ok })
			ts.materialize(t.Context())
		}
	}()

	for range 3000 {
		rr := httptest.NewRecorder()
		ts.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/stable", nil))
		if rr.Code != http.StatusOK {
			close(stop)
			<-churnDone
			t.Fatalf("stable route flapped: got %d", rr.Code)
		}
	}
	close(stop)
	<-churnDone
}

// TestIncrementalPrecedenceDispatch pins the phase-2 dispatch rules through
// the real materializer: exact beats prefix, longest prefix wins, host-
// qualified beats host-less. Handlers are distinguishable stubs applied
// straight to the table (the resolution machinery is exercised elsewhere).
func TestIncrementalPrecedenceDispatch(t *testing.T) {
	ts, _ := newIncrementalTS(t)
	tag := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(name))
		})
	}
	add := func(name, exact, prefix, host string, created time.Time) {
		spec := &routetable.RouteSpec{
			TriggerUID: types.UID(name), Namespace: "default", Name: name,
			TriggerGen: 1, ExactPath: exact, PrefixPath: prefix, Host: host,
			Methods: []string{http.MethodGet},
			Created: metav1.NewTime(created),
		}
		ts.routeTable.ApplyTrigger(spec, func() http.Handler { return tag(name) })
	}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	add("wide", "", "/api/", "", t0)
	add("narrow", "", "/api/v1/", "", t0)
	add("exact", "/api/v1/users", "", "", t0)
	add("hosted", "", "/api/", "api.example.com", t0)
	ts.materialize(t.Context())

	dispatch := func(path, host string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if host != "" {
			req.Host = host
		}
		rr := httptest.NewRecorder()
		ts.ServeHTTP(rr, req)
		return rr.Body.String()
	}
	assert.Equal(t, "exact", dispatch("/api/v1/users", ""), "exact beats every prefix")
	assert.Equal(t, "narrow", dispatch("/api/v1/other", ""), "longest prefix wins")
	assert.Equal(t, "wide", dispatch("/api/v2/x", ""), "shorter prefix serves the rest of the subtree")
	assert.Equal(t, "hosted", dispatch("/api/v1/users", "api.example.com"),
		"host-qualified routes outrank host-less ones, even exact ones")
}

// TestIncrementalConflictConditions drives the full conflict lifecycle
// through the real apply path: the younger duplicate is shadowed and marked
// RouteAdmitted=False/RouteConflict naming the winner; deleting the winner
// flips the loser back to True and its route starts serving.
func TestIncrementalConflictConditions(t *testing.T) {
	fn := incrFn("fn", "default", 1)
	older := incrTrigger("older", "default", 1, "/dup", "fn")
	older.CreationTimestamp = metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	younger := incrTrigger("younger", "default", 1, "/dup", "fn")
	younger.CreationTimestamp = metav1.NewTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))

	ts, cl := newIncrementalTS(t, fn, older, younger)
	fc := fissionfake.NewSimpleClientset(older, younger)
	ts.fissionClient = fc

	for _, trigger := range []*fv1.HTTPTrigger{older, younger} {
		_, err := ts.applyTriggerIncremental(t.Context(), trigger)
		require.NoError(t, err)
	}
	ts.materialize(t.Context())

	gotYounger, err := fc.CoreV1().HTTPTriggers("default").Get(t.Context(), "younger", metav1.GetOptions{})
	require.NoError(t, err)
	requireCondition(t, gotYounger, fv1.HTTPTriggerConditionRouteAdmitted, metav1.ConditionFalse, fv1.HTTPTriggerReasonRouteConflict)
	gotOlder, err := fc.CoreV1().HTTPTriggers("default").Get(t.Context(), "older", metav1.GetOptions{})
	require.NoError(t, err)
	requireCondition(t, gotOlder, fv1.HTTPTriggerConditionRouteAdmitted, metav1.ConditionTrue, fv1.HTTPTriggerReasonRouteAdmitted)

	// Delete the winner: the loser must start serving and flip to True.
	require.NoError(t, cl.Delete(t.Context(), older))
	ts.deleteTriggerIncremental(types.NamespacedName{Namespace: "default", Name: "older"})
	ts.materialize(t.Context())

	public, _ := muxes(ts)
	assert.True(t, muxMatches(public, http.MethodGet, "/dup"), "the shadowed route serves once the winner is gone")
	gotYounger, err = fc.CoreV1().HTTPTriggers("default").Get(t.Context(), "younger", metav1.GetOptions{})
	require.NoError(t, err)
	requireCondition(t, gotYounger, fv1.HTTPTriggerConditionRouteAdmitted, metav1.ConditionTrue, fv1.HTTPTriggerReasonRouteAdmitted)
}

// TestIncrementalConflictLoserSurvivesHandlerSwap is the review-finding
// regression test: a shadowed trigger must KEEP its RouteAdmitted=False/
// RouteConflict condition through handler swaps and resync passes — the
// NoChange/HandlerSwapped condition path must not flip a loser to True while
// the mux still dispatches its shape to the winner.
func TestIncrementalConflictLoserSurvivesHandlerSwap(t *testing.T) {
	fn := incrFn("fn", "default", 1)
	older := incrTrigger("older", "default", 1, "/dup", "fn")
	older.CreationTimestamp = metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	younger := incrTrigger("younger", "default", 1, "/dup", "fn")
	younger.CreationTimestamp = metav1.NewTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))

	ts, _ := newIncrementalTS(t, fn, older, younger)
	fc := fissionfake.NewSimpleClientset(older, younger)
	ts.fissionClient = fc

	for _, trigger := range []*fv1.HTTPTrigger{older, younger} {
		_, err := ts.applyTriggerIncremental(t.Context(), trigger)
		require.NoError(t, err)
	}
	ts.materialize(t.Context())

	assertLoserFalse := func(when string) {
		t.Helper()
		got, err := fc.CoreV1().HTTPTriggers("default").Get(t.Context(), "younger", metav1.GetOptions{})
		require.NoError(t, err, "reading loser condition %s", when)
		requireCondition(t, got, fv1.HTTPTriggerConditionRouteAdmitted, metav1.ConditionFalse, fv1.HTTPTriggerReasonRouteConflict)
	}
	assertLoserFalse("after materialize")

	// A handler swap on the loser (spec generation bump, same shape) must
	// not flip the condition.
	swap := younger.DeepCopy()
	swap.Generation = 2
	res, err := ts.applyTriggerIncremental(t.Context(), swap)
	require.NoError(t, err)
	require.Equal(t, routetable.HandlerSwapped, res)
	assertLoserFalse("after handler swap")

	// A resync pass (which re-applies every trigger as NoChange) must not
	// flip it either — this is the path that runs every 60s in production.
	_, err = ts.resync(t.Context(), false)
	require.NoError(t, err)
	assertLoserFalse("after resync")
}

// TestIncrementalFunctionCreatedAfterTrigger pins the unresolved-index
// cascade: a trigger applied before its function exists is re-admitted by
// the function's create event itself — NOT left waiting for the next resync.
func TestIncrementalFunctionCreatedAfterTrigger(t *testing.T) {
	trigger := incrTrigger("early", "default", 1, "/early", "late-fn")
	ts, cl := newIncrementalTS(t, trigger)
	fc := fissionfake.NewSimpleClientset(trigger)
	ts.fissionClient = fc

	res, err := ts.applyTriggerIncremental(t.Context(), trigger)
	require.NoError(t, err)
	assert.Equal(t, routetable.NoChange, res, "nothing to remove; the route never existed")
	got, err := fc.CoreV1().HTTPTriggers("default").Get(t.Context(), "early", metav1.GetOptions{})
	require.NoError(t, err)
	requireCondition(t, got, fv1.HTTPTriggerConditionRouteAdmitted, metav1.ConditionFalse, fv1.HTTPTriggerReasonFunctionNotFound)

	// The function arrives. Its create event must re-admit the trigger via
	// the unresolved index — no resync involved.
	fn := incrFn("late-fn", "default", 1)
	require.NoError(t, cl.Create(t.Context(), fn))
	_, err = ts.applyFunctionIncremental(t.Context(), fn)
	require.NoError(t, err)
	ts.materialize(t.Context())

	public, _ := muxes(ts)
	assert.True(t, muxMatches(public, http.MethodGet, "/early"),
		"the trigger must serve as soon as its function exists")
	got, err = fc.CoreV1().HTTPTriggers("default").Get(t.Context(), "early", metav1.GetOptions{})
	require.NoError(t, err)
	requireCondition(t, got, fv1.HTTPTriggerConditionRouteAdmitted, metav1.ConditionTrue, fv1.HTTPTriggerReasonRouteAdmitted)
}

// TestIncrementalMaterializeFailureRetries pins the sticky-failure contract:
// a failed materialize re-queues the batch's conditions and sets the dirty
// flag (the resync loop's re-arm signal); the retry then completes the
// deferred work — routes serve and conditions flip True.
func TestIncrementalMaterializeFailureRetries(t *testing.T) {
	fn := incrFn("fn", "default", 1)
	trigger := incrTrigger("t1", "default", 1, "/hello", "fn")
	ts, _ := newIncrementalTS(t, fn, trigger)
	fc := fissionfake.NewSimpleClientset(trigger)
	ts.fissionClient = fc

	failing := true
	realFn := ts.featureConfigFn
	ts.featureConfigFn = func(logger logr.Logger) (*config.FeatureConfig, error) {
		if failing {
			return nil, fmt.Errorf("injected feature config failure")
		}
		return realFn(logger)
	}

	_, err := ts.applyTriggerIncremental(t.Context(), trigger)
	require.NoError(t, err)
	ts.materialize(t.Context())

	assert.True(t, ts.materializeDirty.Load(), "a failed materialize must leave the dirty flag set")
	assert.False(t, ts.ready.Load(), "readiness must not report before a successful build")
	got, err := fc.CoreV1().HTTPTriggers("default").Get(t.Context(), "t1", metav1.GetOptions{})
	require.NoError(t, err)
	requireCondition(t, got, fv1.HTTPTriggerConditionRouteAdmitted, metav1.ConditionFalse, fv1.HTTPTriggerReasonMuxBuildFail)

	// Retry (in production the resync loop re-signals on the dirty flag).
	failing = false
	ts.materialize(t.Context())

	assert.False(t, ts.materializeDirty.Load(), "a successful build must clear the dirty flag")
	assert.True(t, ts.ready.Load())
	public, _ := muxes(ts)
	assert.True(t, muxMatches(public, http.MethodGet, "/hello"), "the deferred batch must now serve")
	got, err = fc.CoreV1().HTTPTriggers("default").Get(t.Context(), "t1", metav1.GetOptions{})
	require.NoError(t, err)
	requireCondition(t, got, fv1.HTTPTriggerConditionRouteAdmitted, metav1.ConditionTrue, fv1.HTTPTriggerReasonRouteAdmitted)
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/router/routetable"
	"github.com/fission/fission/pkg/utils/httpmux"
)

// This file is the incremental route-update path (RFC-0013 phase 1), the only
// production route-update path. The reconcilers feed per-event diffs into the
// route table; handler-only changes (canary weight ticks, function updates)
// swap an atomic pointer and never rebuild a mux; shape changes signal the
// debounced materializer, which rebuilds the httpmux muxes from a table
// snapshot through the registration helpers in routeshape.go.
//
// buildMuxes (httpTriggers.go) is the one-shot mux constructor those same
// registration helpers also back; it is the test/parity builder, not a
// production route-update path.

// resyncInterval is how often the drift guard re-lists triggers + functions
// and diffs them against the route table. Both lists come from the Manager's
// in-memory cache, so a pass is cheap; anything it corrects after startup is
// a missed watch event and increments fission_router_route_resync_drift_total.
const resyncInterval = 60 * time.Second

// referencedFunctions lists the function keys a trigger's reference resolves
// through (the canary form references several).
func referencedFunctions(trigger *fv1.HTTPTrigger) []types.NamespacedName {
	ref := trigger.Spec.FunctionReference
	switch ref.Type {
	case fv1.FunctionReferenceTypeFunctionWeights:
		out := make([]types.NamespacedName, 0, len(ref.FunctionWeights))
		for name := range ref.FunctionWeights {
			out = append(out, types.NamespacedName{Namespace: trigger.Namespace, Name: name})
		}
		return out
	default:
		return []types.NamespacedName{{Namespace: trigger.Namespace, Name: ref.Name}}
	}
}

// referencedAliases lists the FunctionAlias keys a trigger's reference
// resolves through (RFC-0025): non-empty only for a plain-name reference that
// also carries Alias — a version pin and FunctionWeights never reference an
// alias. Mirrors referencedFunctions so a resolve failure can mark BOTH the
// function and alias unresolved edges (MarkUnresolved), keeping the
// alias-create cascade working the same way the function-create cascade
// already does.
func referencedAliases(trigger *fv1.HTTPTrigger) []types.NamespacedName {
	ref := trigger.Spec.FunctionReference
	if ref.Type == fv1.FunctionReferenceTypeFunctionName && ref.Alias != "" {
		return []types.NamespacedName{{Namespace: trigger.Namespace, Name: ref.Alias}}
	}
	return nil
}

// applyTriggerIncremental reconciles one trigger into the route table:
// validate → resolve → apply. Returns the apply result so the resync loop
// can count drift, and an error only for transient resolve failures (the
// reconciler requeues; the last-known-good route keeps serving).
func (ts *HTTPTriggerSet) applyTriggerIncremental(ctx context.Context, trigger *fv1.HTTPTrigger) (routetable.ApplyResult, error) {
	key := types.NamespacedName{Namespace: trigger.Namespace, Name: trigger.Name}

	// Invalid CORS/ingress config: the route must not serve (the one-shot
	// buildMuxes skips it too), and the user sees why on the trigger's conditions.
	// Delete-by-name so a previously-unresolved trigger's index entry is
	// cleared too.
	if reason, cfgErr := triggerConfigError(trigger); cfgErr != nil {
		res := ts.routeTable.DeleteTriggerByName(key)
		routeTableApplies.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "rejected")))
		if res == routetable.ShapeChanged {
			ts.signalMaterialize()
		}
		ts.updateRoutesGauge()
		ts.markTriggerCondition(ctx, trigger, metav1.ConditionFalse, reason,
			"router rejected the trigger configuration: "+cfgErr.Error(),
			"trigger is not serving due to invalid configuration")
		return res, nil
	}

	rr, err := ts.resolver.resolve(ctx, *trigger)
	if err != nil {
		if errors.Is(err, errFunctionNotFound) {
			// The referenced function does not exist: drop the route (it
			// would 404 anyway) and say so on the trigger. The unresolved
			// index keeps the trigger→function edge alive, so the function's
			// create event re-admits the route immediately via the cascade.
			res := ts.routeTable.DeleteTriggerByName(key)
			ts.routeTable.MarkUnresolved(key, referencedFunctions(trigger), referencedAliases(trigger))
			routeTableApplies.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "rejected")))
			if res == routetable.ShapeChanged {
				ts.signalMaterialize()
			}
			ts.updateRoutesGauge()
			ts.markTriggerCondition(ctx, trigger, metav1.ConditionFalse, fv1.HTTPTriggerReasonFunctionNotFound,
				"router cannot resolve the trigger's function reference: "+err.Error(),
				"trigger is not serving because its function does not exist")
			return res, nil
		}
		// Transient reader error: requeue with backoff and keep serving the
		// last-known-good route.
		return routetable.NoChange, err
	}
	if rr.resolveResultType != resolveResultSingleFunction && rr.resolveResultType != resolveResultMultipleFunctions {
		ts.logger.Error(nil, "resolve result type not implemented", "type", rr.resolveResultType)
		res := ts.routeTable.DeleteTriggerByName(key)
		routeTableApplies.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "rejected")))
		if res == routetable.ShapeChanged {
			ts.signalMaterialize()
		}
		ts.updateRoutesGauge()
		ts.markTriggerCondition(ctx, trigger, metav1.ConditionFalse, fv1.HTTPTriggerReasonMuxBuildFail,
			fmt.Sprintf("resolve result type not implemented: %v", rr.resolveResultType),
			"trigger is not serving due to an unsupported function reference")
		return res, nil
	}

	shape := deriveRouteShape(trigger)
	fnGens := make(map[string]int64, len(rr.functionMap))
	fnTimeout := make(map[crd.CacheKeyUG]int, len(rr.functionMap))
	for name, fn := range rr.functionMap {
		fnGens[name] = fn.Generation
		fnTimeout[crd.CacheKeyUGFromMeta(&fn.ObjectMeta)] = fn.Spec.FunctionTimeout
	}
	spec := &routetable.RouteSpec{
		TriggerUID: trigger.UID,
		Namespace:  trigger.Namespace,
		Name:       trigger.Name,
		TriggerGen: trigger.Generation,
		FnGens:     fnGens,
		Aliases:    rr.Aliases,
		ExactPath:  shape.exactPath,
		PrefixPath: shape.prefixPath,
		Host:       shape.host,
		Methods:    shape.methods,
		Created:    trigger.CreationTimestamp,
	}
	res := ts.routeTable.ApplyTrigger(spec, func() http.Handler {
		return ts.buildTriggerHandler(trigger, rr, fnTimeout)
	})
	routeTableApplies.Add(ctx, 1, metric.WithAttributes(attribute.String("result", res.String())))

	switch res {
	case routetable.ShapeChanged:
		// The route becomes observable only after the debounced materialize;
		// queue the condition so it is marked after the swap (the mux swap
		// is where the route becomes live).
		ts.queuePendingCondition(trigger)
		ts.signalMaterialize()
	default:
		// NoChange / HandlerSwapped: the route is already live (the swap is
		// immediate). Mark right away — UNLESS the trigger is currently
		// shadowed by a route conflict: its RouteConflict condition is owned
		// by reportConflicts, and flipping it to True here would assert the
		// opposite of observable routing (the resync re-applies every
		// trigger, so without this guard a shadowed loser would self-mark
		// serving within a minute of being reported).
		if ts.isConflictLoser(key) {
			break
		}
		ts.markTriggerCondition(ctx, trigger, metav1.ConditionTrue, fv1.HTTPTriggerReasonRouteAdmitted,
			"router accepted the trigger and installed its mux entry",
			"trigger is serving")
	}
	ts.updateRoutesGauge()
	return res, nil
}

// deleteTriggerIncremental handles a trigger deletion event (by name — the
// object, and with it the UID, is gone).
func (ts *HTTPTriggerSet) deleteTriggerIncremental(key types.NamespacedName) routetable.ApplyResult {
	res := ts.routeTable.DeleteTriggerByName(key)
	if res == routetable.ShapeChanged {
		ts.signalMaterialize()
		ts.updateRoutesGauge()
	}
	return res
}

// applyFunctionIncremental reconciles a function event: upsert its internal
// route (insert = shape change, update = handler swap), then cascade to the
// triggers resolving through it (their handlers close over the function
// snapshot, so each gets a fresh resolve + swap; a trigger that could not
// resolve before re-admits here).
func (ts *HTTPTriggerSet) applyFunctionIncremental(ctx context.Context, fn *fv1.Function) (routetable.ApplyResult, error) {
	key := types.NamespacedName{Namespace: fn.Namespace, Name: fn.Name}
	fnTimeout := map[crd.CacheKeyUG]int{crd.CacheKeyUGFromMeta(&fn.ObjectMeta): fn.Spec.FunctionTimeout}
	res := ts.routeTable.ApplyFunction(routetable.InternalKey{NamespacedName: key}, fn.Generation, func() http.Handler {
		return ts.buildInternalFunctionHandler(fn, fnTimeout)
	})
	routeTableApplies.Add(ctx, 1, metric.WithAttributes(attribute.String("result", res.String())))
	if res == routetable.ShapeChanged {
		ts.signalMaterialize()
	}
	ts.updateRoutesGauge()
	return res, ts.reapplyTriggersForFunction(ctx, key)
}

// deleteFunctionIncremental removes a deleted function's internal route and
// cascades to its triggers (whose resolve now fails NotFound, dropping their
// routes and marking FunctionNotFound).
func (ts *HTTPTriggerSet) deleteFunctionIncremental(ctx context.Context, key types.NamespacedName) error {
	if ts.routeTable.DeleteFunction(routetable.InternalKey{NamespacedName: key}) == routetable.ShapeChanged {
		ts.signalMaterialize()
		ts.updateRoutesGauge()
	}
	return ts.reapplyTriggersForFunction(ctx, key)
}

// reapplyTriggersForFunction re-applies every trigger resolving through (or
// unresolvably referencing) the given function. Aggregates transient errors
// so the reconciler requeues.
func (ts *HTTPTriggerSet) reapplyTriggersForFunction(ctx context.Context, key types.NamespacedName) error {
	var errs error
	for _, tkey := range ts.routeTable.TriggersForFunction(key) {
		trigger := &fv1.HTTPTrigger{}
		if err := ts.client.Get(ctx, tkey, trigger); err != nil {
			if apierrors.IsNotFound(err) {
				// The trigger is gone; its own delete event (or the resync)
				// removes the route.
				continue
			}
			errs = errors.Join(errs, err)
			continue
		}
		if _, err := ts.applyTriggerIncremental(ctx, trigger); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// reapplyTriggersForAlias re-applies every trigger resolving through (or
// unresolvably referencing) the given FunctionAlias — the alias-side mirror
// of reapplyTriggersForFunction. Each re-apply re-runs the full resolver
// (applyTriggerIncremental), so an alias repoint lands as a HandlerSwapped on
// every affected trigger's route.
func (ts *HTTPTriggerSet) reapplyTriggersForAlias(ctx context.Context, key types.NamespacedName) error {
	var errs error
	for _, tkey := range ts.routeTable.TriggersForAlias(key.Namespace, key.Name) {
		trigger := &fv1.HTTPTrigger{}
		if err := ts.client.Get(ctx, tkey, trigger); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			errs = errors.Join(errs, err)
			continue
		}
		if _, err := ts.applyTriggerIncremental(ctx, trigger); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// internalKeyForAlias / internalKeyForVersion build the InternalKey a
// materialized `:<alias>`/`:<version>` route lives at: the resolved
// function's namespace/name with the alias/version CR's own name as Suffix —
// the RFC-0025 URL grammar internalRouteExactURLs (routeshape.go) renders
// from it.
func internalKeyForAlias(alias *fv1.FunctionAlias) routetable.InternalKey {
	return routetable.InternalKey{
		NamespacedName: types.NamespacedName{Namespace: alias.Namespace, Name: alias.Spec.FunctionName},
		Suffix:         alias.Name,
	}
}

func internalKeyForVersion(v *fv1.FunctionVersion) routetable.InternalKey {
	return routetable.InternalKey{
		NamespacedName: types.NamespacedName{Namespace: v.Namespace, Name: v.Spec.FunctionName},
		Suffix:         v.Name,
	}
}

// applyAliasInternalRoute upserts (or, if the alias has not resolved, drops)
// the materialized `:<alias>` internal route: the handler always proxies to
// the alias's CURRENT effective target (Spec.Version when name-pinned, else
// Status.ResolvedVersion — mirroring functionReferenceResolver.resolveByAlias,
// but never the weighted split: a direct `:<alias>` call always reaches the
// PRIMARY target, matching the RFC's "whatever FunctionVersion it currently
// resolves to" framing). ApplyFunction keys the change-detection on the
// resolved version's Generation, which versioning.VersionedFunction sets to
// v.Spec.FunctionGeneration — unique per FunctionVersion by construction, so
// a repoint (even one that changes nothing observable, e.g. re-resolving to
// the same target) is a HandlerSwapped, never a spurious ShapeChanged: only
// route ADD/DELETE (a brand-new alias, or one that stops resolving) signals
// the materializer.
func (ts *HTTPTriggerSet) applyAliasInternalRoute(ctx context.Context, alias *fv1.FunctionAlias) (routetable.ApplyResult, error) {
	key := internalKeyForAlias(alias)
	target := alias.Spec.Version
	if target == "" {
		target = alias.Status.ResolvedVersion
	}
	if target == "" {
		// Not resolved yet: nothing to serve. Drop any stale route from a
		// prior resolution (e.g. the alias was repointed at a target that no
		// longer exists).
		res := ts.routeTable.DeleteFunction(key)
		if res == routetable.ShapeChanged {
			ts.signalMaterialize()
		}
		ts.updateRoutesGauge()
		return res, nil
	}

	fn, err := ts.resolver.resolveVersion(ctx, alias.Namespace, alias.Spec.FunctionName, target)
	if err != nil {
		if errors.Is(err, errFunctionNotFound) {
			res := ts.routeTable.DeleteFunction(key)
			if res == routetable.ShapeChanged {
				ts.signalMaterialize()
			}
			ts.updateRoutesGauge()
			return res, nil
		}
		return routetable.NoChange, err
	}

	fnTimeout := map[crd.CacheKeyUG]int{crd.CacheKeyUGFromMeta(&fn.ObjectMeta): fn.Spec.FunctionTimeout}
	res := ts.routeTable.ApplyFunction(key, fn.Generation, func() http.Handler {
		return ts.buildInternalFunctionHandler(fn, fnTimeout)
	})
	if res == routetable.ShapeChanged {
		ts.signalMaterialize()
	}
	ts.updateRoutesGauge()
	return res, nil
}

// applyAliasIncremental reconciles a FunctionAlias event: cascades to every
// trigger consuming it (re-resolve + HandlerSwapped, or re-admit for a
// trigger that was waiting on this alias) and upserts/refreshes its
// materialized `:<alias>` internal route. Errors from both halves are
// aggregated so the reconciler requeues on either failure.
func (ts *HTTPTriggerSet) applyAliasIncremental(ctx context.Context, alias *fv1.FunctionAlias) (routetable.ApplyResult, error) {
	res, err := ts.applyAliasInternalRoute(ctx, alias)
	cascadeErr := ts.reapplyTriggersForAlias(ctx, types.NamespacedName{Namespace: alias.Namespace, Name: alias.Name})
	return res, errors.Join(err, cascadeErr)
}

// deleteAliasIncremental handles a FunctionAlias deletion event: removes its
// materialized `:<alias>` internal route (found by namespace+suffix — the
// alias object, and with it its target function's name, is already gone) and
// cascades to its triggers, which re-resolve, fail with errFunctionNotFound,
// and drop their routes via the existing unresolved path.
func (ts *HTTPTriggerSet) deleteAliasIncremental(ctx context.Context, key types.NamespacedName) error {
	if ts.routeTable.DeleteInternalBySuffix(key.Namespace, key.Name) == routetable.ShapeChanged {
		ts.signalMaterialize()
		ts.updateRoutesGauge()
	}
	return ts.reapplyTriggersForAlias(ctx, key)
}

// applyVersionIncremental upserts (or, if the live function is gone, drops)
// the materialized `:<version>` internal route for one FunctionVersion. No
// trigger cascade: a trigger pinned directly to a Version resolves through
// the live-function index (see referencedFunctions), so a FunctionVersion
// create event is picked up by the periodic resync — `:<version>` routes
// exist for direct/rollback-warm invocation, not to gate trigger admission.
func (ts *HTTPTriggerSet) applyVersionIncremental(ctx context.Context, v *fv1.FunctionVersion) (routetable.ApplyResult, error) {
	key := internalKeyForVersion(v)
	fn, err := ts.resolver.resolveVersion(ctx, v.Namespace, v.Spec.FunctionName, v.Name)
	if err != nil {
		if errors.Is(err, errFunctionNotFound) {
			res := ts.routeTable.DeleteFunction(key)
			if res == routetable.ShapeChanged {
				ts.signalMaterialize()
			}
			ts.updateRoutesGauge()
			return res, nil
		}
		return routetable.NoChange, err
	}

	fnTimeout := map[crd.CacheKeyUG]int{crd.CacheKeyUGFromMeta(&fn.ObjectMeta): fn.Spec.FunctionTimeout}
	res := ts.routeTable.ApplyFunction(key, fn.Generation, func() http.Handler {
		return ts.buildInternalFunctionHandler(fn, fnTimeout)
	})
	if res == routetable.ShapeChanged {
		ts.signalMaterialize()
	}
	ts.updateRoutesGauge()
	return res, nil
}

// deleteVersionIncremental removes a deleted FunctionVersion's `:<version>`
// internal route, found by namespace+suffix (the version object, and with it
// its function's name, is already gone).
func (ts *HTTPTriggerSet) deleteVersionIncremental(namespace, name string) {
	if ts.routeTable.DeleteInternalBySuffix(namespace, name) == routetable.ShapeChanged {
		ts.signalMaterialize()
		ts.updateRoutesGauge()
	}
}

// signalMaterialize requests a debounced mux rebuild on the
// updateRouterRequestChannel, consumed by materializeLoop.
func (ts *HTTPTriggerSet) signalMaterialize() {
	ts.syncDebouncer(func() {
		ts.updateRouterRequestChannel <- struct{}{}
	})
}

// queuePendingCondition records a trigger whose RouteAdmitted condition
// should be marked after the next materialize (when its route is actually
// observable). Latest snapshot wins.
func (ts *HTTPTriggerSet) queuePendingCondition(trigger *fv1.HTTPTrigger) {
	ts.pendingMu.Lock()
	defer ts.pendingMu.Unlock()
	if ts.pendingConditions == nil {
		ts.pendingConditions = make(map[types.UID]*fv1.HTTPTrigger)
	}
	ts.pendingConditions[trigger.UID] = trigger
}

// drainPendingConditions returns and clears the queued triggers.
func (ts *HTTPTriggerSet) drainPendingConditions() []*fv1.HTTPTrigger {
	ts.pendingMu.Lock()
	defer ts.pendingMu.Unlock()
	out := make([]*fv1.HTTPTrigger, 0, len(ts.pendingConditions))
	for _, tr := range ts.pendingConditions {
		out = append(out, tr)
	}
	ts.pendingConditions = nil
	return out
}

// isConflictLoser reports whether a trigger is currently shadowed by a route
// conflict (phase 2). Reads happen on the reconciler/resync goroutines while
// materialize writes, hence the lock.
func (ts *HTTPTriggerSet) isConflictLoser(key types.NamespacedName) bool {
	ts.pendingMu.Lock()
	defer ts.pendingMu.Unlock()
	_, shadowed := ts.conflictLosers[key]
	return shadowed
}

// setConflictLosers swaps in the current shadow set and returns the previous
// one (for cleared-conflict detection).
func (ts *HTTPTriggerSet) setConflictLosers(current map[types.NamespacedName]routetable.Conflict) map[types.NamespacedName]routetable.Conflict {
	ts.pendingMu.Lock()
	defer ts.pendingMu.Unlock()
	prev := ts.conflictLosers
	ts.conflictLosers = current
	return prev
}

// updateRoutesGauge publishes the table sizes.
func (ts *HTTPTriggerSet) updateRoutesGauge() {
	pub, internal := ts.routeTable.Sizes()
	routesTotal.Record(context.Background(), int64(pub), metric.WithAttributes(attribute.String("listener", "public")))
	routesTotal.Record(context.Background(), int64(internal), metric.WithAttributes(attribute.String("listener", "internal")))
}

// materializeLoop consumes debounced shape-change signals and rebuilds the
// muxes from the route table — the router's only production mux-rebuild loop.
func (ts *HTTPTriggerSet) materializeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ts.updateRouterRequestChannel:
		}
		ts.materialize(ctx)
	}
}

// buildIncrementalMuxes builds the public + internal listener muxes from a
// route-table materialization snapshot: the precedence-ordered public routes,
// the internal function-invocation routes (GHSA split — never on the public
// mux), and the router-owned routes. Registration goes through the same helpers
// as the one-shot buildMuxes (routeshape.go), so the two paths cannot drift.
// materialize swaps in .Handler() of each; tests introspect the *httpmux.Mux.
func (ts *HTTPTriggerSet) buildIncrementalMuxes(featureConfig *config.FeatureConfig, m routetable.Materialization) (public, internal *httpmux.Mux) {
	publicMux, internalMux := ts.newListenerMuxes(featureConfig)

	// Precedence-ordered registration (phase 2): hosted before host-less,
	// exact before prefix, longest prefix first, creation-time tiebreak.
	// httpmux dispatches to the first matching registration, so the order IS
	// the precedence.
	for _, r := range m.Routes {
		shape := routeShape{host: r.Host, methods: r.Methods}
		if r.Exact {
			shape.exactPath = r.Path
		} else {
			shape.prefixPath = r.Path
		}
		registerRouteShape(publicMux, shape, r.Handler)
	}

	for _, ispec := range ts.routeTable.InternalSnapshot() {
		registerInternalRoute(internalMux, ispec.Key, ispec.Handler)
	}

	ts.registerRouterOwnedRoutes(publicMux, featureConfig, m.HomeClaimed)
	ts.registerAsyncDLQRoutes(internalMux)
	ts.registerTopicRoutes(internalMux)
	return publicMux, internalMux
}

// materialize rebuilds both listener muxes from a route-table snapshot and
// swaps them in, then marks the conditions of the triggers whose shape
// changes were in this batch. Registration goes through the same helpers as
// the one-shot buildMuxes (routeshape.go), so the two paths cannot drift.
//
// A failed materialize is STICKY: the drained conditions are re-queued, the
// dirty flag stays set, and the resync loop re-signals every tick until a
// build succeeds — the consumed signal must not strand the table's state out
// of the mux (a stale mux self-reports routes it does not serve).
func (ts *HTTPTriggerSet) materialize(ctx context.Context) {
	featureConfig, err := ts.featureConfigFn(ts.logger)
	if err != nil {
		ts.logger.Error(err, "error reading feature config; mux materialization failed (will retry)")
		materializeFailures.Add(ctx, 1)
		ts.materializeDirty.Store(true)
		for _, trigger := range ts.drainPendingConditions() {
			ts.markTriggerCondition(ctx, trigger,
				metav1.ConditionFalse, fv1.HTTPTriggerReasonMuxBuildFail,
				"router failed to build mux: "+err.Error(),
				"trigger is not serving due to router mux error")
			// Re-queue so the retry's success flips the condition to True;
			// consuming the batch here would leave a serving route marked
			// failed (or, worse, a non-serving route later marked True by
			// the resync's NoChange path).
			ts.queuePendingCondition(trigger)
		}
		return
	}

	m := ts.routeTable.Materialization()
	publicMux, internalMux := ts.buildIncrementalMuxes(featureConfig, m)

	ts.updateRouter(publicMux.Handler())
	if ts.internalMutableRouter != nil {
		ts.internalMutableRouter.updateRouter(internalMux.Handler())
	}
	ts.materializeDirty.Store(false)
	muxRebuilds.Add(ctx, 1, metric.WithAttributes(attribute.String("listener", "public"), attribute.String("reason", "shape_change")))
	muxRebuilds.Add(ctx, 1, metric.WithAttributes(attribute.String("listener", "internal"), attribute.String("reason", "shape_change")))
	// The mux now serves user routes — report ready (gates /readyz).
	ts.ready.Store(true)

	ts.reportConflicts(ctx, m.Conflicts)

	// Mark the batch's triggers admitted now that their routes are
	// observable (the conditions are marked after the mux swap so they reflect
	// observable state). A trigger that is
	// shadowed by a conflict keeps the False condition reportConflicts set.
	for _, trigger := range ts.drainPendingConditions() {
		if ts.isConflictLoser(types.NamespacedName{Namespace: trigger.Namespace, Name: trigger.Name}) {
			continue
		}
		ts.markTriggerCondition(ctx, trigger,
			metav1.ConditionTrue, fv1.HTTPTriggerReasonRouteAdmitted,
			"router accepted the trigger and installed its mux entry",
			"trigger is serving")
	}
}

// reportConflicts maintains the RouteAdmitted=False/RouteConflict conditions
// across materializations: current losers are marked False naming their
// winner; triggers that stopped losing (their winner was deleted or
// reshaped) flip back to True.
func (ts *HTTPTriggerSet) reportConflicts(ctx context.Context, conflicts []routetable.Conflict) {
	current := make(map[types.NamespacedName]routetable.Conflict, len(conflicts))
	for _, c := range conflicts {
		current[c.Loser] = c
	}
	prev := ts.setConflictLosers(current)

	for _, c := range conflicts {
		if _, known := prev[c.Loser]; !known {
			ts.logger.Info("route conflict: trigger is shadowed by an identical route shape",
				"trigger", c.Loser.String(), "winner", c.Winner.String())
		}
		trigger := &fv1.HTTPTrigger{}
		if err := ts.client.Get(ctx, c.Loser, trigger); err != nil {
			if !apierrors.IsNotFound(err) {
				// Transient: the condition stays stale until the next
				// materialize re-runs this loop. NotFound needs no action
				// (the delete event cleans the table and the conflict).
				ts.logger.Error(err, "failed to read conflicting trigger for condition update", "trigger", c.Loser.String())
			}
			continue
		}
		ts.markTriggerCondition(ctx, trigger,
			metav1.ConditionFalse, fv1.HTTPTriggerReasonRouteConflict,
			fmt.Sprintf("route is shadowed: trigger %s registered the same route shape and wins by precedence (oldest first)", c.Winner.String()),
			"trigger is not serving; its route shape is owned by "+c.Winner.String())
	}
	for loser := range prev {
		if _, still := current[loser]; still {
			continue
		}
		ts.logger.Info("route conflict cleared: trigger is serving again", "trigger", loser.String())
		trigger := &fv1.HTTPTrigger{}
		if err := ts.client.Get(ctx, loser, trigger); err != nil {
			if !apierrors.IsNotFound(err) {
				ts.logger.Error(err, "failed to read formerly-conflicting trigger for condition update", "trigger", loser.String())
			}
			continue
		}
		ts.markTriggerCondition(ctx, trigger,
			metav1.ConditionTrue, fv1.HTTPTriggerReasonRouteAdmitted,
			"router accepted the trigger and installed its mux entry",
			"trigger is serving")
	}
}

// resyncLoop is the drift guard: it periodically re-lists triggers +
// functions from the Manager cache and re-applies them. Anything it corrects
// is a missed watch event; the incremental path must self-heal from one
// regardless. It also re-arms the materializer after a failed build (the
// sticky dirty flag).
func (ts *HTTPTriggerSet) resyncLoop(ctx context.Context) {
	ticker := time.NewTicker(resyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if ts.materializeDirty.Load() {
			ts.signalMaterialize()
		}
		if _, err := ts.resync(ctx, false); err != nil {
			resyncFailures.Add(ctx, 1)
			ts.logger.Error(err, "route table resync failed; routes keep serving from the last good state")
		}
	}
}

// resync reconciles the full trigger + function + FunctionAlias +
// FunctionVersion lists into the table. initial=true is the startup build
// (population is expected and not drift); afterwards every correction
// increments the drift counter. Returns the drift count so tests can assert
// zero-drift directly (the RFC-0025 zero-drift gate) rather than inferring it
// from the materializer signal, which HandlerSwapped-only corrections never
// trip.
func (ts *HTTPTriggerSet) resync(ctx context.Context, initial bool) (int, error) {
	var triggerList fv1.HTTPTriggerList
	if err := ts.client.List(ctx, &triggerList); err != nil {
		return 0, fmt.Errorf("listing http triggers: %w", err)
	}
	var functionList fv1.FunctionList
	if err := ts.client.List(ctx, &functionList); err != nil {
		return 0, fmt.Errorf("listing functions: %w", err)
	}
	var aliasList fv1.FunctionAliasList
	if err := ts.client.List(ctx, &aliasList); err != nil {
		return 0, fmt.Errorf("listing function aliases: %w", err)
	}
	var versionList fv1.FunctionVersionList
	if err := ts.client.List(ctx, &versionList); err != nil {
		return 0, fmt.Errorf("listing function versions: %w", err)
	}

	drift := 0
	var errs error

	// Functions first so trigger resolution sees fresh internal routes.
	liveFns := make(map[types.NamespacedName]struct{}, len(functionList.Items))
	for i := range functionList.Items {
		fn := &functionList.Items[i]
		key := types.NamespacedName{Namespace: fn.Namespace, Name: fn.Name}
		liveFns[key] = struct{}{}
		fnTimeout := map[crd.CacheKeyUG]int{crd.CacheKeyUGFromMeta(&fn.ObjectMeta): fn.Spec.FunctionTimeout}
		// Apply the internal route directly (no trigger cascade — the
		// triggers are re-applied below in this same pass).
		res := ts.routeTable.ApplyFunction(routetable.InternalKey{NamespacedName: key}, fn.Generation, func() http.Handler {
			return ts.buildInternalFunctionHandler(fn, fnTimeout)
		})
		if res != routetable.NoChange {
			drift++
			if res == routetable.ShapeChanged {
				ts.signalMaterialize()
			}
		}
	}

	// FunctionAlias / FunctionVersion (RFC-0025): upsert/refresh each live
	// object's materialized internal route, tracking the InternalKeys that
	// remain live so the cleanup pass below can tell a stale alias/version
	// route apart from a stale plain function route.
	liveAliasKeys := make(map[routetable.InternalKey]struct{}, len(aliasList.Items))
	for i := range aliasList.Items {
		alias := &aliasList.Items[i]
		liveAliasKeys[internalKeyForAlias(alias)] = struct{}{}
		res, err := ts.applyAliasInternalRoute(ctx, alias)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		if res != routetable.NoChange {
			drift++
		}
	}
	liveVersionKeys := make(map[routetable.InternalKey]struct{}, len(versionList.Items))
	for i := range versionList.Items {
		v := &versionList.Items[i]
		liveVersionKeys[internalKeyForVersion(v)] = struct{}{}
		res, err := ts.applyVersionIncremental(ctx, v)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		if res != routetable.NoChange {
			drift++
		}
	}

	for _, key := range ts.routeTable.InternalKeys() {
		if key.Suffix == "" {
			if _, ok := liveFns[key.NamespacedName]; ok {
				continue
			}
			// Re-check before deleting: the function may have been created
			// while this pass was walking (its reconciler already applied
			// it) — the LIST snapshot must not tear down a fresher table
			// entry.
			if err := ts.client.Get(ctx, key.NamespacedName, &fv1.Function{}); err == nil || !apierrors.IsNotFound(err) {
				continue
			}
			if ts.routeTable.DeleteFunction(key) == routetable.ShapeChanged {
				drift++
				ts.signalMaterialize()
			}
			continue
		}
		// A materialized alias or version route: live in EITHER set means
		// keep it (the alias-name and version-name spaces are independent,
		// so a suffix collision across the two kinds is not this loop's
		// concern — both applied it under the identical InternalKey above,
		// so at most one is "stale" and the loop below only fires with
		// neither claiming it).
		if _, ok := liveAliasKeys[key]; ok {
			continue
		}
		if _, ok := liveVersionKeys[key]; ok {
			continue
		}
		if ts.routeTable.DeleteFunction(key) == routetable.ShapeChanged {
			drift++
			ts.signalMaterialize()
		}
	}

	liveTriggers := make(map[types.UID]struct{}, len(triggerList.Items))
	for i := range triggerList.Items {
		trigger := &triggerList.Items[i]
		liveTriggers[trigger.UID] = struct{}{}
		res, err := ts.applyTriggerIncremental(ctx, trigger)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		if res != routetable.NoChange {
			drift++
		}
	}
	for uid, key := range ts.routeTable.PublicTriggers() {
		if _, ok := liveTriggers[uid]; ok {
			continue
		}
		// Mid-pass-create guard (same idea as the function side): re-read
		// before deleting, but compare UIDs — a live object with the SAME
		// UID means the trigger was created while this pass walked (its
		// reconciler already applied it; the LIST snapshot is just stale).
		// A live object with a DIFFERENT UID is a delete+recreate whose old
		// entry must still be dropped, and a transient read error skips the
		// delete (the next pass retries).
		cur := &fv1.HTTPTrigger{}
		switch err := ts.client.Get(ctx, key, cur); {
		case err == nil && cur.UID == uid:
			continue
		case err != nil && !apierrors.IsNotFound(err):
			continue
		}
		if ts.routeTable.DeleteTrigger(uid) == routetable.ShapeChanged {
			drift++
			ts.signalMaterialize()
		}
	}

	ts.updateRoutesGauge()
	if !initial && drift > 0 {
		resyncDrift.Add(ctx, int64(drift))
		ts.logger.Info("route table resync corrected drift (a watch event was missed)", "corrections", drift)
	}
	return drift, errs
}

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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	config "github.com/fission/fission/pkg/featureconfig"
	"github.com/fission/fission/pkg/router/routetable"
)

// This file is the incremental route-update path (RFC-0013 phase 1), the
// default since ROUTER_INCREMENTAL_ROUTES landed. The reconcilers feed
// per-event diffs into the route table; handler-only changes (canary weight
// ticks, function updates) swap an atomic pointer and never rebuild a mux;
// shape changes signal the debounced materializer, which rebuilds the gorilla
// muxes from a table snapshot through the same registration helpers as the
// legacy path (routeshape.go).
//
// The legacy full-rebuild loop (updateRouter + buildMuxes) is retained behind
// ROUTER_INCREMENTAL_ROUTES=false for one release as the escape hatch.

// resyncInterval is how often the drift guard re-lists triggers + functions
// and diffs them against the route table. Both lists come from the Manager's
// in-memory cache, so a pass is cheap; anything it corrects after startup is
// a missed watch event and increments fission_router_route_resync_drift_total.
const resyncInterval = 60 * time.Second

// applyTriggerIncremental reconciles one trigger into the route table:
// validate → resolve → apply. Returns the apply result so the resync loop
// can count drift, and an error only for transient resolve failures (the
// reconciler requeues; the last-known-good route keeps serving — parity with
// the legacy path, where a LIST error kept the old mux).
func (ts *HTTPTriggerSet) applyTriggerIncremental(ctx context.Context, trigger *fv1.HTTPTrigger) (routetable.ApplyResult, error) {
	// Invalid CORS/ingress config: the route must not serve (parity with the
	// legacy skip), and the user sees why on the trigger's conditions.
	if reason, cfgErr := triggerConfigError(trigger); cfgErr != nil {
		res := ts.routeTable.DeleteTrigger(trigger.UID)
		routeTableApplies.WithLabelValues("rejected").Inc()
		if res == routetable.ShapeChanged {
			ts.signalMaterialize()
		}
		ts.markTriggerCondition(ctx, trigger, metav1.ConditionFalse, reason,
			"router rejected the trigger configuration: "+cfgErr.Error(),
			"trigger is not serving due to invalid configuration")
		return res, nil
	}

	rr, err := ts.resolver.resolve(ctx, *trigger)
	if err != nil {
		if errors.Is(err, errFunctionNotFound) {
			// The referenced function does not exist: drop the route (it
			// would 404 anyway) and say so on the trigger. A later function
			// create re-admits it via the function reconciler's cascade.
			res := ts.routeTable.DeleteTrigger(trigger.UID)
			routeTableApplies.WithLabelValues("rejected").Inc()
			if res == routetable.ShapeChanged {
				ts.signalMaterialize()
			}
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
		res := ts.routeTable.DeleteTrigger(trigger.UID)
		routeTableApplies.WithLabelValues("rejected").Inc()
		if res == routetable.ShapeChanged {
			ts.signalMaterialize()
		}
		ts.markTriggerCondition(ctx, trigger, metav1.ConditionFalse, fv1.HTTPTriggerReasonMuxBuildFail,
			fmt.Sprintf("resolve result type not implemented: %v", rr.resolveResultType),
			"trigger is not serving due to an unsupported function reference")
		return res, nil
	}

	shape := deriveRouteShape(trigger)
	fnRVs := make(map[string]string, len(rr.functionMap))
	fnTimeout := make(map[types.UID]int, len(rr.functionMap))
	for name, fn := range rr.functionMap {
		fnRVs[name] = fn.ResourceVersion
		fnTimeout[fn.UID] = fn.Spec.FunctionTimeout
	}
	spec := &routetable.RouteSpec{
		TriggerUID: trigger.UID,
		Namespace:  trigger.Namespace,
		Name:       trigger.Name,
		TriggerRV:  trigger.ResourceVersion,
		FnRVs:      fnRVs,
		ExactPath:  shape.exactPath,
		PrefixPath: shape.prefixPath,
		Host:       shape.host,
		Methods:    shape.methods,
		Created:    trigger.CreationTimestamp,
	}
	res := ts.routeTable.ApplyTrigger(spec, func() http.Handler {
		return ts.buildTriggerHandler(trigger, rr, fnTimeout)
	})
	routeTableApplies.WithLabelValues(res.String()).Inc()

	switch res {
	case routetable.ShapeChanged:
		// The route becomes observable only after the debounced materialize;
		// queue the condition so it is marked after the swap (parity with the
		// legacy path, which marks after updateRouter's swap).
		ts.queuePendingCondition(trigger)
		ts.signalMaterialize()
	default:
		// NoChange / HandlerSwapped: the route is already live (the swap is
		// immediate); mark right away. markTriggerCondition's fast path makes
		// the NoChange case a no-op write.
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
// snapshot, so each gets a fresh resolve + swap).
func (ts *HTTPTriggerSet) applyFunctionIncremental(ctx context.Context, fn *fv1.Function) (routetable.ApplyResult, error) {
	key := types.NamespacedName{Namespace: fn.Namespace, Name: fn.Name}
	fnTimeout := map[types.UID]int{fn.UID: fn.Spec.FunctionTimeout}
	res := ts.routeTable.ApplyFunction(key, fn.ResourceVersion, func() http.Handler {
		return ts.buildInternalFunctionHandler(fn, fnTimeout)
	})
	routeTableApplies.WithLabelValues(res.String()).Inc()
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
	if ts.routeTable.DeleteFunction(key) == routetable.ShapeChanged {
		ts.signalMaterialize()
		ts.updateRoutesGauge()
	}
	return ts.reapplyTriggersForFunction(ctx, key)
}

// reapplyTriggersForFunction re-applies every trigger resolving through the
// given function. Aggregates transient errors so the reconciler requeues.
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

// signalMaterialize requests a debounced mux rebuild — same channel and
// debouncer the legacy syncTriggers path uses, consumed by materializeLoop
// in incremental mode.
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

// updateRoutesGauge publishes the table sizes.
func (ts *HTTPTriggerSet) updateRoutesGauge() {
	pub, internal := ts.routeTable.Sizes()
	routesTotal.WithLabelValues("public").Set(float64(pub))
	routesTotal.WithLabelValues("internal").Set(float64(internal))
}

// materializeLoop consumes debounced shape-change signals and rebuilds the
// muxes from the route table. The incremental-mode counterpart of
// updateRouter.
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

// materialize rebuilds both listener muxes from a route-table snapshot and
// swaps them in, then marks the conditions of the triggers whose shape
// changes were in this batch. Registration goes through the same helpers as
// the legacy buildMuxes (routeshape.go), so the two paths cannot drift.
func (ts *HTTPTriggerSet) materialize(ctx context.Context) {
	featureConfig, err := config.GetFeatureConfig(ts.logger)
	if err != nil {
		ts.logger.Error(err, "error reading feature config; skipping mux materialization")
		for _, trigger := range ts.drainPendingConditions() {
			ts.markTriggerCondition(ctx, trigger,
				metav1.ConditionFalse, fv1.HTTPTriggerReasonMuxBuildFail,
				"router failed to build mux: "+err.Error(),
				"trigger is not serving due to router mux error")
		}
		return
	}

	public, internal := ts.newListenerMuxes(featureConfig)

	homeHandled := false
	for _, spec := range ts.routeTable.Snapshot() {
		shape := routeShape{
			exactPath:  spec.ExactPath,
			prefixPath: spec.PrefixPath,
			host:       spec.Host,
			methods:    spec.Methods,
		}
		registerRouteShape(public, shape, spec.Handler)
		if shape.claimsHome() {
			homeHandled = true
		}
	}

	// Internal listener: the function invocation routes (GHSA split — these
	// must never appear on the public mux).
	for _, ispec := range ts.routeTable.InternalSnapshot() {
		exact, prefix := internalRoutePair(ispec.Key)
		internal.Handle(exact, ispec.Handler)
		internal.PathPrefix(prefix).Handler(ispec.Handler)
	}

	ts.registerRouterOwnedRoutes(public, featureConfig, homeHandled)

	ts.mutableRouter.updateRouter(public)
	if ts.internalMutableRouter != nil {
		ts.internalMutableRouter.updateRouter(internal)
	}
	muxRebuilds.WithLabelValues("public", "shape_change").Inc()
	muxRebuilds.WithLabelValues("internal", "shape_change").Inc()
	// The mux now serves user routes — report ready (gates /readyz).
	ts.ready.Store(true)

	// Mark the batch's triggers admitted now that their routes are
	// observable (parity with the legacy post-swap loop).
	for _, trigger := range ts.drainPendingConditions() {
		ts.markTriggerCondition(ctx, trigger,
			metav1.ConditionTrue, fv1.HTTPTriggerReasonRouteAdmitted,
			"router accepted the trigger and installed its mux entry",
			"trigger is serving")
	}
}

// resyncLoop is the drift guard: it periodically re-lists triggers +
// functions from the Manager cache and re-applies them. Anything it corrects
// is a missed watch event; today's full-rebuild path self-heals every event,
// so the incremental path must demonstrably do the same.
func (ts *HTTPTriggerSet) resyncLoop(ctx context.Context) {
	ticker := time.NewTicker(resyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := ts.resync(ctx, false); err != nil {
			ts.logger.Error(err, "route table resync failed; routes keep serving from the last good state")
		}
	}
}

// resync reconciles the full trigger + function lists into the table.
// initial=true is the startup build (population is expected and not drift);
// afterwards every correction increments the drift counter.
func (ts *HTTPTriggerSet) resync(ctx context.Context, initial bool) error {
	var triggerList fv1.HTTPTriggerList
	if err := ts.client.List(ctx, &triggerList); err != nil {
		return fmt.Errorf("listing http triggers: %w", err)
	}
	var functionList fv1.FunctionList
	if err := ts.client.List(ctx, &functionList); err != nil {
		return fmt.Errorf("listing functions: %w", err)
	}

	drift := 0
	var errs error

	// Functions first so trigger resolution sees fresh internal routes.
	liveFns := make(map[types.NamespacedName]struct{}, len(functionList.Items))
	for i := range functionList.Items {
		fn := &functionList.Items[i]
		key := types.NamespacedName{Namespace: fn.Namespace, Name: fn.Name}
		liveFns[key] = struct{}{}
		fnTimeout := map[types.UID]int{fn.UID: fn.Spec.FunctionTimeout}
		// Apply the internal route directly (no trigger cascade — the
		// triggers are re-applied below in this same pass).
		res := ts.routeTable.ApplyFunction(key, fn.ResourceVersion, func() http.Handler {
			return ts.buildInternalFunctionHandler(fn, fnTimeout)
		})
		if res != routetable.NoChange {
			drift++
			if res == routetable.ShapeChanged {
				ts.signalMaterialize()
			}
		}
	}
	for _, key := range ts.routeTable.InternalKeys() {
		if _, ok := liveFns[key]; !ok {
			if ts.routeTable.DeleteFunction(key) == routetable.ShapeChanged {
				drift++
				ts.signalMaterialize()
			}
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
	for _, uid := range ts.routeTable.PublicUIDs() {
		if _, ok := liveTriggers[uid]; !ok {
			if ts.routeTable.DeleteTrigger(uid) == routetable.ShapeChanged {
				drift++
				ts.signalMaterialize()
			}
		}
	}

	ts.updateRoutesGauge()
	if !initial && drift > 0 {
		resyncDrift.Add(float64(drift))
		ts.logger.Info("route table resync corrected drift (a watch event was missed)", "corrections", drift)
	}
	return errs
}

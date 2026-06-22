// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// httpTriggerReconciler and functionReconciler replace the router's HTTPTrigger
// and Function informers + event handlers. Both are thin: each reconcile
// performs its per-object side effect (ingress reconciliation, route-table
// apply) via the incremental path (RFC-0013) — a handler-only change swaps an
// atomic pointer, while shape changes signal the debounced materializer.
// Many concurrent shape changes collapse into one mux rebuild via the
// syncDebouncer behind signalMaterialize — the single-key coalescing pattern.
//
// Both are registered with GenerationChangedPredicate (via controller.Register),
// so status-only writes are dropped: the router's own HTTPTrigger condition
// writes and the executor's Function readiness writes do not trigger spurious
// rebuilds, matching the old generation-based informer filters.

// httpTriggerReconciler reconciles a trigger's external route (via the
// registered RouteProviders) and applies the per-event route-table diff.
type httpTriggerReconciler struct {
	logger    logr.Logger
	client    client.Client
	ts        *HTTPTriggerSet
	providers []RouteProvider
}

func (r *httpTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	trigger := &fv1.HTTPTrigger{}
	if err := r.client.Get(ctx, req.NamespacedName, trigger); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted: drop any route object it owned (idempotent by name) and
			// remove its route. Aggregate provider errors and return
			// them so a transient API failure on delete is retried rather than
			// leaking an orphaned route object.
			var errs error
			for _, p := range r.providers {
				if err := p.DeleteByName(ctx, req.Name); err != nil {
					r.logger.Error(err, "failed to delete route on trigger deletion", "provider", p.Name(), "trigger", req.Name)
					errs = errors.Join(errs, fmt.Errorf("%s: %w", p.Name(), err))
				}
			}
			r.ts.deleteTriggerIncremental(req.NamespacedName)
			return ctrl.Result{}, errs
		}
		return ctrl.Result{}, err
	}
	// Warn when a trigger requests a provider the router did not register (e.g.
	// requesting the gateway provider while GATEWAY_API_ENABLED is unset): no
	// provider will create a route, so make the misconfiguration visible in the
	// router logs rather than failing silently.
	if want := desiredRouteProvider(trigger); want != "" && !r.hasProvider(want) {
		r.logger.Info("trigger requests a route provider that is not enabled on this router; no route will be created",
			"provider", want, "trigger", trigger.Name, "namespace", trigger.Namespace)
	}
	// Each provider creates/updates its object when the trigger requests it and
	// deletes its object otherwise, so switching providers self-cleans. Errors
	// are aggregated and returned so controller-runtime requeues with backoff —
	// a transient API/RBAC error then retries instead of being dropped. (A
	// permanent config error, e.g. provider=gateway with no parentRefs, also
	// requeues; backoff caps the cost and the error is logged each pass.)
	var errs error
	for _, p := range r.providers {
		if err := p.Reconcile(ctx, trigger); err != nil {
			r.logger.Error(err, "failed to reconcile route", "provider", p.Name(), "trigger", trigger.Name)
			errs = errors.Join(errs, fmt.Errorf("%s: %w", p.Name(), err))
		}
	}
	// Per-event diff (RFC-0013): validate → resolve → route-table apply.
	// A handler-only change swaps an atomic pointer; only shape changes
	// signal the materializer. A transient resolve error is returned so
	// controller-runtime requeues (the last-known-good route keeps
	// serving).
	if _, err := r.ts.applyTriggerIncremental(ctx, trigger); err != nil {
		errs = errors.Join(errs, err)
	}
	return ctrl.Result{}, errs
}

// hasProvider reports whether a provider with the given name is registered.
func (r *httpTriggerReconciler) hasProvider(name fv1.RouteProviderType) bool {
	for _, p := range r.providers {
		if p.Name() == string(name) {
			return true
		}
	}
	return false
}

// functionReconciler re-applies a changed Function's internal route and the
// triggers that reference it (RFC-0013 per-event diff — a handler swap for
// spec/weight changes, a mux rebuild only when the route shape changes).
type functionReconciler struct {
	logger logr.Logger
	client client.Client
	ts     *HTTPTriggerSet
}

func (r *functionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fn := &fv1.Function{}
	if err := r.client.Get(ctx, req.NamespacedName, fn); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted: drop the internal route and cascade to the triggers
			// resolving through it.
			return ctrl.Result{}, r.ts.deleteFunctionIncremental(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, err
	}
	// Per-event diff (RFC-0013): upsert the internal route (insert =
	// shape change, update = handler swap) and re-apply the referencing
	// triggers via the fn index — function churn never rebuilds a mux.
	_, err := r.ts.applyFunctionIncremental(ctx, fn)
	return ctrl.Result{}, err
}

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
// and Function informers + event handlers. Both are thin: the heavy lifting (a
// debounced full mux rebuild from the Manager cache) already lived behind the
// updateRouterRequestChannel, so each reconcile just performs its per-object
// side effect (ingress reconciliation / resolver-cache invalidation) and signals
// a rebuild. Many concurrent events collapse into one rebuild via the existing
// syncDebouncer — this is the single-key coalescing pattern.
//
// Both are registered with GenerationChangedPredicate (via controller.Register),
// so status-only writes are dropped: the router's own HTTPTrigger condition
// writes and the executor's Function readiness writes do not trigger spurious
// rebuilds, matching the old generation-based informer filters.

// httpTriggerReconciler reconciles a trigger's external route (via the
// registered RouteProviders) and rebuilds the mux.
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
			// rebuild the mux without it. Aggregate provider errors and return
			// them so a transient API failure on delete is retried rather than
			// leaking an orphaned route object.
			var errs error
			for _, p := range r.providers {
				if err := p.DeleteByName(ctx, req.Name); err != nil {
					r.logger.Error(err, "failed to delete route on trigger deletion", "provider", p.Name(), "trigger", req.Name)
					errs = errors.Join(errs, fmt.Errorf("%s: %w", p.Name(), err))
				}
			}
			r.ts.syncTriggers()
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
	r.ts.syncTriggers()
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

// functionReconciler invalidates the resolver cache for a changed Function and
// rebuilds the mux (function spec changes can alter routing / weights).
type functionReconciler struct {
	logger logr.Logger
	client client.Client
	ts     *HTTPTriggerSet
}

func (r *functionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fn := &fv1.Function{}
	if err := r.client.Get(ctx, req.NamespacedName, fn); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted: nothing to invalidate by ResourceVersion; rebuild so the
			// function's routes drop out.
			r.ts.syncTriggers()
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if r.ts.resolver != nil {
		r.ts.resolver.invalidateForFunction(fn.Namespace, fn.Name, fn.ResourceVersion)
	}
	r.ts.syncTriggers()
	return ctrl.Result{}, nil
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"

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

// httpTriggerReconciler reconciles a trigger's Ingress and rebuilds the mux.
type httpTriggerReconciler struct {
	logger logr.Logger
	client client.Client
	ts     *HTTPTriggerSet
}

func (r *httpTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	trigger := &fv1.HTTPTrigger{}
	if err := r.client.Get(ctx, req.NamespacedName, trigger); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted: drop any Ingress it owned (idempotent by name) and
			// rebuild the mux without it.
			deleteIngressByName(ctx, r.logger, req.Name, r.ts.kubeClient)
			r.ts.syncTriggers()
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	reconcileIngress(ctx, r.logger, trigger, r.ts.kubeClient)
	r.ts.syncTriggers()
	return ctrl.Result{}, nil
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

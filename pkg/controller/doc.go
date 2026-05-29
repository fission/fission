// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package controller holds the shared scaffolding Fission's CRD controllers use
// to run as controller-runtime Reconcilers (RFC-0005 WS3). It deliberately
// stays small — a registration helper and a status-condition writer — rather
// than a generic Reconciler base type, because the subsystems' side effects
// (cron scheduling, queue subscriptions, builder Deployments, mux rebuilds) are
// too different to share a body usefully.
//
// # Canonical Reconcile shape
//
// Each Fission CRD Reconciler follows the same skeleton:
//
//	func (r *XReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
//		obj, err := r.client.Get(ctx, req.Name, metav1.GetOptions{})  // namespaced typed client
//		if apierrors.IsNotFound(err) {
//			r.teardown(req.NamespacedName)   // stop cron / unsubscribe / delete builder
//			return ctrl.Result{}, nil
//		}
//		if err != nil {
//			return ctrl.Result{}, err        // controller-runtime requeues with backoff
//		}
//		if err := r.apply(ctx, obj); err != nil {
//			return ctrl.Result{}, err        // requeue; no os.Exit
//		}
//		controller.SetConditions(ctx, r.log, r.client.XS(req.Namespace), obj, want...)
//		return ctrl.Result{}, nil            // or {RequeueAfter: d} for time-driven work
//	}
//
// Deletion uses the NotFound path, not finalizers: teardown for these
// subsystems is in-memory only (stop a goroutine, drop a map entry), so there
// is nothing that must complete before the object is allowed to vanish. In-
// memory state is therefore keyed by req.NamespacedName, which is available on
// a NotFound (the object UID is not).
//
// Register applies predicate.GenerationChangedPredicate so a Reconciler is not
// re-queued by its own status writes; see Register.
package controller

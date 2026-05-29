// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Register wires reconciler into mgr as a controller watching obj, with the
// standard Fission event filter: GenerationChangedPredicate drops spec-unchanged
// (i.e. status-only) updates so a controller doesn't re-trigger on its own
// status writes, while still delivering Adds and Deletes — deletion teardown
// relies on the delete event flowing through. name is the controller's unique
// name within the Manager.
//
// Controllers registered this way inherit the Manager's leader-election scope:
// on a leader-elected Manager they run only on the elected replica; on a
// non-elected Manager (e.g. the router) they run on every replica.
func Register(mgr ctrl.Manager, obj client.Object, reconciler reconcile.Reconciler, name string) error {
	return RegisterWithConcurrency(mgr, obj, reconciler, name, 0)
}

// RegisterWithConcurrency is Register with an explicit MaxConcurrentReconciles.
// maxConcurrent <= 0 leaves the controller-runtime default (1). A higher value
// lets a subsystem reconcile independent objects in parallel (e.g. mqtrigger
// subscribing many triggers at once); controller-runtime still serializes
// reconciles for the same key, so in-memory state keyed by NamespacedName stays
// safe.
func RegisterWithConcurrency(mgr ctrl.Manager, obj client.Object, reconciler reconcile.Reconciler, name string, maxConcurrent int) error {
	b := builder.ControllerManagedBy(mgr).
		For(obj, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named(name)
	if maxConcurrent > 0 {
		b = b.WithOptions(ctrlcontroller.Options{MaxConcurrentReconciles: maxConcurrent})
	}
	return b.Complete(reconciler)
}

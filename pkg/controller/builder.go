// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	return builder.ControllerManagedBy(mgr).
		For(obj, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named(name).
		Complete(reconciler)
}

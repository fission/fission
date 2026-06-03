// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
)

// functionManager is the subset of *Container the Function handlers drive.
// Defined as an interface so the reconcile routing (create-vs-update on the
// last-reconciled object) is unit-testable with a fake.
type functionManager interface {
	createFunction(context.Context, *fv1.Function) (*fscache.FuncSvc, error)
	updateFunction(context.Context, *fv1.Function, *fv1.Function) error
	deleteFunction(context.Context, *fv1.Function) error
	// resourcesExist reports whether the function's backing Deployment and Service
	// are present (read from the Manager cache). False means they drifted away
	// (deleted out-of-band) and the function should be recreated.
	resourcesExist(context.Context, *fv1.Function) (bool, error)
}

// ReconcileFunction satisfies executortype.FuncReconciler for container-backed
// functions (Deployment/Service/HPA). The shared Function reconciler owns the
// last-reconciled cache and executor-type transitions, so this only sees same-type
// create (old == nil → createFunction) and update (old != nil →
// updateFunction(old, fn), which diffs HPA min/max/metrics and secret/configmap
// changes).
func (caaf *Container) ReconcileFunction(ctx context.Context, old, fn *fv1.Function) error {
	return reconcileContainerFunc(ctx, caaf, old, fn)
}

// DeleteFunction satisfies executortype.FuncReconciler: it tears down the
// function's Deployment/Service/HPA.
func (caaf *Container) DeleteFunction(ctx context.Context, fn *fv1.Function) error {
	return caaf.deleteFunction(ctx, fn)
}

// reconcileContainerFunc holds the create-vs-update routing, split out so it is
// unit-testable with a fake functionManager. It is level-triggered: a reconcile
// of a managed function whose backing Deployment/Service drifted away (deleted
// out-of-band, surfaced by the drift watch) recreates them via the idempotent
// get-or-create path rather than diffing a no-longer-existent object.
func reconcileContainerFunc(ctx context.Context, mgr functionManager, old, fn *fv1.Function) error {
	if old == nil {
		_, err := mgr.createFunction(ctx, fn)
		return err
	}
	exist, err := mgr.resourcesExist(ctx, fn)
	if err != nil {
		return err
	}
	if !exist {
		_, err := mgr.createFunction(ctx, fn)
		return err
	}
	return mgr.updateFunction(ctx, old, fn)
}

// RegisterReconcilers registers no type-specific watches: the container type's
// Function reconciles are handled by the shared executor-level Function
// reconciler (see funcreconciler.RegisterReconciler), which it plugs into via
// FuncReconciler. It captures the Manager's cache-backed client for IsValid's
// Deployment/Service reads (replacing the per-type informer factory).
func (caaf *Container) RegisterReconcilers(mgr ctrl.Manager) error {
	caaf.crClient = mgr.GetClient()
	return nil
}

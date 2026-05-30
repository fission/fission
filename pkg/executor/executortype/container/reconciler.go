// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"context"
	"sync"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/executor/fscache"
)

// functionManager is the subset of *Container the reconciler drives. Defined as
// an interface so the reconcile routing (create/update/delete based on the
// last-reconciled object) is unit-testable with a fake.
type functionManager interface {
	createFunction(context.Context, *fv1.Function) (*fscache.FuncSvc, error)
	updateFunction(context.Context, *fv1.Function, *fv1.Function) error
	deleteFunction(context.Context, *fv1.Function) error
}

// functionReconciler manages container-backed function deployments. It replaces
// the Function informer event handlers: controller-runtime's workqueue (with
// bounded MaxConcurrentReconciles and per-key serialization) is the reconciler,
// replacing the unbounded bare goroutines the old handlers spawned — addressing
// the code's own "should use a workqueue" TODO.
//
// updateFunction is diff-based (it compares old vs new for HPA min/max/metrics,
// secret/configmap changes, and executor-type transitions), but a reconciler
// only has the current Function. So the reconciler keeps the last-reconciled
// Function per key to supply the "old" object: Reconcile calls createFunction on
// first sight, updateFunction(old, current) thereafter, and deleteFunction(old)
// when the Function is gone. It is registered with GenerationChangedPredicate, so
// the executor's own status writes (which leave Generation unchanged) don't churn
// the loop — they were no-ops through updateFunction anyway.
type functionReconciler struct {
	logger logr.Logger
	client client.Client
	caaf   functionManager
	// lastReconciled maps client.ObjectKey -> *fv1.Function (the object as last
	// reconciled), supplying the "old" object updateFunction diffs against.
	lastReconciled sync.Map
}

func (r *functionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fn := &fv1.Function{}
	if err := r.client.Get(ctx, req.NamespacedName, fn); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted: clean up using the last-reconciled object (the live one is gone).
			if old, ok := r.lastReconciled.LoadAndDelete(req.NamespacedName); ok {
				if err := r.caaf.deleteFunction(ctx, old.(*fv1.Function)); err != nil {
					return ctrl.Result{}, err
				}
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	old, seen := r.lastReconciled.Load(req.NamespacedName)
	if !seen {
		// First sight. Only manage functions of our executor type — createFunction
		// is reached only for container (and unset) types, matching the old
		// AddFunc filter. A function that later switches *to* container generates a
		// spec change and arrives here uncached, so it is created then too.
		if !isContainerType(fn) {
			return ctrl.Result{}, nil
		}
		if _, err := r.caaf.createFunction(ctx, fn); err != nil {
			return ctrl.Result{}, err
		}
		r.lastReconciled.Store(req.NamespacedName, fn.DeepCopy())
		return ctrl.Result{}, nil
	}

	// Already managing it: updateFunction handles spec/HPA diffs and executor-type
	// transitions (it deletes the resources if the function is no longer container).
	if err := r.caaf.updateFunction(ctx, old.(*fv1.Function), fn); err != nil {
		return ctrl.Result{}, err
	}
	if isContainerType(fn) {
		r.lastReconciled.Store(req.NamespacedName, fn.DeepCopy())
	} else {
		// Transitioned away from container; updateFunction cleaned up the resources.
		r.lastReconciled.Delete(req.NamespacedName)
	}
	return ctrl.Result{}, nil
}

// isContainerType reports whether the executor should manage this function.
// Mirrors the old handler filter: an unset executor type is treated as managed
// (the function falls back to this type), anything else is skipped.
func isContainerType(fn *fv1.Function) bool {
	t := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
	return t == "" || t == fv1.ExecutorTypeContainer
}

// RegisterReconcilers registers the container Function reconciler on the executor
// Manager.
func (caaf *Container) RegisterReconcilers(mgr ctrl.Manager) error {
	r := &functionReconciler{
		logger: caaf.logger.WithName("function_reconciler"),
		client: mgr.GetClient(),
		caaf:   caaf,
	}
	return controller.Register(mgr, &fv1.Function{}, r, "container-function")
}

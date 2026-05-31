// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

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

// funcManager is the subset of *NewDeploy the Function reconciler drives. Defined
// as an interface so the reconcile routing (create/update/delete based on the
// last-reconciled object) is unit-testable with a fake.
type funcManager interface {
	createFunction(context.Context, *fv1.Function) (*fscache.FuncSvc, error)
	updateFunction(context.Context, *fv1.Function, *fv1.Function) error
	deleteFunction(context.Context, *fv1.Function) error
}

// envFunctionUpdater is the subset of *NewDeploy the Environment reconciler
// drives — propagating an environment image change to its functions' deployments.
type envFunctionUpdater interface {
	updateEnvFunctions(context.Context, *fv1.Environment) error
}

// functionReconciler manages newdeploy-backed function Deployments/Services/HPAs.
// It replaces the Function informer event handlers: controller-runtime's
// workqueue (bounded concurrency, per-key serialization) is the reconciler,
// replacing the unbounded bare goroutines the old handlers spawned — addressing
// the code's own "should use a workqueue" TODO.
//
// updateFunction is diff-based (it compares old vs new for HPA min/max/metrics,
// secret/configmap/package changes, and executor-type transitions), but a
// reconciler only has the current Function. So the reconciler keeps the
// last-reconciled Function per key to supply the "old" object: Reconcile calls
// createFunction on first sight, updateFunction(old, current) thereafter, and
// deleteFunction(old) when the Function is gone. Registered with
// GenerationChangedPredicate, so the executor's own status writes (Generation
// unchanged) don't churn the loop — they were no-ops through updateFunction anyway.
type functionReconciler struct {
	logger logr.Logger
	client client.Client
	deploy funcManager
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
				if err := r.deploy.deleteFunction(ctx, old.(*fv1.Function)); err != nil {
					return ctrl.Result{}, err
				}
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	old, seen := r.lastReconciled.Load(req.NamespacedName)
	if !seen {
		// First sight. Only manage functions whose executor type is newdeploy
		// (createFunction is a no-op for any other type, so caching them would just
		// grow lastReconciled). A function that later switches *to* newdeploy
		// generates a spec change and arrives here uncached, so it is created then.
		if !isNewdeployType(fn) {
			return ctrl.Result{}, nil
		}
		if _, err := r.deploy.createFunction(ctx, fn); err != nil {
			return ctrl.Result{}, err
		}
		r.lastReconciled.Store(req.NamespacedName, fn.DeepCopy())
		return ctrl.Result{}, nil
	}

	// Already managing it: updateFunction handles spec/HPA diffs and executor-type
	// transitions (it deletes the resources if the function is no longer newdeploy).
	if err := r.deploy.updateFunction(ctx, old.(*fv1.Function), fn); err != nil {
		return ctrl.Result{}, err
	}
	if isNewdeployType(fn) {
		r.lastReconciled.Store(req.NamespacedName, fn.DeepCopy())
	} else {
		// Transitioned away from newdeploy; updateFunction cleaned up the resources.
		r.lastReconciled.Delete(req.NamespacedName)
	}
	return ctrl.Result{}, nil
}

// environmentReconciler propagates an environment's runtime-image change to the
// deployments of its newdeploy functions. It replaces the Environment informer
// handler, which acted only on an image change (Add/Delete were no-ops).
//
// The handler diffed old vs new image, so the reconciler keeps the last-reconciled
// Environment per key: first sight only caches (matching the old no-op Add), and a
// later reconcile rebuilds the function deployments only when the image actually
// changed. GenerationChangedPredicate keeps env status writes from churning it.
type environmentReconciler struct {
	logger logr.Logger
	client client.Client
	deploy envFunctionUpdater
	// lastReconciled maps client.ObjectKey -> *fv1.Environment.
	lastReconciled sync.Map
}

func (r *environmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	env := &fv1.Environment{}
	if err := r.client.Get(ctx, req.NamespacedName, env); err != nil {
		if apierrors.IsNotFound(err) {
			// Environment deleted; the old handler did nothing here (function cleanup
			// is driven by the Function watch), so just drop the cached copy.
			r.lastReconciled.Delete(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	old, seen := r.lastReconciled.Load(req.NamespacedName)
	if !seen {
		// First sight: cache only, matching the old AddFunc no-op. We have no prior
		// image to diff against, and functions already track their environment.
		r.lastReconciled.Store(req.NamespacedName, env.DeepCopy())
		return ctrl.Result{}, nil
	}

	if old.(*fv1.Environment).Spec.Runtime.Image != env.Spec.Runtime.Image {
		if err := r.deploy.updateEnvFunctions(ctx, env); err != nil {
			// Don't advance the cache: a retry should still see the image as changed.
			return ctrl.Result{}, err
		}
	}
	r.lastReconciled.Store(req.NamespacedName, env.DeepCopy())
	return ctrl.Result{}, nil
}

// isNewdeployType reports whether the executor should manage this function.
// createFunction is a no-op unless the executor type is exactly newdeploy, so we
// only cache/manage those — caching other-type functions (which the old handler
// passed but createFunction ignored) would grow lastReconciled without ever
// creating resources.
func isNewdeployType(fn *fv1.Function) bool {
	return fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeNewdeploy
}

// RegisterReconcilers registers the newdeploy Function and Environment reconcilers
// on the executor Manager, replacing their informer event handlers.
func (deploy *NewDeploy) RegisterReconcilers(mgr ctrl.Manager) error {
	fr := &functionReconciler{
		logger: deploy.logger.WithName("function_reconciler"),
		client: mgr.GetClient(),
		deploy: deploy,
	}
	if err := controller.Register(mgr, &fv1.Function{}, fr, "newdeploy-function"); err != nil {
		return err
	}

	er := &environmentReconciler{
		logger: deploy.logger.WithName("environment_reconciler"),
		client: mgr.GetClient(),
		deploy: deploy,
	}
	return controller.Register(mgr, &fv1.Environment{}, er, "newdeploy-environment")
}

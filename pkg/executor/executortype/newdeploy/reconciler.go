// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"context"
	"sync"
	"time"

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
	reconcileDeploymentSpec(context.Context, *fv1.Function) error
}

// envFunctionUpdater is the subset of *NewDeploy the Environment handler drives —
// propagating an environment image change to its functions' deployments. An
// interface so the routing is unit-testable with a fake.
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
		// createFunction only adopts/scales an existing deployment. If this first
		// reconcile coalesced the create with a later spec update (e.g. the router
		// specialized the function on-demand before `fn update` landed), the adopted
		// deployment can be stale with no transition for updateFunction to diff —
		// bring it to the current spec. A no-op when already current.
		if err := r.deploy.reconcileDeploymentSpec(ctx, fn); err != nil {
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

// ReconcileEnvironment satisfies executortype.EnvReconciler: it propagates an
// environment's runtime-image change to the deployments of its newdeploy
// functions. It acts only on an image change (the informer handler it replaced
// treated Add/Delete as no-ops), so first sight (old == nil) caches only and a
// later event rolls the functions only when the image actually changed. It asks
// for no periodic requeue (0); the shared dispatcher's requeue is driven by the
// pool manager's resync.
func (deploy *NewDeploy) ReconcileEnvironment(ctx context.Context, old, env *fv1.Environment) (time.Duration, error) {
	return reconcileNewdeployEnv(ctx, deploy, old, env)
}

// CleanupEnvironment is a no-op: function cleanup is driven by the Function watch,
// so a deleted Environment needs no newdeploy-side action (matching the old
// handler, which did nothing on delete).
func (deploy *NewDeploy) CleanupEnvironment(context.Context, *fv1.Environment) {}

// reconcileNewdeployEnv holds the image-diff routing, split out so it is
// unit-testable with a fake envFunctionUpdater.
func reconcileNewdeployEnv(ctx context.Context, up envFunctionUpdater, old, env *fv1.Environment) (time.Duration, error) {
	if old == nil {
		// First sight: nothing to diff against, and functions already track their
		// environment. Matches the old AddFunc no-op.
		return 0, nil
	}
	if old.Spec.Runtime.Image != env.Spec.Runtime.Image {
		if err := up.updateEnvFunctions(ctx, env); err != nil {
			return 0, err
		}
	}
	return 0, nil
}

// isNewdeployType reports whether the executor should manage this function.
// createFunction is a no-op unless the executor type is exactly newdeploy, so we
// only cache/manage those — caching other-type functions (which the old handler
// passed but createFunction ignored) would grow lastReconciled without ever
// creating resources.
func isNewdeployType(fn *fv1.Function) bool {
	return fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeNewdeploy
}

// RegisterReconcilers registers the newdeploy Function reconciler on the executor
// Manager, replacing its informer event handler. The Environment watch is shared
// across executor types and registered once at the executor level (see
// envreconciler.RegisterReconciler); newdeploy plugs into it via EnvReconciler.
func (deploy *NewDeploy) RegisterReconcilers(mgr ctrl.Manager) error {
	fr := &functionReconciler{
		logger: deploy.logger.WithName("function_reconciler"),
		client: mgr.GetClient(),
		deploy: deploy,
	}
	return controller.Register(mgr, &fv1.Function{}, fr, "newdeploy-function")
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

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
	"github.com/fission/fission/pkg/crd"
)

// envResyncPeriod re-reconciles each Environment periodically so the pool
// deployment is kept in sync even without a spec change — replacing the 30m
// informer resync the env workqueue used to get.
const envResyncPeriod = 30 * time.Minute

// funcManager is the subset of *GenericPoolManager the Function reconciler drives.
// An interface so the reconcile routing is unit-testable with a fake.
type funcManager interface {
	markFuncDeleted(crd.CacheKeyURG)
	refreshFuncPods(ctx context.Context, fn *fv1.Function) error
	createIstioServiceForFunction(ctx context.Context, fn *fv1.Function) error
	deleteIstioServiceForFunction(ctx context.Context, fn *fv1.Function) error
}

// poolManager is the subset of *GenericPoolManager the Environment reconciler
// drives (pool lifecycle), also an interface for testing.
type poolManager interface {
	reconcileEnvPool(ctx context.Context, env *fv1.Environment) error
	cleanupEnvPool(ctx context.Context, env *fv1.Environment)
}

// isPoolmgrType reports whether the pool manager should handle this function.
// Poolmgr is the default executor, so an empty executor type counts as poolmgr —
// matching the old istio/handler filter (`type != "" && type != poolmgr → skip`).
func isPoolmgrType(fn *fv1.Function) bool {
	t := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
	return t == "" || t == fv1.ExecutorTypePoolmgr
}

// functionReconciler manages the pool manager's per-Function concerns, replacing
// poolpodcontroller's istio Function handler and #3432's delete-only reconciler:
//
//   - create: create the per-function istio Service (when istio is enabled).
//   - update: refresh (delete) the function's specialized pods so the next
//     request re-specializes a warm pod with the new package/config. Poolmgr had
//     no function-update path before, so a stale specialized pod (old package)
//     could keep being routed to until the idle reaper recycled it.
//   - delete: mark the fsCache entries deleted (so the reaper recycles pods) and
//     remove the istio Service.
//
// updateFunction-style diffing is unnecessary: GenerationChangedPredicate means a
// reconcile of a cached function is always a spec change, and the istio Service is
// keyed on the (stable) function name/uid so it only needs create/delete. The
// last-seen Function supplies the object for cleanup once the live one is gone.
type functionReconciler struct {
	logger      logr.Logger
	client      client.Client
	mgr         funcManager
	enableIstio bool
	lastSeen    sync.Map // client.ObjectKey -> *fv1.Function
}

func (r *functionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fn := &fv1.Function{}
	if err := r.client.Get(ctx, req.NamespacedName, fn); err != nil {
		if apierrors.IsNotFound(err) {
			if old, ok := r.lastSeen.LoadAndDelete(req.NamespacedName); ok {
				if err := r.cleanupFunction(ctx, old.(*fv1.Function)); err != nil {
					return ctrl.Result{}, err
				}
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	old, seen := r.lastSeen.Load(req.NamespacedName)
	if !seen {
		// First sight. Only manage poolmgr-type functions.
		if !isPoolmgrType(fn) {
			return ctrl.Result{}, nil
		}
		if r.enableIstio {
			if err := r.mgr.createIstioServiceForFunction(ctx, fn); err != nil {
				return ctrl.Result{}, err
			}
		}
		r.lastSeen.Store(req.NamespacedName, fn.DeepCopy())
		return ctrl.Result{}, nil
	}

	oldFn := old.(*fv1.Function)
	// A delete+recreate of the same name, or a switch away from poolmgr, can
	// coalesce into one reconcile (the workqueue carries only namespace/name).
	// If the cached function is no longer the one we should manage, clean it up.
	if oldFn.UID != fn.UID || !isPoolmgrType(fn) {
		if err := r.cleanupFunction(ctx, oldFn); err != nil {
			return ctrl.Result{}, err
		}
		if !isPoolmgrType(fn) {
			r.lastSeen.Delete(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		// New incarnation is still poolmgr: treat as a fresh create.
		if r.enableIstio {
			if err := r.mgr.createIstioServiceForFunction(ctx, fn); err != nil {
				return ctrl.Result{}, err
			}
		}
		r.lastSeen.Store(req.NamespacedName, fn.DeepCopy())
		return ctrl.Result{}, nil
	}

	// Genuine spec change of a managed function: re-specialize its pods so the new
	// package/config is served. The istio Service is keyed on the stable function
	// name/uid, so it needs no change here.
	if err := r.mgr.refreshFuncPods(ctx, fn); err != nil {
		return ctrl.Result{}, err
	}
	r.lastSeen.Store(req.NamespacedName, fn.DeepCopy())
	return ctrl.Result{}, nil
}

// cleanupFunction marks the function's fsCache entries deleted and removes its
// istio Service (when enabled).
func (r *functionReconciler) cleanupFunction(ctx context.Context, fn *fv1.Function) error {
	r.mgr.markFuncDeleted(crd.CacheKeyURGFromMeta(&fn.ObjectMeta))
	if r.enableIstio {
		if err := r.mgr.deleteIstioServiceForFunction(ctx, fn); err != nil {
			return err
		}
	}
	return nil
}

// environmentReconciler manages the warm pool for each Environment, replacing
// poolpodcontroller's env create/update/delete workqueues. It drives the gpm
// actor (getPool/cleanupPool/updatePoolDeployment) — which stays as the
// serializer shared with the hot GetFuncSvc path — and keeps the last-seen
// Environment so a delete can still destroy the pool and reap specialized pods.
type environmentReconciler struct {
	logger   logr.Logger
	client   client.Client
	mgr      poolManager
	lastSeen sync.Map // client.ObjectKey -> *fv1.Environment
}

func (r *environmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	env := &fv1.Environment{}
	if err := r.client.Get(ctx, req.NamespacedName, env); err != nil {
		if apierrors.IsNotFound(err) {
			if old, ok := r.lastSeen.LoadAndDelete(req.NamespacedName); ok {
				r.mgr.cleanupEnvPool(ctx, old.(*fv1.Environment))
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if err := r.mgr.reconcileEnvPool(ctx, env); err != nil {
		return ctrl.Result{}, err
	}
	r.lastSeen.Store(req.NamespacedName, env.DeepCopy())
	// Periodically re-reconcile to keep the pool deployment in sync even without a
	// spec change (the old env workqueue got this from the 30m informer resync).
	return ctrl.Result{RequeueAfter: envResyncPeriod}, nil
}

// RegisterReconcilers registers the pool manager's Function and Environment
// reconcilers on the executor Manager, replacing poolpodcontroller's Function and
// Environment informer handlers. The ReplicaSet → specialized-pod-cleanup watch
// stays on poolpodcontroller (k8s pod machinery).
func (gpm *GenericPoolManager) RegisterReconcilers(mgr ctrl.Manager) error {
	// getFunctionEnv (hot path) reads Environments from the Manager cache instead
	// of poolpodcontroller's informer lister now.
	gpm.crClient = mgr.GetClient()

	fr := &functionReconciler{
		logger:      gpm.logger.WithName("function_reconciler"),
		client:      mgr.GetClient(),
		mgr:         gpm,
		enableIstio: gpm.enableIstio,
	}
	if err := controller.Register(mgr, &fv1.Function{}, fr, "poolmgr-function"); err != nil {
		return err
	}

	er := &environmentReconciler{
		logger: gpm.logger.WithName("environment_reconciler"),
		client: mgr.GetClient(),
		mgr:    gpm,
	}
	return controller.Register(mgr, &fv1.Environment{}, er, "poolmgr-environment")
}

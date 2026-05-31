// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"sync"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

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

// rsCleaner is the subset of *GenericPoolManager the ReplicaSet reconciler drives.
type rsCleaner interface {
	processReplicaSet(ctx context.Context, rs *appsv1.ReplicaSet)
}

// replicaSetReconciler reaps a pool's specialized pods when its ReplicaSet scales
// to zero (the pool was destroyed). It replaces poolpodcontroller's ReplicaSet
// informer handler. processReplicaSet is a no-op unless replicas == 0, and the
// scale-to-zero is a spec change (caught by GenerationChangedPredicate) that
// always precedes the ReplicaSet's deletion, so a NotFound needs no handling.
type replicaSetReconciler struct {
	logger  logr.Logger
	client  client.Client
	cleaner rsCleaner
}

func (r *replicaSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	rs := &appsv1.ReplicaSet{}
	if err := r.client.Get(ctx, req.NamespacedName, rs); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	r.cleaner.processReplicaSet(ctx, rs)
	return ctrl.Result{}, nil
}

// poolmgrReplicaSetPredicate keeps the ReplicaSet reconciler to pool-manager
// ReplicaSets only (the executor cache also sees newdeploy/container ones), the
// same filter the old executor-labelled informer applied.
var poolmgrReplicaSetPredicate = predicate.NewPredicateFuncs(func(obj client.Object) bool {
	return obj.GetLabels()[fv1.EXECUTOR_TYPE] == string(fv1.ExecutorTypePoolmgr)
})

// readyPodEnqueuer is the subset of *GenericPoolManager the readyPod reconciler
// drives — adding a warm pod's key to its pool's readyPodQueue.
type readyPodEnqueuer interface {
	enqueueReadyPod(envUID, key string)
}

// readyPodReconciler feeds warm (unspecialized, Running) pool pods into their
// pool's readyPodQueue, which choosePod consumes on cold start. It replaces the
// per-pool readyPod informer with a single Manager-cache Pod watch, routing each
// pod to the right pool by its environment UID label. It is purely additive: a
// specialized or deleted pod needs no handling here because choosePod skips any
// queue entry that is no longer a warm pod.
type readyPodReconciler struct {
	logger   logr.Logger
	client   client.Client
	enqueuer readyPodEnqueuer
}

func (r *readyPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pod := &apiv1.Pod{}
	if err := r.client.Get(ctx, req.NamespacedName, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	// Only feed warm, Running pods. The predicate already filters managed=true, but
	// re-check defensively and gate on Running (the old informer used a
	// status.phase=Running field selector).
	if pod.Labels["managed"] != "true" || pod.Status.Phase != apiv1.PodRunning {
		return ctrl.Result{}, nil
	}
	envUID := pod.Labels[fv1.ENVIRONMENT_UID]
	if envUID == "" {
		return ctrl.Result{}, nil
	}
	r.enqueuer.enqueueReadyPod(envUID, req.String())
	return ctrl.Result{}, nil
}

// poolmgrWarmPodPredicate keeps the readyPod reconciler to pool-manager warm
// pods (managed=true), matching what the old per-pool informer watched.
var poolmgrWarmPodPredicate = predicate.NewPredicateFuncs(func(obj client.Object) bool {
	l := obj.GetLabels()
	return l[fv1.EXECUTOR_TYPE] == string(fv1.ExecutorTypePoolmgr) && l["managed"] == "true"
})

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

// RegisterReconcilers registers the pool manager's Function, Environment, and
// ReplicaSet reconcilers on the executor Manager, replacing poolpodcontroller's
// informer handlers. poolpodcontroller now only owns the specialized-pod cleanup
// queue (fed by the ReplicaSet reconciler and the Environment reconciler) and the
// pod lister.
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
	if err := controller.Register(mgr, &fv1.Environment{}, er, "poolmgr-environment"); err != nil {
		return err
	}

	rr := &replicaSetReconciler{
		logger:  gpm.logger.WithName("replicaset_reconciler"),
		client:  mgr.GetClient(),
		cleaner: gpm,
	}
	if err := controller.RegisterWithPredicates(mgr, &appsv1.ReplicaSet{}, rr, "poolmgr-replicaset", 0,
		poolmgrReplicaSetPredicate, predicate.GenerationChangedPredicate{}); err != nil {
		return err
	}

	// The readyPod reconciler reacts to pod status (phase → Running), so it must
	// NOT use GenerationChangedPredicate (status changes leave Generation
	// unchanged) — only the warm-pod label filter.
	pr := &readyPodReconciler{
		logger:   gpm.logger.WithName("readypod_reconciler"),
		client:   mgr.GetClient(),
		enqueuer: gpm,
	}
	return controller.RegisterWithPredicates(mgr, &apiv1.Pod{}, pr, "poolmgr-readypod", 0,
		poolmgrWarmPodPredicate)
}

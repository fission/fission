// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sTypes "k8s.io/apimachinery/pkg/types"
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

// funcManager is the subset of *GenericPoolManager the Function handlers drive.
// An interface so the reconcile routing is unit-testable with a fake.
type funcManager interface {
	markFuncDeleted(crd.CacheKeyUG)
	refreshFuncPods(ctx context.Context, fn *fv1.Function) error
	createIstioServiceForFunction(ctx context.Context, fn *fv1.Function) error
	deleteIstioServiceForFunction(ctx context.Context, fn *fv1.Function) error
	deleteFunctionService(ctx context.Context, fn *fv1.Function) error
}

// poolManager is the subset of *GenericPoolManager the Environment handlers
// drive (pool lifecycle), an interface so the routing is unit-testable with a fake.
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
	enqueueReadyPod(queueKey, podKey string)
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
	// Per-image (Path B) pool pods carry the image-hash label; route them to
	// their own pool's queue. Absent label -> the env's plain pool.
	imageHash := pod.Labels[fv1.POOL_OCI_IMAGE_HASH]
	r.enqueuer.enqueueReadyPod(poolKey(k8sTypes.UID(envUID), imageHash), req.String())
	return ctrl.Result{}, nil
}

// poolmgrWarmPodPredicate keeps the readyPod reconciler to pool-manager warm
// pods (managed=true), matching what the old per-pool informer watched.
var poolmgrWarmPodPredicate = predicate.NewPredicateFuncs(func(obj client.Object) bool {
	l := obj.GetLabels()
	return l[fv1.EXECUTOR_TYPE] == string(fv1.ExecutorTypePoolmgr) && l["managed"] == "true"
})

// ReconcileFunction satisfies executortype.FuncReconciler for the pool manager:
//
//   - create (old == nil): create the per-function istio Service (when istio is
//     enabled). Pool pods themselves are specialized lazily by GetFuncSvc.
//   - update (old != nil): refresh (delete) the function's specialized pods so the
//     next request re-specializes a warm pod with the new package/config. Poolmgr
//     had no function-update path before, so a stale specialized pod (old package)
//     could keep being routed to until the idle reaper recycled it.
//
// No old/new diffing is needed: a reconcile of a cached function is always a spec
// change (GenerationChangedPredicate), and the istio Service is keyed on the
// stable function name/uid, so it only needs create/delete.
func (gpm *GenericPoolManager) ReconcileFunction(ctx context.Context, old, fn *fv1.Function) error {
	err := reconcilePoolmgrFunc(ctx, gpm, gpm.enableIstio, old, fn)
	if err != nil {
		return err
	}
	if gpm.provisioner != nil {
		if fn.Spec.ProvisionedConcurrency != nil {
			gpm.provisioner.reconcileFunction(ctx, fn)
		} else {
			// Provisioned concurrency was removed (e.g. --provisioned-concurrency 0):
			// clear labels on any previously-provisioned pods and reset status.
		} else if old != nil && old.Spec.ProvisionedConcurrency != nil {
			// Provisioned concurrency was removed (e.g. --provisioned-concurrency 0):
			// clear labels on any previously-provisioned pods and reset status.
			gpm.provisioner.disableProvisioning(ctx, fn)
		}
		}
	}
	return nil
}

// DeleteFunction satisfies executortype.FuncReconciler: it marks the function's
// fsCache entries deleted (so the reaper recycles its pods) and removes its istio
// Service (when enabled) or its headless function Service (RFC-0002, when enabled).
func (gpm *GenericPoolManager) DeleteFunction(ctx context.Context, fn *fv1.Function) error {
	if gpm.provisioner != nil {
		// Function is being deleted: clear provisioned labels so the reaper
		// can recycle the pods. No status write — the Function object is
		// being removed, so a status update would race with the delete and
		// is pointless.
		gpm.provisioner.clearProvisionedLabels(ctx, fn, -1)
	}
	return cleanupPoolmgrFunc(ctx, gpm, gpm.enableIstio, gpm.functionServicesEnabled, fn)
}

// reconcilePoolmgrFunc holds the create-vs-update routing, split out so it is
// unit-testable with a fake funcManager.
func reconcilePoolmgrFunc(ctx context.Context, mgr funcManager, enableIstio bool, old, fn *fv1.Function) error {
	if old == nil {
		if enableIstio {
			return mgr.createIstioServiceForFunction(ctx, fn)
		}
		return nil
	}
	return mgr.refreshFuncPods(ctx, fn)
}

// cleanupPoolmgrFunc holds the teardown routing, split out for unit testing.
func cleanupPoolmgrFunc(ctx context.Context, mgr funcManager, enableIstio, functionServices bool, fn *fv1.Function) error {
	mgr.markFuncDeleted(crd.CacheKeyUGFromMeta(&fn.ObjectMeta))
	if enableIstio {
		return mgr.deleteIstioServiceForFunction(ctx, fn)
	}
	if functionServices {
		// The owner reference covers same-namespace installs; this covers
		// cross-namespace ones and is idempotent either way.
		return mgr.deleteFunctionService(ctx, fn)
	}
	return nil
}

// ReconcileEnvironment satisfies executortype.EnvReconciler: the pool manager
// re-reconciles the Environment's warm pool on every event, driving the gpm
// actor (getPool/cleanupPool/updatePoolDeployment) which stays as the serializer
// shared with the hot GetFuncSvc path. old is ignored — the pool reconcile is
// idempotent, so there is nothing to diff. It asks for a periodic re-reconcile so
// the pool deployment stays in sync even without a spec change (the env workqueue
// used to get this from the 30m informer resync).
func (gpm *GenericPoolManager) ReconcileEnvironment(ctx context.Context, _, env *fv1.Environment) (time.Duration, error) {
	return reconcilePoolmgrEnv(ctx, gpm, env)
}

// CleanupEnvironment destroys the warm pool when its Environment is deleted (and
// reaps the specialized pods), using the last-seen object handed by the dispatcher.
func (gpm *GenericPoolManager) CleanupEnvironment(ctx context.Context, env *fv1.Environment) {
	gpm.cleanupEnvPool(ctx, env)
}

// reconcilePoolmgrEnv holds the pool-reconcile routing, split out so it is
// unit-testable with a fake poolManager.
func reconcilePoolmgrEnv(ctx context.Context, pm poolManager, env *fv1.Environment) (time.Duration, error) {
	if err := pm.reconcileEnvPool(ctx, env); err != nil {
		return 0, err
	}
	return envResyncPeriod, nil
}

// RegisterReconcilers registers the pool manager's ReplicaSet and readyPod
// reconcilers on the executor Manager, replacing poolpodcontroller's informer
// handlers. The Function and Environment watches are shared across executor types
// and registered once at the executor level (see funcreconciler/envreconciler
// RegisterReconciler); the pool manager plugs into them via FuncReconciler and
// EnvReconciler. poolpodcontroller now only owns the specialized-pod cleanup
// queue (fed by the ReplicaSet reconciler and CleanupEnvironment) and the pod
// lister.
func (gpm *GenericPoolManager) RegisterReconcilers(mgr ctrl.Manager) error {
	// getFunctionEnv (hot path) reads Environments from the Manager cache instead
	// of poolpodcontroller's informer lister now.
	gpm.crClient = mgr.GetClient()

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

	// RFC-0026 provisioner: construct after crClient is set so it can
	// list Functions and Pods from the shared cache. Env-var config is
	// wired in Step 9; defaults are applied in NewProvisioner.
	cfg, ok, err := ProvisionerConfigFromEnv()
	if err != nil {
		gpm.logger.Error(err, "EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED is set but unparseable; provisioned concurrency disabled")
		gpm.provisioner = nil
	} else if !ok {
		gpm.provisioner = nil // feature off
	} else {
		gpm.provisioner = NewProvisioner(gpm.logger.WithName("provisioner"), gpm, gpm.fissionClient, gpm.kubernetesClient, gpm.crClient, cfg)
	}
	return controller.RegisterWithPredicates(mgr, &apiv1.Pod{}, pr, "poolmgr-readypod", 0,
		poolmgrWarmPodPredicate)
}

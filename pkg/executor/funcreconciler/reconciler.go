// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package funcreconciler holds the executor's single Function reconciler. It
// replaces the per-executor-type Function reconcilers (poolmgr, newdeploy,
// container) with one reconciler that resolves each Function's executor type and
// dispatches the create/update/delete to the owning type via
// executortype.FuncReconciler. Sharing one reconciler means one Function
// workqueue, one predicate evaluation per event, and one last-reconciled cache
// instead of one set per executor type — and it makes executor-type transitions
// atomic: the same reconcile tears the old type down and builds the new, where
// previously two independent reconcilers had to converge.
package funcreconciler

import (
	"context"
	"sort"
	"sync"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/executortype"
)

// deleteOnlyPredicate passes only Delete events. The drift watch uses it so a
// Function is re-enqueued when one of its backing objects is removed out-of-band,
// but NOT on Create (the executor creates them itself) or Update — crucially, the
// newdeploy idle reaper *scales* the Deployment toward MinScale (it never deletes
// it), so reacting to Updates would fight the reaper and churn the workqueue.
var deleteOnlyPredicate = predicate.Funcs{
	CreateFunc:  func(event.CreateEvent) bool { return false },
	UpdateFunc:  func(event.UpdateEvent) bool { return false },
	DeleteFunc:  func(event.DeleteEvent) bool { return true },
	GenericFunc: func(event.GenericEvent) bool { return false },
}

// ownedObjectToFunction maps a managed Deployment/Service back to the Function
// that owns it, via the function-identifying labels getDeployLabels stamps. The
// labels carry the Function CR's own name/namespace (not the workload namespace),
// so the request points straight at the Function. Returns nil for an object
// without them (it is not ours).
func ownedObjectToFunction(_ context.Context, obj client.Object) []reconcile.Request {
	l := obj.GetLabels()
	name, ns := l[fv1.FUNCTION_NAME], l[fv1.FUNCTION_NAMESPACE]
	if name == "" || ns == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}}
}

// functionFinalizer gates a Function's deletion on the executor tearing its
// backing workloads down. It is the reliable mechanism for cross-namespace
// cleanup: owner-reference GC cannot reach a Deployment/Service/HPA in a
// different namespace than the Function CR (see GetFunctionNS), so the executor
// must observe the delete and tear them down before the object is collected.
// Toggled by the chart-wide finalizerEnabled value (default on); when off, the
// finalizer is drained from any object that carries it so deletes never wedge.
const functionFinalizer = "fission.io/function-cleanup"

// deletionTimestampPredicate passes Update events where the object is being
// deleted (DeletionTimestamp set). The shared GenerationChangedPredicate drops
// these — setting DeletionTimestamp leaves Generation unchanged — which would
// leave a finalizer-held Function unreconciled and wedge its delete. OR'd with
// GenerationChangedPredicate so spec changes still trigger and status-only writes
// are still filtered.
var deletionTimestampPredicate = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		return e.ObjectNew != nil && !e.ObjectNew.GetDeletionTimestamp().IsZero()
	},
}

// funcReconcileConcurrency is the shared Function reconciler's worker count. A
// reconcile can block for up to the specialization timeout: createFunction
// (first sight, or recreating a drifted-away workload) waits in waitForDeploy for
// the Deployment to become available. With a single worker that wait would
// head-of-line-block every other function's reconcile — and merging the three
// per-type reconcilers (each previously its own 1-worker controller, so a
// blocking newdeploy create never stalled container/poolmgr) into one collapsed
// that cross-type isolation. Several workers restore it; controller-runtime still
// serializes reconciles per Function key, so per-function state stays safe.
// Sized above the integration suite's parallelism (-parallel 6) so several
// concurrent first-sight/drift creates can be in their waitForDeploy wait
// without starving unrelated functions' updates.
const funcReconcileConcurrency = 10

// resolveExecutorType returns the executor type that owns fn. An unset type
// defaults to poolmgr (the default executor), matching the old per-type filters
// where an empty type counted as poolmgr.
func resolveExecutorType(fn *fv1.Function) fv1.ExecutorType {
	if t := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType; t != "" {
		return t
	}
	return fv1.ExecutorTypePoolmgr
}

// functionReconciler dispatches Function events to the executor type that owns
// each Function. It owns the last-reconciled Function per key, which supplies the
// "old" object backends diff against on update, the previous executor type on a
// transition, and the object to tear down on delete.
type functionReconciler struct {
	logger         logr.Logger
	client         client.Client
	backends       map[fv1.ExecutorType]executortype.FuncReconciler
	finalizer      bool     // add+honour the cleanup finalizer (opt-in)
	lastReconciled sync.Map // client.ObjectKey -> *fv1.Function
}

// updateFinalizerWithRetry re-reads the Function and re-applies mutate under
// RetryOnConflict, absorbing benign write races (concurrent status writers,
// other finalizer actors) without surfacing them as reconciler errors. It
// reports gone=true when the Function disappeared, which callers treat as
// already-deleted. mutate returns false when the desired state already holds
// (no write needed). On a Conflict the fresh Get re-reads the latest object, so
// each retry re-evaluates mutate against current state.
func (r *functionReconciler) updateFinalizerWithRetry(ctx context.Context, key types.NamespacedName, mutate func(*fv1.Function) bool) (gone bool, err error) {
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fn := &fv1.Function{}
		if err := r.client.Get(ctx, key, fn); err != nil {
			return err
		}
		if !mutate(fn) {
			return nil
		}
		return r.client.Update(ctx, fn)
	})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	return false, err
}

func (r *functionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fn := &fv1.Function{}
	if err := r.client.Get(ctx, req.NamespacedName, fn); err != nil {
		if apierrors.IsNotFound(err) {
			// Gone (no finalizer of ours held it): tear down using the last-reconciled
			// object, routed to the executor type that owned it.
			if old, ok := r.lastReconciled.LoadAndDelete(req.NamespacedName); ok {
				oldFn := old.(*fv1.Function)
				if b, ok := r.backends[resolveExecutorType(oldFn)]; ok {
					if err := b.DeleteFunction(ctx, oldFn); err != nil {
						return ctrl.Result{}, err
					}
				}
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Deletion in progress. Only observable here while a finalizer holds the
	// object; if it is ours, tear the workloads down and release it. If it is not
	// ours, leave the cache intact so the NotFound path tears down once the object
	// is actually removed.
	if !fn.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(fn, functionFinalizer) {
			return ctrl.Result{}, nil
		}
		if b, ok := r.backends[resolveExecutorType(fn)]; ok {
			if err := b.DeleteFunction(ctx, fn); err != nil {
				return ctrl.Result{}, err // keep the finalizer; retry teardown
			}
		}
		// Release the finalizer under RetryOnConflict so a concurrent writer
		// (status update, another finalizer actor) does not surface as a
		// reconciler error. gone == true means another actor already removed the
		// last finalizer or the object was force-deleted; teardown already ran
		// above, so the success path applies either way.
		if _, err := r.updateFinalizerWithRetry(ctx, req.NamespacedName, func(f *fv1.Function) bool {
			return controllerutil.RemoveFinalizer(f, functionFinalizer)
		}); err != nil {
			return ctrl.Result{}, err
		}
		r.lastReconciled.Delete(req.NamespacedName)
		return ctrl.Result{}, nil
	}

	// Keep the cleanup finalizer in sync with the opt-in flag. Adding/removing a
	// finalizer is a metadata write (Generation unchanged), so it does not
	// re-trigger this reconciler through GenerationChangedPredicate.
	if r.finalizer && !controllerutil.ContainsFinalizer(fn, functionFinalizer) {
		gone, err := r.updateFinalizerWithRetry(ctx, req.NamespacedName, func(f *fv1.Function) bool {
			return controllerutil.AddFinalizer(f, functionFinalizer)
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		if gone {
			// Deleted out from under us; nothing was created yet, and the deletion
			// path handles teardown via the watch event.
			return ctrl.Result{}, nil
		}
		// The helper mutated a fresh re-read, so the local fn now lacks the
		// finalizer and carries an older resourceVersion. Nothing below writes fn
		// back — it is only read (resolveExecutorType, ReconcileFunction) and
		// deep-copied into lastReconciled — so proceeding with it is safe; the
		// finalizer is metadata-only and is re-read on the next reconcile.
	} else if !r.finalizer && controllerutil.ContainsFinalizer(fn, functionFinalizer) {
		// Toggled off: drain the finalizer so a later delete is never wedged on it.
		if _, err := r.updateFinalizerWithRetry(ctx, req.NamespacedName, func(f *fv1.Function) bool {
			return controllerutil.RemoveFinalizer(f, functionFinalizer)
		}); err != nil {
			return ctrl.Result{}, err
		}
	}

	newType := resolveExecutorType(fn)

	var old *fv1.Function
	if v, ok := r.lastReconciled.Load(req.NamespacedName); ok {
		old = v.(*fv1.Function)
	}

	// A delete+recreate of the same name (UID change) or a switch of executor type
	// can coalesce into one reconcile (the workqueue carries only namespace/name).
	// Tear the old incarnation down under its old type, then treat the new one as a
	// fresh create under its (possibly different) type.
	if old != nil && (old.UID != fn.UID || resolveExecutorType(old) != newType) {
		if ob, ok := r.backends[resolveExecutorType(old)]; ok {
			if err := ob.DeleteFunction(ctx, old); err != nil {
				return ctrl.Result{}, err
			}
		}
		old = nil
	}

	backend, ok := r.backends[newType]
	if !ok {
		// No executor type manages this type (shouldn't happen — CEL validation
		// restricts ExecutorType to the known set). Any old incarnation was already
		// torn down above; drop the cache entry so we don't retain a stale object.
		r.lastReconciled.Delete(req.NamespacedName)
		return ctrl.Result{}, nil
	}

	if err := backend.ReconcileFunction(ctx, old, fn); err != nil {
		return ctrl.Result{}, err
	}
	r.lastReconciled.Store(req.NamespacedName, fn.DeepCopy())
	return ctrl.Result{}, nil
}

// collectBackends returns the executor types that reconcile Functions, keyed by
// executor type. (Every current type does; the filter future-proofs against a
// type that opts out.)
func collectBackends(executorTypes map[fv1.ExecutorType]executortype.ExecutorType) map[fv1.ExecutorType]executortype.FuncReconciler {
	backends := make(map[fv1.ExecutorType]executortype.FuncReconciler, len(executorTypes))
	for name, et := range executorTypes {
		if b, ok := et.(executortype.FuncReconciler); ok {
			backends[name] = b
		}
	}
	return backends
}

// RegisterReconciler wires the single Function reconciler onto the executor
// Manager, dispatching to the executor types that implement FuncReconciler. If no
// executor type reconciles Functions, no reconciler is registered and nil is
// returned. finalizerEnabled opts the reconciler into the cleanup finalizer
// (reliable cross-namespace teardown); when false, any existing cleanup finalizer
// is drained instead.
//
// It uses Or(GenerationChangedPredicate, deletionTimestampPredicate) rather than
// the bare GenerationChangedPredicate Register applies: status-only writes are
// still filtered, but a delete (DeletionTimestamp set, Generation unchanged) must
// reach the reconciler so a finalizer-held Function is torn down and released.
func RegisterReconciler(mgr ctrl.Manager, logger logr.Logger, executorTypes map[fv1.ExecutorType]executortype.ExecutorType, finalizerEnabled bool) error {
	backends := collectBackends(executorTypes)
	if len(backends) == 0 {
		return nil
	}

	names := make([]string, 0, len(backends))
	for name := range backends {
		names = append(names, string(name))
	}
	sort.Strings(names)
	logger.V(1).Info("registering shared function reconciler", "executor_types", names, "finalizer", finalizerEnabled)

	r := &functionReconciler{
		logger:    logger.WithName("function_reconciler"),
		client:    mgr.GetClient(),
		backends:  backends,
		finalizer: finalizerEnabled,
	}

	// Built directly (not via controller.Register) because this reconciler needs
	// .Watches() in addition to .For(): besides Function spec/delete events, it
	// watches the Deployments and Services it manages so a backing object deleted
	// out-of-band re-enqueues the owning Function, which recreates it (drift
	// self-healing). The Manager cache already scopes those types to
	// executor-managed objects (executorManagedSelector), so only newdeploy /
	// container workloads reach the delete-only watch.
	return builder.ControllerManagedBy(mgr).
		Named("executor-function").
		WithOptions(ctrlcontroller.Options{MaxConcurrentReconciles: funcReconcileConcurrency}).
		For(&fv1.Function{}, builder.WithPredicates(
			predicate.Or(predicate.GenerationChangedPredicate{}, deletionTimestampPredicate))).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(ownedObjectToFunction),
			builder.WithPredicates(deleteOnlyPredicate)).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(ownedObjectToFunction),
			builder.WithPredicates(deleteOnlyPredicate)).
		Complete(r)
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statesvc

import (
	"context"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
)

// stateFinalizer gates a stateful Function's deletion on its keyspace's
// lifecycle decision: purge (the default) or retain. Without it, deleting the
// Function would silently orphan the keyspace — an unbounded namespace-budget
// leak (RFC-0023 open question, resolved to purge-by-default).
const stateFinalizer = "fission.io/state-cleanup"

// AnnotationStateRetain, when "true" on a Function, keeps its keyspace's data
// on delete (explicit operator retention; re-creating a Function with the
// same keyspace re-attaches to the retained data).
const AnnotationStateRetain = "fission.io/state-retain"

// deletionTimestampPredicate passes Update events where the object is being
// deleted; GenerationChangedPredicate drops them (DeletionTimestamp does not
// bump Generation), which would wedge finalizer-held Functions. Same idiom as
// the executor's funcreconciler.
var deletionTimestampPredicate = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		return e.ObjectNew != nil && !e.ObjectNew.GetDeletionTimestamp().IsZero()
	},
}

// functionStateReconciler keeps the FunctionIndex in sync with Function CRDs
// and owns the state-cleanup finalizer. It runs on every statesvc replica (no
// leader election — each replica needs its own index, like mcp); the
// finalizer work is idempotent (purging an empty keyspace is a no-op and
// finalizer updates retry on conflict), so concurrent replicas are safe.
type functionStateReconciler struct {
	logger logr.Logger
	client client.Client
	index  *FunctionIndex
	kv     statestore.KVStore // scoped store; purge respects no quota (deletes only)
}

func (r *functionStateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fn := &fv1.Function{}
	if err := r.client.Get(ctx, req.NamespacedName, fn); err != nil {
		if apierrors.IsNotFound(err) {
			r.index.Delete(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !fn.DeletionTimestamp.IsZero() {
		return r.reconcileDeletion(ctx, fn)
	}

	if fn.Spec.State == nil {
		// Opted out (or never opted in): drop from the index and release any
		// finalizer so the opt-out never wedges a later delete. Deliberately
		// NO purge here: opt-out is reversible (re-adding the same keyspace
		// re-attaches to the data), unlike delete; the operator reclaims a
		// truly abandoned keyspace via `fission fn state delete` (admin path
		// reaches unclaimed keyspaces) or by deleting the function while
		// still opted in.
		r.index.Delete(req.NamespacedName)
		if controllerutil.ContainsFinalizer(fn, stateFinalizer) {
			if _, err := r.updateFinalizerWithRetry(ctx, req.NamespacedName, func(f *fv1.Function) bool {
				return controllerutil.RemoveFinalizer(f, stateFinalizer)
			}); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	r.index.Upsert(req.NamespacedName, fn.Spec.State)
	if !controllerutil.ContainsFinalizer(fn, stateFinalizer) {
		if _, err := r.updateFinalizerWithRetry(ctx, req.NamespacedName, func(f *fv1.Function) bool {
			// A delete may have raced in between the cached read above and this
			// fresh Get: the API server forbids adding a finalizer to an object
			// under deletion, so skip (the deletion reconcile, fired by the
			// deletionTimestamp predicate, handles it). Prevents an error-requeue
			// storm on the create→delete race.
			if !f.DeletionTimestamp.IsZero() {
				return false
			}
			return controllerutil.AddFinalizer(f, stateFinalizer)
		}); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// reconcileDeletion purges the keyspace (unless retained or still claimed by
// another Function) and releases the finalizer. Purge failure keeps the
// finalizer so the delete retries rather than silently orphaning data.
func (r *functionStateReconciler) reconcileDeletion(ctx context.Context, fn *fv1.Function) (ctrl.Result, error) {
	nn := types.NamespacedName{Namespace: fn.Namespace, Name: fn.Name}
	// Drop from the index FIRST so ClaimedByOther sees only live claimants,
	// and tokens for this function stop verifying against the index guard.
	r.index.Delete(nn)

	if controllerutil.ContainsFinalizer(fn, stateFinalizer) && fn.Spec.State != nil {
		keyspace := fn.Spec.State.EffectiveKeyspace(fn.Name)
		switch {
		case fn.Annotations[AnnotationStateRetain] == "true":
			r.logger.Info("retaining keyspace on function delete", "function", nn, "keyspace", keyspace)
		case r.index.ClaimedByOther(nn, fn.Namespace, keyspace):
			r.logger.Info("keyspace still claimed by another function; not purging", "function", nn, "keyspace", keyspace)
		default:
			if err := r.purgeKeyspace(ctx, fn.Namespace, keyspace); err != nil {
				r.logger.Error(err, "purging keyspace; will retry", "function", nn, "keyspace", keyspace)
				return ctrl.Result{}, err
			}
			r.logger.Info("purged keyspace on function delete", "function", nn, "keyspace", keyspace)
		}
	}

	if _, err := r.updateFinalizerWithRetry(ctx, nn, func(f *fv1.Function) bool {
		return controllerutil.RemoveFinalizer(f, stateFinalizer)
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// purgeKeyspace deletes every key in the scope, paging until empty.
func (r *functionStateReconciler) purgeKeyspace(ctx context.Context, namespace, keyspace string) error {
	scope := statestore.Scope{Namespace: namespace, Owner: StateOwner, Keyspace: keyspace}
	for {
		kp, err := r.kv.List(ctx, scope, "", statestore.Page{Limit: 500})
		if err != nil {
			return err
		}
		if len(kp.Keys) == 0 {
			return nil
		}
		for _, key := range kp.Keys {
			if err := r.kv.Delete(ctx, scope, key, 0); err != nil {
				return err
			}
		}
	}
}

// updateFinalizerWithRetry re-reads and re-applies mutate under
// RetryOnConflict (the funcreconciler idiom): benign write races retry
// against fresh state, and a vanished Function counts as done.
func (r *functionStateReconciler) updateFinalizerWithRetry(ctx context.Context, key types.NamespacedName, mutate func(*fv1.Function) bool) (gone bool, err error) {
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

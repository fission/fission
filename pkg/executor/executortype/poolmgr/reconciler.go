// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"sync"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
)

// funcDeleter is the slice of *GenericPoolManager the reconciler needs — an
// interface so the delete routing is unit-testable with a fake.
type funcDeleter interface {
	markFuncDeleted(crd.CacheKeyURG)
}

// markFuncDeleted marks a function's pool service entries deleted in the fsCache.
func (gpm *GenericPoolManager) markFuncDeleted(key crd.CacheKeyURG) {
	gpm.fsCache.MarkFuncDeleted(key)
}

// functionReconciler marks a function's pool-manager service entries deleted in
// the fsCache when the Function is removed, so the idle reaper recycles its
// specialized pods. It replaces poolpodcontroller's Function delete handler.
//
// MarkFuncDeleted matches fsCache entries by the function's UID (and records its
// Generation), which a reconciler does not have once the live object is gone, so
// the reconciler keeps the last-seen Function per key to supply it on delete.
// GenerationChangedPredicate is fine: the UID is stable and the Generation is
// captured on every spec change, and the executor's own status writes (which
// leave Generation unchanged) don't need to churn the cache.
//
// Note: the environment (pool lifecycle), replicaset, and specialized-pod
// cleanup watches remain on poolpodcontroller's informers — they are k8s-native
// pod machinery tightly coupled to the gpm actor, migrated in a later step.
type functionReconciler struct {
	logger   logr.Logger
	client   client.Client
	deleter  funcDeleter
	lastSeen sync.Map // client.ObjectKey -> *fv1.Function
}

func (r *functionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fn := &fv1.Function{}
	if err := r.client.Get(ctx, req.NamespacedName, fn); err != nil {
		if apierrors.IsNotFound(err) {
			if old, ok := r.lastSeen.LoadAndDelete(req.NamespacedName); ok {
				r.deleter.markFuncDeleted(crd.CacheKeyURGFromMeta(&old.(*fv1.Function).ObjectMeta))
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	// A delete followed by a quick recreate of the same name can coalesce into a
	// single reconcile (the workqueue only carries namespace/name): if the cached
	// function has a different UID, mark the old one deleted before caching the
	// new one, so its fsCache entry isn't orphaned and its pods are reaped.
	if old, ok := r.lastSeen.Load(req.NamespacedName); ok && old.(*fv1.Function).UID != fn.UID {
		r.deleter.markFuncDeleted(crd.CacheKeyURGFromMeta(&old.(*fv1.Function).ObjectMeta))
	}
	r.lastSeen.Store(req.NamespacedName, fn.DeepCopy())
	return ctrl.Result{}, nil
}

// RegisterReconcilers registers the pool manager's Function reconciler on the
// executor Manager. The environment, replicaset, and specialized-pod-cleanup
// watches remain on poolpodcontroller's informers for now.
func (gpm *GenericPoolManager) RegisterReconcilers(mgr ctrl.Manager) error {
	r := &functionReconciler{
		logger:  gpm.logger.WithName("function_reconciler"),
		client:  mgr.GetClient(),
		deleter: gpm,
	}
	return controller.Register(mgr, &fv1.Function{}, r, "poolmgr-function")
}

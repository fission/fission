// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package executortype

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"

	ctrl "sigs.k8s.io/controller-runtime"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/executor/reaper/idle"
)

type ExecutorType interface {
	// Run runs background job.
	Run(context.Context, *errgroup.Group)

	// RegisterReconcilers registers this executor type's controller-runtime
	// reconcilers (Function/Environment watchers) on the executor Manager. Types
	// still on the informer-handler path return nil.
	RegisterReconcilers(mgr ctrl.Manager) error

	// IdleStrategy returns this executor type's idle-reaping strategy, driven by
	// the shared idle reaper instead of a per-type goroutine.
	IdleStrategy() idle.Strategy

	// GetTypeName returns the name of executor type
	GetTypeName(context.Context) fv1.ExecutorType

	// GetFuncSvc specializes function pod(s) and returns a service URL for the function.
	GetFuncSvc(context.Context, *fv1.Function) (*fscache.FuncSvc, error)

	// GetFuncSvcFromCache retrieves function service from cache.
	GetFuncSvcFromCache(context.Context, *fv1.Function) (*fscache.FuncSvc, error)

	// DumpDebugInfo dump function service cache to temporary directory of executor pod.
	DumpDebugInfo(context.Context) error

	// DeleteFuncSvcFromCache deletes function service entry in cache.
	DeleteFuncSvcFromCache(context.Context, *fscache.FuncSvc)

	// TapService updates the access time of function service entry to
	// avoid idle pod reaper recycles pods.
	TapService(ctx context.Context, serviceUrl string) error

	// UnTapService updates the isActive to false
	UnTapService(ctx context.Context, fnMeta *metav1.ObjectMeta, svcHost string)

	// ReduceSpecializationInProgress updates the svcWaiting count in funcSvcGroup
	MarkSpecializationFailure(ctx context.Context, fnMeta *metav1.ObjectMeta)

	// IsValid returns true if a function service is valid. Different executor types
	// use distinct ways to examine the function service.
	IsValid(context.Context, *fscache.FuncSvc) bool

	// RefreshFuncPods refreshes function pods if the secrets/configmaps pods reference to get updated.
	RefreshFuncPods(context.Context, logr.Logger, fv1.Function) error

	// AdoptOrphanResources adopts existing resources created by the deleted executor.
	AdoptExistingResources(context.Context)

	// CleanupOldExecutorObjects cleans up resources created by old executor instances
	CleanupOldExecutorObjects(context.Context)
}

// FuncReconciler is implemented by executor types to reconcile the Functions
// they own. The shared executor-level Function reconciler
// (pkg/executor/funcreconciler) resolves each Function's executor type, owns the
// last-reconciled cache, and handles delete/recreate and executor-type
// transitions (tearing the old type down via DeleteFunction and building the new
// via ReconcileFunction) — so the three executor types share one Function
// workqueue, predicate, and cache instead of one reconciler each.
type FuncReconciler interface {
	// ReconcileFunction brings this executor type's backing resources for fn to
	// the desired state. old is the previously reconciled Function for this key
	// (guaranteed same executor type and same UID), or nil on the first reconcile
	// of fn under this type — implementations create on nil and update (diffing
	// against old) otherwise.
	ReconcileFunction(ctx context.Context, old, fn *fv1.Function) error

	// DeleteFunction tears down this executor type's backing resources for fn. It
	// is called with the last-reconciled object — which carries this executor
	// type — both when fn is deleted and when fn transitions away to another type.
	DeleteFunction(ctx context.Context, fn *fv1.Function) error
}

// EnvReconciler is implemented by executor types that react to Environment
// changes. The shared executor-level Environment reconciler holds the last-seen
// Environment per key and dispatches each event to every executor type that
// implements this interface, so the executor types share one Environment
// workqueue (and one cache) instead of each registering its own reconciler.
// Types with no Environment-driven behaviour (e.g. container) simply do not
// implement it and are skipped.
type EnvReconciler interface {
	// ReconcileEnvironment is called on every Environment create/update and on the
	// periodic resync. old is the previously reconciled Environment for this key,
	// or nil on first sight. It returns the interval after which this type wants
	// the Environment re-reconciled (0 = none); the dispatcher requeues using the
	// longest interval any type requests.
	ReconcileEnvironment(ctx context.Context, old, env *fv1.Environment) (requeueAfter time.Duration, err error)

	// CleanupEnvironment is called when the Environment is deleted, with the
	// last-seen object (the live one is already gone). It must not return an error:
	// the object is gone, so there is nothing to retry against.
	CleanupEnvironment(ctx context.Context, env *fv1.Environment)
}

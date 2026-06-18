// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package envreconciler holds the executor's single Environment reconciler. It
// replaces the per-executor-type Environment reconcilers (poolmgr pool sync and
// newdeploy runtime-image propagation) with one reconciler that dispatches each
// Environment event to every executor type implementing
// executortype.EnvReconciler. Sharing one reconciler means one Environment
// workqueue, one predicate evaluation per event, and one last-seen cache instead
// of one set per executor type.
package envreconciler

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/executor/executortype"
)

// environmentReconciler dispatches Environment events to every executor type
// that reacts to them. It owns the last-seen Environment per key (so each
// handler is handed the previous object to diff against, and so a delete can
// still hand the gone object to the handlers' cleanup), matching the per-type
// reconcilers it replaces.
type environmentReconciler struct {
	logger   logr.Logger
	client   client.Client
	handlers []executortype.EnvReconciler
	lastSeen sync.Map // client.ObjectKey -> *fv1.Environment
}

func (r *environmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	env := &fv1.Environment{}
	if err := r.client.Get(ctx, req.NamespacedName, env); err != nil {
		if apierrors.IsNotFound(err) {
			if old, ok := r.lastSeen.LoadAndDelete(req.NamespacedName); ok {
				for _, h := range r.handlers {
					h.CleanupEnvironment(ctx, old.(*fv1.Environment))
				}
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	var old *fv1.Environment
	if v, ok := r.lastSeen.Load(req.NamespacedName); ok {
		old = v.(*fv1.Environment)
	}

	// Dispatch to every handler. On the first error we return without advancing
	// the cache, so a retry hands each handler the same `old` again. Handlers are
	// therefore expected to be idempotent for a given (old, env) pair — both
	// current handlers are (poolmgr re-reconciles the pool deployment; newdeploy
	// re-patches functions to the same image).
	var requeueAfter time.Duration
	for _, h := range r.handlers {
		d, err := h.ReconcileEnvironment(ctx, old, env)
		if err != nil {
			return ctrl.Result{}, err
		}
		if d > requeueAfter {
			requeueAfter = d
		}
	}
	r.lastSeen.Store(req.NamespacedName, env.DeepCopy())
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// collectHandlers returns the executor types that react to Environment changes,
// ordered deterministically by executor-type name so behaviour and tests are
// stable regardless of map iteration order.
func collectHandlers(executorTypes map[fv1.ExecutorType]executortype.ExecutorType) []executortype.EnvReconciler {
	names := make([]string, 0, len(executorTypes))
	for name := range executorTypes {
		names = append(names, string(name))
	}
	sort.Strings(names)

	var handlers []executortype.EnvReconciler
	for _, name := range names {
		if h, ok := executorTypes[fv1.ExecutorType(name)].(executortype.EnvReconciler); ok {
			handlers = append(handlers, h)
		}
	}
	return handlers
}

// RegisterReconciler wires the single Environment reconciler onto the executor
// Manager, dispatching to the executor types that implement EnvReconciler. If no
// executor type reacts to Environments, no reconciler is registered and nil is
// returned.
func RegisterReconciler(mgr ctrl.Manager, logger logr.Logger, executorTypes map[fv1.ExecutorType]executortype.ExecutorType) error {
	handlers := collectHandlers(executorTypes)
	if len(handlers) == 0 {
		return nil
	}

	r := &environmentReconciler{
		logger:   logger.WithName("environment_reconciler"),
		client:   mgr.GetClient(),
		handlers: handlers,
	}
	// RegisterTenantScoped adds controller.MembershipPredicate when dynamic
	// tenancy is on (no-op otherwise), so the cluster-wide cache only reconciles
	// Environments in live tenant namespaces.
	return controller.RegisterTenantScoped(mgr, &fv1.Environment{}, r, "executor-environment")
}

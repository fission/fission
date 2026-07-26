// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package versiongc tears down the per-version Kubernetes objects a deleted
// FunctionVersion leaves behind (RFC-0025). The newdeploy and container
// executor types mint a distinct Deployment/Service/HPA set per published
// version (executorUtils.VersionedObjName); when the FunctionVersion CR is
// deleted — by retention GC's sweep (pkg/versioning/retentiongc.go) or a
// manual delete passing the webhook guard — nothing else removes that set, so
// it would leak until the whole Function is deleted.
//
// The teardown is LABEL-based (each executor type's CleanupFunctionVersion
// lists objects by the fv1.FUNCTION_VERSION identity label), NOT an
// ownerReference on the objects pointing at the FunctionVersion, because:
//
//   - Kubernetes ownerRefs require owner and dependent in the SAME namespace.
//     The FunctionVersion lives in the function's namespace, but the objects
//     deploy to nsResolver.GetFunctionNS(fn.Namespace) — a DIFFERENT
//     namespace whenever the chart's functionNamespace value is set and the
//     function is in "default" (the supported backward-compat layout; the
//     chart's own disableOwnerReference docs call this exact limitation out).
//     A cross-namespace ownerRef is resolved in the dependent's namespace,
//     found absent, and the GC deletes the per-version Deployment right after
//     creation.
//   - When enableOwnerReferences is on, the objects already carry a
//     controller ownerRef to the Function. The GC only deletes a dependent
//     once ALL its owners are gone, so ADDING a FunctionVersion ref would
//     never cascade on version delete while the Function lives; REPLACING
//     the Function ref changes teardown semantics for the flag-off installs
//     the flag exists for.
//
// Known window: a FunctionVersion deleted while the executor is down is not
// seen by this reconciler (its initial sync only enqueues versions that still
// exist). That orphan is bounded by two existing backstops: fnDelete's
// cache-independent label sweep removes every per-version set when the
// Function itself is deleted, and reaper.CleanupExecutorObjects reaps all
// previous-incarnation objects (per-version sets are never re-stamped by the
// adopt pass) at the next executor startup.
package versiongc

import (
	"context"
	"errors"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
)

// VersionObjectCleaner is the per-executor-type teardown hook: remove every
// Kubernetes object (and cache entry) backing the named FunctionVersion's
// projection. Implemented by the newdeploy and container managers; poolmgr
// needs no hook — its per-version pods are pool-managed and reaped by the
// idle reaper once versionretain stops retaining them.
type VersionObjectCleaner interface {
	CleanupFunctionVersion(ctx context.Context, fnNamespace, versionName string) error
}

type reconciler struct {
	logger   logr.Logger
	client   client.Client
	cleaners []VersionObjectCleaner
}

// Reconcile treats an existing FunctionVersion as steady state and a missing
// one as the delete signal: the Get below hits the Manager cache, which has
// already dropped the object by the time its DELETE event fires this request.
// FunctionVersions are immutable in practice, so no UPDATE handling exists.
func (r *reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	v := &fv1.FunctionVersion{}
	err := r.client.Get(ctx, req.NamespacedName, v)
	if err == nil {
		// Alive (CREATE / initial-sync event): nothing to tear down.
		return ctrl.Result{}, nil
	}
	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	var errs error
	for _, c := range r.cleaners {
		errs = errors.Join(errs, c.CleanupFunctionVersion(ctx, req.Namespace, req.Name))
	}
	if errs != nil {
		r.logger.Error(errs, "error tearing down per-version objects for deleted function version",
			"version", req.Name, "namespace", req.Namespace)
		// Returning the error requeues with backoff so a transient API
		// failure doesn't turn into a permanent leak.
		return ctrl.Result{}, errs
	}
	return ctrl.Result{}, nil
}

// RegisterReconciler wires the version-GC teardown controller onto mgr,
// watching FunctionVersion tenant-scoped like the executor's other
// controllers. The default GenerationChangedPredicate is fine here — CREATE
// and DELETE events always pass it, and FunctionVersion has no in-place
// updates.
func RegisterReconciler(mgr ctrl.Manager, logger logr.Logger, cleaners ...VersionObjectCleaner) error {
	r := &reconciler{
		logger:   logger.WithName("versiongc_reconciler"),
		client:   mgr.GetClient(),
		cleaners: cleaners,
	}
	return controller.RegisterTenantScoped(mgr, &fv1.FunctionVersion{}, r, "executor-versiongc")
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	"context"
	"errors"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
)

// packageNotReadyRequeueInterval bounds how long a Function reconcile waits
// before re-checking package build-readiness after Publish reports
// ErrPackageNotReady. It is a fixed, cheap poll — not a blocking wait — and a
// drift guard rather than the primary trigger: the package's own
// transition-into-ready event (mapPackageToFunctions, via
// packageBuildReadyPredicate) normally re-enqueues the function long before
// this elapses.
const packageNotReadyRequeueInterval = 30 * time.Second

// AutoPublishReconciler mints a FunctionVersion whenever a Function opted
// into RFC-0025 auto-publish (Spec.Versioning.Mode == "" or "auto") changes
// in a way that affects what gets specialized into a pod or is otherwise
// invocation-observable (versioning.RuntimeAffecting), once its referenced
// Package has reached a build-ready terminal state. It is a thin controller
// over the pure, idempotent Publish engine: this reconciler decides WHEN to
// call Publish, never how a version is constructed.
//
// It watches both Function (the primary resource: an edit is the ordinary
// trigger) and Package (a build completing after the edit — the function's
// spec did not change again, so no new Function event would otherwise ever
// fire) — the same two-watched-types shape as AliasReconciler, registered
// directly via builder.ControllerManagedBy for the same reason: reconciling
// on a second type has no hook through controller.RegisterTenantScoped.
type AutoPublishReconciler struct {
	logger logr.Logger
	// client is the manager's cached controller-runtime client: used for the
	// primary Function fetch that drives every Reconcile call.
	client client.Client
	// clientset is the generated Fission clientset: used for every operation
	// the pure publish engine (Publish, newestVersion) already expects it
	// for, plus the Package-watch map function's dependent-Function List
	// (mirroring pkg/buildermgr/common.go's markFunctionsForPackage, which
	// lists the same way for the same reason — no server-side "functions
	// referencing package X" index exists).
	clientset versioned.Interface
}

// RegisterAutoPublishReconciler wires the RFC-0025 phase-4 auto-publish
// controller onto mgr. Under dynamic/cluster-wide tenancy
// (utils.CrdWatchClusterWide) both watches are additionally scoped to live
// tenant namespaces via controller.MembershipPredicate, and a FissionTenant
// watch re-converges a namespace's functions on onboarding — mirroring
// RegisterAliasReconciler's composition for the types it cannot register
// through controller.RegisterTenantScoped.
func RegisterAutoPublishReconciler(mgr ctrl.Manager, logger logr.Logger, clientset versioned.Interface) error {
	r := &AutoPublishReconciler{
		logger:    logger.WithName("autopublish_reconciler"),
		client:    mgr.GetClient(),
		clientset: clientset,
	}

	fnPredicates := []predicate.Predicate{predicate.GenerationChangedPredicate{}}
	pkgPredicates := []predicate.Predicate{packageBuildReadyPredicate()}
	if utils.CrdWatchClusterWide() {
		mp := controller.MembershipPredicate(utils.DefaultNSResolver())
		fnPredicates = append(fnPredicates, mp)
		pkgPredicates = append(pkgPredicates, mp)
	}

	b := builder.ControllerManagedBy(mgr).
		Named("versioning-autopublish").
		For(&fv1.Function{}, builder.WithPredicates(fnPredicates...)).
		Watches(&fv1.Package{}, handler.EnqueueRequestsFromMapFunc(r.mapPackageToFunctions),
			builder.WithPredicates(pkgPredicates...))

	if utils.CrdWatchClusterWide() {
		b = b.Watches(&fv1.FissionTenant{},
			controller.TenantReenqueueHandler(mgr.GetAPIReader(), mgr.GetScheme(), &fv1.Function{}),
			builder.WithPredicates(controller.TenantOnboardPredicate()))
	}

	return b.Complete(r)
}

// packageBuildReadyPredicate admits a Package Create already in a
// build-ready terminal state, and an Update whose BuildStatus TRANSITIONS
// INTO one (succeeded or none) from a non-ready one. The shape mirrors
// pkg/buildermgr/package_reconciler.go's buildTriggerPredicate (transition
// detection on Update, not a bare field check): without the "was not already
// ready" half, every unrelated status write on an already-ready Package
// (e.g. LastUpdateTimestamp churn) would re-enqueue every dependent Function
// forever. Delete/Generic are dropped — a deleted Package has no build
// outcome to propagate, and a resync Generic event would just re-fire this
// on every already-converged pair.
func packageBuildReadyPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			pkg, ok := e.Object.(*fv1.Package)
			return ok && buildReady(pkg.Status.BuildStatus)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPkg, ok1 := e.ObjectOld.(*fv1.Package)
			newPkg, ok2 := e.ObjectNew.(*fv1.Package)
			if !ok1 || !ok2 {
				return false
			}
			return buildReady(newPkg.Status.BuildStatus) && !buildReady(oldPkg.Status.BuildStatus)
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// buildReady reports whether status is a build-ready terminal state — the
// same predicate Publish itself enforces (ErrPackageNotReady otherwise).
func buildReady(status fv1.BuildStatus) bool {
	return status == fv1.BuildStatusSucceeded || status == fv1.BuildStatusNone
}

// mapPackageToFunctions enqueues every Function in obj's namespace whose
// Spec.Package.PackageRef names it — List+filter, not a server-side query:
// no field index/selector exists for "functions referencing package X" (see
// the clientset field doc). Mirrors
// pkg/buildermgr/common.go:markFunctionsForPackage's lookup for the same
// reason.
func (r *AutoPublishReconciler) mapPackageToFunctions(ctx context.Context, obj client.Object) []reconcile.Request {
	pkg, ok := obj.(*fv1.Package)
	if !ok {
		return nil
	}

	fns, err := r.clientset.CoreV1().Functions(pkg.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		r.logger.V(1).Info("failed to list functions for package watch",
			"namespace", pkg.Namespace, "package", pkg.Name, "error", err)
		return nil
	}

	var reqs []reconcile.Request
	for i := range fns.Items {
		fn := &fns.Items[i]
		if fn.Spec.Package.PackageRef.Name != pkg.Name || fn.Spec.Package.PackageRef.Namespace != pkg.Namespace {
			continue
		}
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(fn)})
	}
	return reqs
}

// Reconcile decides whether req's Function needs a new FunctionVersion and,
// if so, mints one through the shared Publish engine.
//
//  1. Spec.Versioning == nil: the function never opted in. No-op.
//  2. MODE GATE: Mode "" or "auto" proceeds; "manual" no-ops. CRITICAL: the
//     CRD defaults Mode to "auto" (types.go's
//     +kubebuilder:default:=auto), but that default is an apiserver-side
//     effect — fake clients in tests never apply it, and an object read by
//     this reconciler moments after being created (before the apiserver's
//     own defaulting round-trip lands in the cache) can carry "" in a real
//     cluster too. Treating "" as "auto" is therefore load-bearing, not a
//     convenience: without it, every freshly-created auto-publish function
//     would silently no-op its first reconcile.
//  3. Get the newest existing FunctionVersion for the function (by
//     VersionFunctionNameLabel, highest Sequence). None found is NOT
//     equivalent to "not affecting" — it means first publish, which always
//     proceeds (there is nothing to compare against).
//  4. If a newest version exists, classify it against the live spec:
//     zero live Spec.Versioning (Publish would zero it too — comparing
//     against a raw newest.Spec.Snapshot that already has it zeroed would
//     always report "affecting" on this field alone) and normalize the
//     newest version's snapshot (normalizedSnapshot undoes the legacy
//     PackageRef repoint recorded via SourcePackageAnnotation) before
//     handing both to RuntimeAffecting. A live spec unchanged since the
//     last publish must never re-trigger idempotence's false positive here.
//     Not affecting: no-op (metric: unchanged).
//  5. Publish. Its own package-build-readiness gate is reused rather than
//     duplicated: ErrPackageNotReady converts to a fixed RequeueAfter
//     (metric: deferred) instead of a reconcile error, so the workqueue
//     never applies exponential error backoff to an ordinary
//     still-building package — Reconcile does not block waiting for the
//     build either way. Any other Publish error is returned as-is (no
//     metric: an unbounded error label set is exactly what task review
//     ruled out; the returned error still drives the workqueue's normal
//     retry).
//  6. Publish is itself idempotent (its own newest-vs-live comparison,
//     using the same normalizedSnapshot machinery), so this reconciler
//     never fights a concurrent `fission fn publish` of the same spec: both
//     converge on the same existing version (metric: unchanged).
func (r *AutoPublishReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	fn := &fv1.Function{}
	if err := r.client.Get(ctx, req.NamespacedName, fn); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if fn.Spec.Versioning == nil {
		return reconcile.Result{}, nil
	}
	if fn.Spec.Versioning.Mode == fv1.VersioningModeManual {
		return reconcile.Result{}, nil
	}

	newest, _, err := newestVersion(ctx, r.clientset, fn)
	if err != nil {
		return reconcile.Result{}, err
	}

	if newest != nil {
		zeroed := fn.Spec.DeepCopy()
		zeroed.Versioning = nil
		if !RuntimeAffecting(normalizedSnapshot(newest), *zeroed) {
			recordAutopublish(ctx, autopublishResultUnchanged)
			return reconcile.Result{}, nil
		}
	}

	result, err := Publish(ctx, r.clientset, fn, "")
	if err != nil {
		if errors.Is(err, ErrPackageNotReady) {
			recordAutopublish(ctx, autopublishResultDeferred)
			return reconcile.Result{RequeueAfter: packageNotReadyRequeueInterval}, nil
		}
		return reconcile.Result{}, err
	}

	if result.Created {
		recordAutopublish(ctx, autopublishResultCreated)
	} else {
		recordAutopublish(ctx, autopublishResultUnchanged)
	}
	return reconcile.Result{}, nil
}

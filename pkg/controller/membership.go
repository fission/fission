// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	ctrlcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

// MembershipPredicate admits an object only when its namespace is in the
// resolver's live tenant set. It is composed onto a reconciler's watch so that,
// with a cluster-wide Fission-CRD cache (the Tier-A dynamic-watch model, where
// the cache holds CRs from every namespace), objects in non-tenant namespaces
// never reach the workqueue.
//
// It reads the live set on every event (a cheap locked map lookup), so a
// namespace onboarded at runtime starts admitting immediately without rebuilding
// the controller, and an offboarded one stops — the heart of zero-restart
// onboarding. It is for namespaced Fission CRDs only: a cluster-scoped object
// (empty namespace) is never in the tenant set and would always be dropped.
func MembershipPredicate(nsr *utils.NamespaceResolver) predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj != nil && nsr.IsTenant(obj.GetNamespace())
	})
}

// RegisterTenantScoped is Register for a namespaced Fission CRD reconciler under
// the multi-namespace model. When the Fission-CRD cache is cluster-wide (dynamic
// OR cluster mode), it composes MembershipPredicate so CRs in non-tenant
// namespaces are dropped before the workqueue; otherwise it is exactly Register.
// Use it ONLY for namespaced Fission CRDs — never cluster-scoped types, whose
// empty namespace would always be dropped.
func RegisterTenantScoped(mgr ctrl.Manager, obj client.Object, reconciler reconcile.Reconciler, name string) error {
	return RegisterTenantScopedWithPredicates(mgr, obj, reconciler, name, 0, predicate.GenerationChangedPredicate{})
}

// RegisterTenantScopedWithConcurrency is RegisterWithConcurrency under the
// multi-namespace model: the default GenerationChangedPredicate plus
// MembershipPredicate when dynamic namespaces are on.
func RegisterTenantScopedWithConcurrency(mgr ctrl.Manager, obj client.Object, reconciler reconcile.Reconciler, name string, maxConcurrent int) error {
	return RegisterTenantScopedWithPredicates(mgr, obj, reconciler, name, maxConcurrent, predicate.GenerationChangedPredicate{})
}

// RegisterTenantScopedWithPredicates is RegisterWithPredicates plus, when dynamic
// namespaces are on, MembershipPredicate ANDed onto the supplied predicates AND a
// FissionTenant watch that re-converges a namespace on onboarding (see
// TenantReenqueueHandler). The supplied predicates REPLACE the default
// GenerationChangedPredicate (pass it explicitly if wanted).
func RegisterTenantScopedWithPredicates(mgr ctrl.Manager, obj client.Object, reconciler reconcile.Reconciler, name string, maxConcurrent int, predicates ...predicate.Predicate) error {
	if !utils.CrdWatchClusterWide() {
		return RegisterWithPredicates(mgr, obj, reconciler, name, maxConcurrent, predicates...)
	}
	b := builder.ControllerManagedBy(mgr).
		For(obj, builder.WithPredicates(tenantScopedPredicates(predicates)...)).
		Watches(&fv1.FissionTenant{},
			TenantReenqueueHandler(mgr.GetAPIReader(), mgr.GetScheme(), obj),
			builder.WithPredicates(TenantOnboardPredicate())).
		Named(name)
	if maxConcurrent > 0 {
		b = b.WithOptions(ctrlcontroller.Options{MaxConcurrentReconciles: maxConcurrent})
	}
	return b.Complete(reconciler)
}

// RegisterTenantScopedWithRawSources is RegisterTenantScopedWithPredicates
// plus raw event sources (e.g. a wake source.Channel whose pushers enqueue a
// reconcile without a CR change — the workflow engine's append-then-wake
// path). The supplied predicates REPLACE the default
// GenerationChangedPredicate, and raw sources bypass predicates entirely
// (their producers already decided the event matters).
func RegisterTenantScopedWithRawSources(mgr ctrl.Manager, obj client.Object, reconciler reconcile.Reconciler, name string, maxConcurrent int, srcs []source.TypedSource[reconcile.Request], predicates ...predicate.Predicate) error {
	b := builder.ControllerManagedBy(mgr).
		For(obj, builder.WithPredicates(tenantScopedPredicates(predicates)...)).
		Named(name)
	if utils.CrdWatchClusterWide() {
		b = b.Watches(&fv1.FissionTenant{},
			TenantReenqueueHandler(mgr.GetAPIReader(), mgr.GetScheme(), obj),
			builder.WithPredicates(TenantOnboardPredicate()))
	}
	for _, src := range srcs {
		b = b.WatchesRawSource(src)
	}
	if maxConcurrent > 0 {
		b = b.WithOptions(ctrlcontroller.Options{MaxConcurrentReconciles: maxConcurrent})
	}
	return b.Complete(reconciler)
}

// tenantScopedPredicates appends MembershipPredicate when the Fission-CRD cache is
// cluster-wide (dynamic OR cluster mode), copying rather than mutating the caller's
// slice. Extracted so the composition is unit-testable without a Manager.
func tenantScopedPredicates(predicates []predicate.Predicate) []predicate.Predicate {
	if !utils.CrdWatchClusterWide() {
		return predicates
	}
	out := make([]predicate.Predicate, 0, len(predicates)+1)
	out = append(out, predicates...)
	out = append(out, MembershipPredicate(utils.DefaultNSResolver()))
	return out
}

// TenantOnboardPredicate fires the FissionTenant re-enqueue watch on a tenant's
// create and update (onboarding / re-onboarding), but not delete: offboarding
// just stops admitting new events (the resolver-sync controller drops the
// namespace from the live set), and there is nothing to re-converge. Exported so
// reconcilers that build their own controller (e.g. the executor funcreconciler)
// can wire the same FissionTenant watch as RegisterTenantScoped.
func TenantOnboardPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		UpdateFunc:  func(event.UpdateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// TenantReenqueueHandler maps a FissionTenant onboarding event to reconcile
// requests for every `proto`-typed CR already in that tenant's namespace. It
// closes the dynamic-onboarding gap the MembershipPredicate alone leaves: that
// predicate drops a CR whose event arrives while the namespace is not yet a
// tenant, and — because GenerationChangedPredicate also suppresses the informer's
// periodic resync — the CR would never be reconciled again. On a FissionTenant
// add it:
//
//  1. Adds the namespace to the live resolver set IMMEDIATELY (utils.AddTenant),
//     so a CR created right after onboarding is admitted by its own event without
//     waiting on the separate resolver-sync controller — eliminating the
//     cross-controller race that otherwise drops it permanently.
//  2. Re-enqueues the namespace's existing CRs, so any created before onboarding
//     converge in one pass. The enqueued requests bypass the predicates, so a CR
//     dropped earlier is reconciled now.
//
// reader MUST be uncached (mgr.GetAPIReader()): a CR staged moments before the
// onboard may not be in the informer cache yet, and the cached client would then
// return an empty list and silently re-enqueue nothing — the informer-lag race
// that the apiserver read closes.
func TenantReenqueueHandler(reader client.Reader, scheme *runtime.Scheme, proto client.Object) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(tenantReenqueueMapFunc(reader, scheme, proto))
}

// tenantReenqueueMapFunc is the map function behind TenantReenqueueHandler,
// extracted so it can be unit-tested without a workqueue.
func tenantReenqueueMapFunc(reader client.Reader, scheme *runtime.Scheme, proto client.Object) handler.MapFunc {
	return func(ctx context.Context, tenantObj client.Object) []reconcile.Request {
		ft, ok := tenantObj.(*fv1.FissionTenant)
		if !ok || ft.Spec.Namespace == "" {
			return nil
		}
		ns := ft.Spec.Namespace
		// (1) Make membership live now (idempotent), independent of the dedicated
		// resolver-sync controller's timing.
		utils.DefaultNSResolver().AddTenant(ns)

		// (2) List the proto's CRs in ns and enqueue them. Build the List type for
		// proto from the scheme (e.g. Function → FunctionList).
		gvk, err := apiutil.GVKForObject(proto, scheme)
		if err != nil {
			return nil
		}
		gvk.Kind += "List"
		listObj, err := scheme.New(gvk)
		if err != nil {
			return nil
		}
		list, ok := listObj.(client.ObjectList)
		if !ok {
			return nil
		}
		if err := reader.List(ctx, list, client.InNamespace(ns)); err != nil {
			return nil
		}
		var reqs []reconcile.Request
		_ = apimeta.EachListItem(list, func(o runtime.Object) error {
			if m, err := apimeta.Accessor(o); err == nil {
				reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: m.GetNamespace(), Name: m.GetName()}})
			}
			return nil
		})
		log.FromContext(ctx).Info("tenant onboarded: re-enqueueing namespace CRs",
			"namespace", ns, "kind", gvk.Kind, "count", len(reqs))
		return reqs
	}
}

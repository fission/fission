// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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
// the multi-namespace model. When dynamic namespaces are enabled (the Fission-CRD
// cache is then cluster-wide), it composes MembershipPredicate so CRs in
// non-tenant namespaces are dropped before the workqueue; otherwise it is exactly
// Register. Use it ONLY for namespaced Fission CRDs — never cluster-scoped types,
// whose empty namespace would always be dropped.
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
// namespaces are on, MembershipPredicate ANDed onto the supplied predicates. The
// supplied predicates REPLACE the default GenerationChangedPredicate (pass it
// explicitly if wanted).
func RegisterTenantScopedWithPredicates(mgr ctrl.Manager, obj client.Object, reconciler reconcile.Reconciler, name string, maxConcurrent int, predicates ...predicate.Predicate) error {
	return RegisterWithPredicates(mgr, obj, reconciler, name, maxConcurrent, tenantScopedPredicates(predicates)...)
}

// tenantScopedPredicates appends MembershipPredicate when dynamic namespaces are
// enabled, copying rather than mutating the caller's slice. Extracted so the
// composition is unit-testable without a Manager.
func tenantScopedPredicates(predicates []predicate.Predicate) []predicate.Predicate {
	if !utils.DynamicNamespacesEnabled() {
		return predicates
	}
	out := make([]predicate.Predicate, 0, len(predicates)+1)
	out = append(out, predicates...)
	out = append(out, MembershipPredicate(utils.DefaultNSResolver()))
	return out
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

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

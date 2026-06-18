// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

func TestMembershipPredicate(t *testing.T) {
	r := &utils.NamespaceResolver{}
	r.SetTenants(map[string]string{"team-a": "team-a"})
	p := MembershipPredicate(r)

	inTenant := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a"}}
	notTenant := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Namespace: "team-b"}}

	assert.True(t, p.Create(event.CreateEvent{Object: inTenant}), "tenant-namespace object is admitted")
	assert.False(t, p.Create(event.CreateEvent{Object: notTenant}), "non-tenant object is dropped")
	assert.True(t, p.Delete(event.DeleteEvent{Object: inTenant}), "tenant-namespace delete is admitted")
	assert.False(t, p.Update(event.UpdateEvent{ObjectNew: notTenant}), "non-tenant update is dropped")

	// The predicate reads the live set, so a namespace onboarded at runtime
	// starts admitting without rebuilding the controller.
	r.AddTenant("team-b")
	assert.True(t, p.Create(event.CreateEvent{Object: notTenant}), "predicate must observe a later AddTenant")
}

// TestTenantReenqueueMapFunc is the regression guard for the runtime-onboarding
// 404: when a namespace is onboarded, the FissionTenant re-enqueue must (1) make
// the namespace a live tenant immediately and (2) re-enqueue only that namespace's
// CRs — so a Function whose event predated the tenant (dropped by the membership
// predicate, never re-delivered) converges instead of 404-ing forever.
func TestTenantReenqueueMapFunc(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, fv1.AddToScheme(scheme))

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&fv1.Function{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "fn1"}},
		&fv1.Function{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "fn2"}},
		&fv1.Function{ObjectMeta: metav1.ObjectMeta{Namespace: "team-b", Name: "other"}},
	).Build()

	r := utils.DefaultNSResolver()
	r.RemoveTenant("team-a")
	t.Cleanup(func() { r.RemoveTenant("team-a") })
	require.False(t, r.IsTenant("team-a"), "precondition: team-a is not yet a tenant")

	reqs := tenantReenqueueMapFunc(c, scheme, &fv1.Function{})(t.Context(),
		&fv1.FissionTenant{
			ObjectMeta: metav1.ObjectMeta{Name: "team-a"},
			Spec:       fv1.FissionTenantSpec{Namespace: "team-a"},
		})

	// (1) Membership went live immediately — so a Function created right after
	// onboarding is admitted by its own event without the resolver-sync race.
	assert.True(t, r.IsTenant("team-a"), "onboarding must make the namespace a live tenant")
	// (2) Only team-a's Functions are re-enqueued (not team-b's).
	require.Len(t, reqs, 2)
	assert.ElementsMatch(t, []string{"fn1", "fn2"}, []string{reqs[0].Name, reqs[1].Name})
	for _, req := range reqs {
		assert.Equal(t, "team-a", req.Namespace)
	}

	// A FissionTenant with no namespace is a no-op (defensive).
	assert.Nil(t, tenantReenqueueMapFunc(c, scheme, &fv1.Function{})(t.Context(), &fv1.FissionTenant{}))
}

func TestTenantScopedPredicates(t *testing.T) {
	base := []predicate.Predicate{predicate.GenerationChangedPredicate{}}

	t.Setenv("FISSION_TENANCY_MODE", "static")
	assert.Len(t, tenantScopedPredicates(base), 1, "off: no membership predicate added")

	t.Setenv("FISSION_TENANCY_MODE", "dynamic")
	got := tenantScopedPredicates(base)
	assert.Len(t, got, 2, "dynamic: membership predicate appended")
	assert.Len(t, base, 1, "the caller's slice must not be mutated")

	// Cluster mode also runs a cluster-wide cache, so it MUST get the membership
	// predicate too — otherwise the cluster-wide cache reconciles every namespace
	// before the resolver knows it is a tenant, breaking ordering + key stamping.
	t.Setenv("FISSION_TENANCY_MODE", "cluster")
	assert.Len(t, tenantScopedPredicates(base), 2, "cluster: membership predicate appended")
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/fission/fission/pkg/utils"
)

// TestSyncResolverFromTenants pins the shared resolver-sync: the live tenant set
// is the env seed plus every FissionTenant's namespace. Data-plane managers run
// this (read-only) so a runtime-onboarded namespace reaches their membership
// predicate without a restart — the cross-process half of dynamic onboarding.
func TestSyncResolverFromTenants(t *testing.T) {
	c := newFakeClient(t, tenant("team-a", "team-a"), tenant("custom-name", "team-b"))
	resolver := &utils.NamespaceResolver{}

	require.NoError(t, SyncResolverFromTenants(t.Context(), c, resolver))

	assert.True(t, resolver.IsTenant("team-a"), "tenant namespace by CR name")
	assert.True(t, resolver.IsTenant("team-b"), "tenant namespace under a custom CR name")
	assert.False(t, resolver.IsTenant("never-onboarded"), "a namespace with no FissionTenant is not a tenant")
}

// TestResolverSyncReconciler checks the reconciler drives the same sync on any
// FissionTenant event.
func TestResolverSyncReconciler(t *testing.T) {
	c := newFakeClient(t, tenant("team-a", "team-a"))
	resolver := &utils.NamespaceResolver{}
	r := &ResolverSyncReconciler{client: c, resolver: resolver}

	_, err := r.Reconcile(t.Context(), ctrl.Request{})
	require.NoError(t, err)
	assert.True(t, resolver.IsTenant("team-a"))
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"testing"
	"time"

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

// TestResolverSyncReconcilerHooks verifies that hooks are called after
// SetTenants. This is the mechanism the router uses to re-scope its
// per-namespace EndpointSlice informers when tenants change at runtime (#3647).
func TestResolverSyncReconcilerHooks(t *testing.T) {
	c := newFakeClient(t, tenant("team-a", "team-a"), tenant("team-b", "team-b"))
	resolver := &utils.NamespaceResolver{}

	var called int
	r := &ResolverSyncReconciler{
		client:   c,
		resolver: resolver,
		hooks: []func() bool{
			func() bool {
				called++
				return false
			},
		},
	}

	res, err := r.Reconcile(t.Context(), ctrl.Request{})
	require.NoError(t, err)
	assert.Equal(t, 1, called, "hook must be called once per Reconcile")
	assert.Zero(t, res.RequeueAfter, "no requeue when no hook reports pending")
}

// TestResolverSyncReconcilerPendingRequeues verifies that a hook reporting
// pending work triggers an explicit-interval requeue starting at
// hookRetryDelay. This retries quickly while avoiding the workqueue rate
// limiter's exponential backoff, which would leave a namespace unadmitted for
// ~16 minutes when the missing RoleBinding finally lands.
func TestResolverSyncReconcilerPendingRequeues(t *testing.T) {
	c := newFakeClient(t, tenant("team-a", "team-a"))
	resolver := &utils.NamespaceResolver{}
	r := &ResolverSyncReconciler{
		client:   c,
		resolver: resolver,
		hooks: []func() bool{
			func() bool { return true },
		},
	}

	res, err := r.Reconcile(t.Context(), ctrl.Request{})
	require.NoError(t, err)
	assert.Equal(t, hookRetryDelay, res.RequeueAfter, "first pending reconcile must requeue after the base poll interval")
}

// TestResolverSyncReconcilerPendingBackoff verifies the escalating poll
// interval: consecutive pending reconciles double the delay up to
// hookRetryMaxDelay and hold there, so a namespace that can never pass (an
// orphaned FissionTenant) settles into a cheap poll instead of re-checking
// every 2s forever. A clean reconcile resets it, so the next onboarding race
// still converges at the base interval.
func TestResolverSyncReconcilerPendingBackoff(t *testing.T) {
	c := newFakeClient(t, tenant("team-a", "team-a"))
	pending := true
	r := &ResolverSyncReconciler{
		client:   c,
		resolver: &utils.NamespaceResolver{},
		hooks: []func() bool{
			func() bool { return pending },
		},
	}

	for _, want := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, hookRetryMaxDelay, hookRetryMaxDelay} {
		res, err := r.Reconcile(t.Context(), ctrl.Request{})
		require.NoError(t, err)
		assert.Equal(t, want, res.RequeueAfter, "escalating poll interval")
	}

	// A clean reconcile clears the counter...
	pending = false
	res, err := r.Reconcile(t.Context(), ctrl.Request{})
	require.NoError(t, err)
	require.Zero(t, res.RequeueAfter, "no requeue once hooks report no pending work")

	// ...so the next pending round starts over at the base interval.
	pending = true
	res, err = r.Reconcile(t.Context(), ctrl.Request{})
	require.NoError(t, err)
	assert.Equal(t, hookRetryDelay, res.RequeueAfter, "backoff must reset after a clean reconcile")
}

// TestResolverSyncReconcilerNoHooks verifies that a reconciler with no hooks
// (the default for executor/buildermgr) still works — backward compatibility.
func TestResolverSyncReconcilerNoHooks(t *testing.T) {
	c := newFakeClient(t, tenant("team-a", "team-a"))
	resolver := &utils.NamespaceResolver{}
	r := &ResolverSyncReconciler{client: c, resolver: resolver}

	_, err := r.Reconcile(t.Context(), ctrl.Request{})
	require.NoError(t, err)
	assert.True(t, resolver.IsTenant("team-a"))
}

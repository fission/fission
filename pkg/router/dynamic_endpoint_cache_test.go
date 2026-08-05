// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/fission/fission/pkg/router/endpointcache"
	"github.com/fission/fission/pkg/utils"
)

// addOnlyManager lets setupDynamicEndpointCache register its startup runnable
// without starting a full controller-runtime manager. No other Manager method
// is used while setupDynamicEndpointCache is being called.
type addOnlyManager struct {
	ctrl.Manager
}

func (*addOnlyManager) Add(manager.Runnable) error {
	return nil
}

// TestSetupDynamicEndpointCacheReusesPreflightRBAC verifies that namespaces
// approved by the startup preflight are reused by the initial dynamic-cache
// sync instead of issuing the same SelfSubjectAccessReviews again.
func TestSetupDynamicEndpointCacheReusesPreflightRBAC(t *testing.T) {
	kubeClient := fake.NewSimpleClientset()
	var sarCalls atomic.Int32
	kubeClient.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		sarCalls.Add(1)
		sar := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		return true, &authorizationv1.SelfSubjectAccessReview{
			Spec:   sar.Spec,
			Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true},
		}, nil
	})

	precheckedNamespaces := sliceWatchNamespaces()
	require.NotEmpty(t, precheckedNamespaces)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	_, hooks, err := setupDynamicEndpointCache(
		ctx,
		kubeClient,
		endpointcache.NewIndex(),
		&addOnlyManager{},
		nil,
		logr.Discard(),
		precheckedNamespaces,
	)
	require.NoError(t, err)
	require.Len(t, hooks, 1)

	pending := hooks[0]()
	assert.False(t, pending)
	assert.Zero(t, sarCalls.Load(), "preflight-approved namespaces must not repeat SARs during dynamic-cache setup")
}

// newDynamicCacheTest builds a dynamicEndpointCache backed by a fake clientset
// whose SAR reactor consults the allowedNS map. A namespace present with value
// true → SAR Allowed=true; absent or false → Allowed=false. The map is shared
// so tests can flip a namespace's RBAC state between calls (the re-onboard
// scenario). Returns the cache and the resolver (so tests can onboard/offboard).
func newDynamicCacheTest(t *testing.T, allowedNS map[string]bool) (*dynamicEndpointCache, *utils.NamespaceResolver) {
	t.Helper()
	kubeClient := fake.NewSimpleClientset()
	var mu sync.Mutex
	kubeClient.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		sar := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		ns := sar.Spec.ResourceAttributes.Namespace
		mu.Lock()
		defer mu.Unlock()
		return true, &authorizationv1.SelfSubjectAccessReview{
			Spec:   sar.Spec,
			Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowedNS[ns]},
		}, nil
	})
	resolver := &utils.NamespaceResolver{}
	nsi := endpointcache.NewNamespaceInformers(kubeClient, endpointcache.NewIndex(), logr.Discard())
	t.Cleanup(nsi.Close)
	dynCache := &dynamicEndpointCache{
		kubeClient:  kubeClient,
		resolver:    resolver,
		nsInformers: nsi,
		logger:      logr.Discard(),
	}
	return dynCache, resolver
}

// TestDynamicCacheAllowedCachesAndIncludes verifies the happy path: a namespace
// that passes SAR is cached in rbacChecked, included in the informer set, and
// does not report pending.
func TestDynamicCacheAllowedCachesAndIncludes(t *testing.T) {
	t.Parallel()
	allowedNS := map[string]bool{"team-a": true}
	dyn, resolver := newDynamicCacheTest(t, allowedNS)
	resolver.SetTenants(map[string]string{"team-a": "team-a"})

	pending := dyn.syncInformers(t.Context())
	assert.False(t, pending, "allowed namespace must not report pending")
	_, cached := dyn.rbacChecked.Load("team-a")
	assert.True(t, cached, "allowed namespace must be cached in rbacChecked")
}

// TestDynamicCacheDeniedExcludesAndReportsPending verifies the #3647 exclusion:
// a namespace whose SAR returns Allowed=false is EXCLUDED from the informer set
// (not cached) and reports pending=true so the reconciler requeues.
func TestDynamicCacheDeniedExcludesAndReportsPending(t *testing.T) {
	t.Parallel()
	allowedNS := map[string]bool{"team-a": false}
	dyn, resolver := newDynamicCacheTest(t, allowedNS)
	resolver.SetTenants(map[string]string{"team-a": "team-a"})

	pending := dyn.syncInformers(t.Context())
	assert.True(t, pending, "denied namespace must report pending")
	_, cached := dyn.rbacChecked.Load("team-a")
	assert.False(t, cached, "denied namespace must not be cached")
}

// TestDynamicCachePrunesOnOffboard verifies fix #1: when a namespace leaves the
// live set (offboard), its cached SAR pass is pruned from rbacChecked. Without
// pruning, the stale entry would make a re-onboard skip the SAR.
func TestDynamicCachePrunesOnOffboard(t *testing.T) {
	t.Parallel()
	allowedNS := map[string]bool{"team-a": true}
	dyn, resolver := newDynamicCacheTest(t, allowedNS)

	// Onboard team-a → SAR passes → cached.
	resolver.SetTenants(map[string]string{"team-a": "team-a"})
	dyn.syncInformers(t.Context())
	_, cached := dyn.rbacChecked.Load("team-a")
	require.True(t, cached, "team-a must be cached after onboarding")

	// Offboard team-a → cache must be pruned.
	resolver.SetTenants(map[string]string{})
	dyn.syncInformers(t.Context())
	_, cached = dyn.rbacChecked.Load("team-a")
	assert.False(t, cached, "team-a must be pruned from rbacChecked after offboard")
}

// TestDynamicCacheReOnboardReRunsSAR is the core regression test for fix #1:
// onboard → offboard → re-onboard with SAR now DENIED. Without the cache-prune,
// the stale rbacChecked entry would make syncInformers skip the SAR, include the
// namespace, and return pending=false — starting an informer whose LIST stays
// forbidden, wedging /readyz fleet-wide. With the prune, the SAR re-runs and
// the namespace is correctly excluded + pending.
func TestDynamicCacheReOnboardReRunsSAR(t *testing.T) {
	t.Parallel()
	allowedNS := map[string]bool{"team-a": true}
	dyn, resolver := newDynamicCacheTest(t, allowedNS)

	// Onboard team-a → SAR passes → cached.
	resolver.SetTenants(map[string]string{"team-a": "team-a"})
	dyn.syncInformers(t.Context())
	_, cached := dyn.rbacChecked.Load("team-a")
	require.True(t, cached, "team-a must be cached after first onboarding")

	// Offboard team-a → cache pruned.
	resolver.SetTenants(map[string]string{})
	dyn.syncInformers(t.Context())
	_, cached = dyn.rbacChecked.Load("team-a")
	require.False(t, cached, "team-a must be pruned after offboard")

	// Re-onboard team-a, but now SAR denies (RoleBinding not yet recreated).
	allowedNS["team-a"] = false
	resolver.SetTenants(map[string]string{"team-a": "team-a"})
	pending := dyn.syncInformers(t.Context())

	assert.True(t, pending, "re-onboarded namespace with denied SAR must report pending")
	_, cached = dyn.rbacChecked.Load("team-a")
	assert.False(t, cached, "re-onboarded namespace with denied SAR must not be cached")
}

// TestDynamicCacheReOnboardAfterRBACLands verifies the full lifecycle: onboard
// denied → requeue (pending) → RBAC lands → re-onboard allowed → cached. This
// is the normal onboarding race (tenant controller hasn't created the binding
// yet) resolving correctly.
func TestDynamicCacheReOnboardAfterRBACLands(t *testing.T) {
	t.Parallel()
	allowedNS := map[string]bool{"team-a": false}
	dyn, resolver := newDynamicCacheTest(t, allowedNS)
	resolver.SetTenants(map[string]string{"team-a": "team-a"})

	// First sync: SAR denied → pending, not cached.
	pending := dyn.syncInformers(t.Context())
	require.True(t, pending, "denied on first check")
	_, cached := dyn.rbacChecked.Load("team-a")
	require.False(t, cached, "not cached when denied")

	// RBAC lands: SAR now allowed.
	allowedNS["team-a"] = true
	pending = dyn.syncInformers(t.Context())
	assert.False(t, pending, "no longer pending once SAR passes")
	_, cached = dyn.rbacChecked.Load("team-a")
	assert.True(t, cached, "cached once SAR passes")
}

// TestDynamicCacheSARErrorReportsPending verifies fix #2: when the SAR itself
// errors (apiserver flake), the namespace is optimistically included but pending
// is still set true. Without pending, a genuinely-unauthorized namespace whose
// SAR consistently errors keeps an unsyncable informer forever — no FissionTenant
// event fires to re-run the check.
func TestDynamicCacheSARErrorReportsPending(t *testing.T) {
	t.Parallel()
	kubeClient := fake.NewSimpleClientset()
	kubeClient.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, assert.AnError
	})
	resolver := &utils.NamespaceResolver{}
	nsi := endpointcache.NewNamespaceInformers(kubeClient, endpointcache.NewIndex(), logr.Discard())
	t.Cleanup(nsi.Close)
	dyn := &dynamicEndpointCache{
		kubeClient:  kubeClient,
		resolver:    resolver,
		nsInformers: nsi,
		logger:      logr.Discard(),
	}
	resolver.SetTenants(map[string]string{"team-a": "team-a"})

	errCtx, cancel := context.WithCancel(t.Context())
	cancel()

	pending := dyn.syncInformers(errCtx)

	assert.True(t, pending, "SAR error must report pending so reconciler requeues")
	_, cached := dyn.rbacChecked.Load("team-a")
	assert.False(t, cached, "namespace with SAR error must not be cached")
}

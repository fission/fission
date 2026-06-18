// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fscheme "github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/utils"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, fscheme.AddToScheme(s))
	return s
}

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&fv1.FissionTenant{}).
		Build()
}

func ns(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, UID: types.UID(name + "-uid")}}
}

func tenant(name, namespace string) *fv1.FissionTenant {
	return &fv1.FissionTenant{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec:       fv1.FissionTenantSpec{Namespace: namespace},
	}
}

func TestTenantReconcilerNamespaceExistsSetsReady(t *testing.T) {
	ft := tenant("team-a", "team-a")
	c := newFakeClient(t, ft, ns("team-a", nil))
	r := &TenantReconciler{logger: logr.Discard(), client: c, resolver: &utils.NamespaceResolver{}}

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "team-a"}})
	require.NoError(t, err)

	got := &fv1.FissionTenant{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Name: "team-a"}, got))
	cond := apimeta.FindStatusCondition(got.Status.Conditions, fv1.FissionTenantConditionReady)
	require.NotNil(t, cond, "Ready condition must be set")
	assert.Equal(t, metav1.ConditionTrue, cond.Status)

	assert.Contains(t, r.resolver.FissionResourceNamespaces(), "team-a", "resolver must include the onboarded namespace")
}

func TestTenantReconcilerSetsObservedGeneration(t *testing.T) {
	ft := tenant("team-a", "team-a") // Generation: 1
	c := newFakeClient(t, ft, ns("team-a", nil))
	r := &TenantReconciler{logger: logr.Discard(), client: c, resolver: &utils.NamespaceResolver{}}

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "team-a"}})
	require.NoError(t, err)

	got := &fv1.FissionTenant{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Name: "team-a"}, got))
	assert.Equal(t, int64(1), got.Status.ObservedGeneration, "status.observedGeneration must track spec generation")
}

func TestTenantReconcilerNamespaceToRequests(t *testing.T) {
	// "custom" CR manages namespace team-b; a team-b namespace event must enqueue it.
	c := newFakeClient(t, tenant("team-a", "team-a"), tenant("custom", "team-b"))
	r := &TenantReconciler{logger: logr.Discard(), client: c, resolver: &utils.NamespaceResolver{}}

	reqs := r.namespaceToRequests(t.Context(), ns("team-b", nil))
	require.Len(t, reqs, 1)
	assert.Equal(t, "custom", reqs[0].Name, "the tenant whose spec.namespace matches must be enqueued by its CR name")

	assert.Empty(t, r.namespaceToRequests(t.Context(), ns("unmanaged", nil)), "an unmanaged namespace enqueues nothing")
}

// TestTenantReconcilerOffboardTearsDownRBAC drives the security-critical
// offboarding path through Reconcile: a deleted FissionTenant (held by its
// finalizer) must tear down the provisioned RBAC AND the derived-key Secret
// before releasing the finalizer, so no privilege/key residue outlives the tenant.
func TestTenantReconcilerOffboardTearsDownRBAC(t *testing.T) {
	ft := tenant("team-a", "team-a")
	ft.Finalizers = []string{tenantFinalizer}
	c := newFakeClient(t, ft, ns("team-a", nil))
	ctx := t.Context()

	// Provision as a prior reconcile would have.
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a", "fission", metav1.OwnerReference{}))
	require.NoError(t, EnsureNamespaceAuthSecret(ctx, c, []byte("master-bytes"), "team-a"))

	resolver := &utils.NamespaceResolver{}
	resolver.AddTenant("team-a")
	r := &TenantReconciler{logger: logr.Discard(), client: c, resolver: resolver, master: []byte("master-bytes"), releaseNamespace: "fission"}

	// Offboard: deleting the CR leaves it with a DeletionTimestamp (finalizer held).
	cur := &fv1.FissionTenant{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "team-a"}, cur))
	require.NoError(t, c.Delete(ctx, cur))

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "team-a"}})
	require.NoError(t, err)

	notFound := func(obj client.Object, name string, ns string) bool {
		return apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, obj))
	}
	assert.True(t, notFound(&corev1.ServiceAccount{}, fv1.FissionFetcherSA, "team-a"), "fetcher SA must be torn down")
	assert.True(t, notFound(&corev1.Secret{}, fv1.TenantAuthKeysSecret, "team-a"), "derived-key Secret must be torn down")
	assert.True(t, notFound(&fv1.FissionTenant{}, "team-a", ""), "finalizer released → the FissionTenant is removed")
	assert.NotContains(t, r.resolver.FissionResourceNamespaces(), "team-a", "resolver must drop the offboarded namespace")
}

func TestTenantReconcilerProvisionsAuthSecret(t *testing.T) {
	ft := tenant("team-a", "team-a")
	c := newFakeClient(t, ft, ns("team-a", nil))
	r := &TenantReconciler{logger: logr.Discard(), client: c, resolver: &utils.NamespaceResolver{}, master: []byte("master-bytes-for-test")}

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "team-a"}})
	require.NoError(t, err)

	// The derived-key secret is provisioned in the tenant namespace.
	s := &corev1.Secret{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "team-a", Name: fv1.TenantAuthKeysSecret}, s))
	assert.NotEmpty(t, s.Data["fetcherKey"], "derived fetcher key must be written")

	// And AuthKeyProvisioned is reported True.
	got := &fv1.FissionTenant{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Name: "team-a"}, got))
	cond := apimeta.FindStatusCondition(got.Status.Conditions, fv1.FissionTenantConditionAuthKeyProvisioned)
	require.NotNil(t, cond, "AuthKeyProvisioned must be set when a master is present")
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestTenantReconcilerNamespaceMissingSetsNotReady(t *testing.T) {
	ft := tenant("ghost", "ghost") // no Namespace object created
	c := newFakeClient(t, ft)
	r := &TenantReconciler{logger: logr.Discard(), client: c, resolver: &utils.NamespaceResolver{}}

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ghost"}})
	require.NoError(t, err)

	got := &fv1.FissionTenant{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Name: "ghost"}, got))
	cond := apimeta.FindStatusCondition(got.Status.Conditions, fv1.FissionTenantConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, ReasonNamespaceNotFound, cond.Reason)
}

func TestTenantReconcilerDeletedDropsFromResolver(t *testing.T) {
	// no FissionTenant in the store → reconcile sees NotFound → resolver re-synced
	c := newFakeClient(t)
	res := &utils.NamespaceResolver{}
	res.SetTenants(map[string]string{"default": "default", "stale": "stale"})
	r := &TenantReconciler{logger: logr.Discard(), client: c, resolver: res}

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "stale"}})
	require.NoError(t, err)

	assert.NotContains(t, r.resolver.FissionResourceNamespaces(), "stale", "deleted tenant must drop from the resolver")
}

func TestNamespaceReconcilerLabeledMaterializesCR(t *testing.T) {
	c := newFakeClient(t, ns("team-b", map[string]string{EnabledLabel: "true"}))
	r := &NamespaceReconciler{logger: logr.Discard(), client: c}

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "team-b"}})
	require.NoError(t, err)

	got := &fv1.FissionTenant{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Name: "team-b"}, got), "a FissionTenant must be materialized")
	assert.Equal(t, "team-b", got.Spec.Namespace)
	assert.Equal(t, managedByLabel, got.Annotations[managedByAnnotation])
	require.Len(t, got.OwnerReferences, 1)
	assert.Equal(t, "Namespace", got.OwnerReferences[0].Kind)
}

func TestNamespaceReconcilerAlreadyOnboardedNoDuplicate(t *testing.T) {
	c := newFakeClient(t,
		ns("team-c", map[string]string{EnabledLabel: "true"}),
		tenant("custom-name", "team-c"), // a user CR already manages team-c under a different name
	)
	r := &NamespaceReconciler{logger: logr.Discard(), client: c}

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "team-c"}})
	require.NoError(t, err)

	list := &fv1.FissionTenantList{}
	require.NoError(t, c.List(t.Context(), list))
	assert.Len(t, list.Items, 1, "must not create a duplicate FissionTenant for an already-onboarded namespace")
}

func TestNamespaceReconcilerUnlabeledNoCR(t *testing.T) {
	c := newFakeClient(t, ns("plain", nil))
	r := &NamespaceReconciler{logger: logr.Discard(), client: c}

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "plain"}})
	require.NoError(t, err)

	list := &fv1.FissionTenantList{}
	require.NoError(t, c.List(t.Context(), list))
	assert.Empty(t, list.Items, "an unlabeled namespace must not be materialized")
}

// TestNamespaceReconcilerAutoOnboardAll covers cluster mode: every non-system
// namespace is materialized into a FissionTenant regardless of the label, while
// the Kubernetes system namespaces and the control-plane namespace are excluded.
func TestNamespaceReconcilerAutoOnboardAll(t *testing.T) {
	reconcile := func(t *testing.T, c client.Client, name string) {
		t.Helper()
		r := &NamespaceReconciler{logger: logr.Discard(), client: c, autoOnboardAll: true, releaseNamespace: "fission"}
		_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
		require.NoError(t, err)
	}

	t.Run("unlabeled namespace is auto-onboarded", func(t *testing.T) {
		c := newFakeClient(t, ns("team-x", nil))
		reconcile(t, c, "team-x")
		got := &fv1.FissionTenant{}
		require.NoError(t, c.Get(t.Context(), types.NamespacedName{Name: "team-x"}, got),
			"cluster mode must materialize a FissionTenant for an unlabeled namespace")
		assert.Equal(t, "team-x", got.Spec.Namespace)
	})

	for _, sys := range []string{"kube-system", "kube-public", "kube-node-lease", "fission"} {
		t.Run("excludes "+sys, func(t *testing.T) {
			c := newFakeClient(t, ns(sys, nil))
			reconcile(t, c, sys)
			list := &fv1.FissionTenantList{}
			require.NoError(t, c.List(t.Context(), list))
			assert.Empty(t, list.Items, "system / control-plane namespace %q must not be auto-onboarded", sys)
		})
	}

	// Delete-recreate race: a managed FissionTenant left by a deleted same-named
	// namespace (stale owner UID) must not be treated as "already onboarded" — the
	// reconciler requeues so it re-materializes for the current namespace once GC
	// reaps the stale CR.
	t.Run("requeues on a stale managed tenant from a recreated namespace", func(t *testing.T) {
		staleFT := &fv1.FissionTenant{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "team-x",
				Annotations:     map[string]string{managedByAnnotation: managedByLabel},
				OwnerReferences: []metav1.OwnerReference{{Kind: "Namespace", Name: "team-x", UID: "team-x-OLD-uid"}},
			},
			Spec: fv1.FissionTenantSpec{Namespace: "team-x"},
		}
		c := newFakeClient(t, ns("team-x", nil), staleFT) // ns("team-x") has UID "team-x-uid" != stale
		r := &NamespaceReconciler{logger: logr.Discard(), client: c, autoOnboardAll: true, releaseNamespace: "fission"}
		res, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "team-x"}})
		require.NoError(t, err)
		assert.Positive(t, res.RequeueAfter, "stale managed tenant must trigger a requeue, not an early return")
		list := &fv1.FissionTenantList{}
		require.NoError(t, c.List(t.Context(), list))
		assert.Len(t, list.Items, 1, "must not create a duplicate while the stale CR is still being GC'd")
	})

	// A user-authored CR (no managed annotation) for the namespace is respected:
	// no requeue, no duplicate.
	t.Run("respects a user-authored tenant under a different name", func(t *testing.T) {
		c := newFakeClient(t, ns("team-y", nil), tenant("custom", "team-y"))
		r := &NamespaceReconciler{logger: logr.Discard(), client: c, autoOnboardAll: true, releaseNamespace: "fission"}
		res, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "team-y"}})
		require.NoError(t, err)
		assert.Zero(t, res.RequeueAfter, "a user-authored tenant is not stale; no requeue")
		list := &fv1.FissionTenantList{}
		require.NoError(t, c.List(t.Context(), list))
		assert.Len(t, list.Items, 1, "must not duplicate a user-authored tenant")
	})
}

// TestNamespaceReconcilerClusterOptOut covers the cluster-mode opt-out label
// (fission.io/enabled=false): a labelled namespace is not onboarded, and labelling
// an already-onboarded namespace offboards its controller-managed FissionTenant
// while leaving a user-authored CR alone.
func TestNamespaceReconcilerClusterOptOut(t *testing.T) {
	reconcile := func(t *testing.T, c client.Client, name string) {
		t.Helper()
		r := &NamespaceReconciler{logger: logr.Discard(), client: c, autoOnboardAll: true, releaseNamespace: "fission"}
		_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
		require.NoError(t, err)
	}
	optedOut := map[string]string{EnabledLabel: EnabledLabelOptOut}

	t.Run("opted-out namespace is not onboarded", func(t *testing.T) {
		c := newFakeClient(t, ns("team-z", optedOut))
		reconcile(t, c, "team-z")
		list := &fv1.FissionTenantList{}
		require.NoError(t, c.List(t.Context(), list))
		assert.Empty(t, list.Items, "fission.io/enabled=false must keep a namespace un-onboarded")
	})

	t.Run("opting out a live namespace offboards its managed tenant", func(t *testing.T) {
		managedFT := &fv1.FissionTenant{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "team-w",
				Annotations:     map[string]string{managedByAnnotation: managedByLabel},
				OwnerReferences: []metav1.OwnerReference{{Kind: "Namespace", Name: "team-w", UID: "team-w-uid"}},
			},
			Spec: fv1.FissionTenantSpec{Namespace: "team-w"},
		}
		c := newFakeClient(t, ns("team-w", optedOut), managedFT)
		reconcile(t, c, "team-w")
		err := c.Get(t.Context(), types.NamespacedName{Name: "team-w"}, &fv1.FissionTenant{})
		assert.True(t, apierrors.IsNotFound(err), "opting out must delete the controller-managed FissionTenant")
	})

	t.Run("opt-out leaves a user-authored tenant alone", func(t *testing.T) {
		c := newFakeClient(t, ns("team-u", optedOut), tenant("user-owned", "team-u"))
		reconcile(t, c, "team-u")
		require.NoError(t, c.Get(t.Context(), types.NamespacedName{Name: "user-owned"}, &fv1.FissionTenant{}),
			"a user-authored FissionTenant must survive the opt-out label")
	})
}

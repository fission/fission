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

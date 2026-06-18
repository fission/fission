// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestEnsureNamespaceRBACCreatesObjects(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()

	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a", "fission", metav1.OwnerReference{}))

	// ServiceAccounts.
	for _, sa := range []string{fv1.FissionFetcherSA, fv1.FissionBuilderSA} {
		got := &corev1.ServiceAccount{}
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: sa}, got), "SA %q must exist", sa)
	}

	// The fetcher/builder/websocket pod SAs are bound to their fixed-name
	// function-access ClusterRoles in the tenant namespace — the rules live in the
	// chart (rendered as the ClusterRoles), so the controller authors no Role.
	cases := []struct {
		binding     string
		sa          string
		clusterRole string
	}{
		{fetcherRoleName, fv1.FissionFetcherSA, fv1.FetcherTenantWorkloadClusterRole},
		{builderRoleName, fv1.FissionBuilderSA, fv1.BuilderTenantWorkloadClusterRole},
		{fetcherWebsocketRoleName, fv1.FissionFetcherSA, fv1.FetcherWebsocketTenantWorkloadClusterRole},
	}
	for _, tc := range cases {
		t.Run(tc.binding, func(t *testing.T) {
			rb := &rbacv1.RoleBinding{}
			require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: tc.binding}, rb), "RoleBinding %q must exist", tc.binding)
			assert.Equal(t, "ClusterRole", rb.RoleRef.Kind, "must bind a ClusterRole")
			assert.Equal(t, tc.clusterRole, rb.RoleRef.Name)
			require.Len(t, rb.Subjects, 1)
			assert.Equal(t, tc.sa, rb.Subjects[0].Name)
			assert.Equal(t, "team-a", rb.Subjects[0].Namespace, "subject SA lives in the tenant namespace")
			assert.Equal(t, managedByValue, rb.Labels[managedByLabelKey], "must be labelled for cleanup")
		})
	}

	// The controller authors no Role of its own — it binds chart ClusterRoles.
	roles := &rbacv1.RoleList{}
	require.NoError(t, c.List(ctx, roles, client.InNamespace("team-a")))
	assert.Empty(t, roles.Items, "controller must not author any Role")
}

// TestEnsureNamespaceRBACBindsWorkloadClusterRoles pins that the executor and
// buildermgr (release-namespace SAs) get a RoleBinding to their workload
// ClusterRole in each tenant namespace, so they can manage workloads there under
// dynamic tenancy. The binding is scoped to the namespace (RoleBinding, not
// ClusterRoleBinding) and the subject points at the release namespace.
func TestEnsureNamespaceRBACBindsWorkloadClusterRoles(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a", "fission", metav1.OwnerReference{}))

	cases := []struct {
		binding     string
		sa          string
		clusterRole string
	}{
		{"fission-executor-workload", fv1.FissionExecutorSA, fv1.ExecutorTenantWorkloadClusterRole},
		{"fission-buildermgr-workload", fv1.FissionBuildermgrSA, fv1.BuildermgrTenantWorkloadClusterRole},
	}
	for _, tc := range cases {
		t.Run(tc.binding, func(t *testing.T) {
			rb := &rbacv1.RoleBinding{}
			require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: tc.binding}, rb))
			assert.Equal(t, "ClusterRole", rb.RoleRef.Kind, "must bind a ClusterRole")
			assert.Equal(t, tc.clusterRole, rb.RoleRef.Name)
			require.Len(t, rb.Subjects, 1)
			assert.Equal(t, tc.sa, rb.Subjects[0].Name)
			assert.Equal(t, "fission", rb.Subjects[0].Namespace, "subject SA lives in the release namespace")
			assert.Equal(t, managedByValue, rb.Labels[managedByLabelKey], "must be labelled for cleanup")
		})
	}
}

// TestEnsureNamespaceRBACSkipsWorkloadBindingsWithoutReleaseNS guards the
// fallback: with no release namespace known, the workload bindings are skipped
// (an unresolvable subject would be useless), leaving the fetcher/builder RBAC.
func TestEnsureNamespaceRBACSkipsWorkloadBindingsWithoutReleaseNS(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a", "", metav1.OwnerReference{}))

	rb := &rbacv1.RoleBinding{}
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: "fission-executor-workload"}, rb)),
		"workload binding must be skipped when the release namespace is unknown")
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: fetcherRoleName}, &rbacv1.RoleBinding{}),
		"the fetcher function-access binding is still provisioned without a release namespace")
}

func TestEnsureNamespaceRBACIsIdempotent(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a", "fission", metav1.OwnerReference{}))
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a", "fission", metav1.OwnerReference{}), "re-running must not error (create-if-absent)")
}

func TestDeleteNamespaceRBACRemovesManaged(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a", "fission", metav1.OwnerReference{}))
	// Provision the derived-key Secret too, so teardown's name-scoped delete of it
	// is exercised (it is the security-load-bearing branch: a leaked-then-offboarded
	// tenant's signing key must not linger).
	require.NoError(t, EnsureNamespaceAuthSecret(ctx, c, []byte("master-bytes"), "team-a"))

	require.NoError(t, DeleteNamespaceRBAC(ctx, c, "team-a"))

	sa := &corev1.ServiceAccount{}
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: fv1.FissionFetcherSA}, sa)), "fetcher SA must be deleted")
	rb := &rbacv1.RoleBinding{}
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: fetcherRoleName}, rb)), "fetcher RoleBinding must be deleted")
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: builderRoleName}, rb)), "builder RoleBinding must be deleted")
	keys := &corev1.Secret{}
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: fv1.TenantAuthKeysSecret}, keys)),
		"derived-key Secret must be deleted by name on teardown")
}

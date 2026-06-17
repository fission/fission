// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestEnsureNamespaceRBACCreatesObjects(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()

	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a", "fission"))

	// ServiceAccounts.
	for _, sa := range []string{fv1.FissionFetcherSA, fv1.FissionBuilderSA} {
		got := &corev1.ServiceAccount{}
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: sa}, got), "SA %q must exist", sa)
	}

	// Roles + RoleBindings (fetcher, builder, fetcher-websocket).
	for _, role := range []string{fetcherRoleName, builderRoleName, fetcherWebsocketRoleName} {
		r := &rbacv1.Role{}
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: role}, r), "Role %q must exist", role)
		assert.Equal(t, managedByValue, r.Labels[managedByLabelKey], "Role must be labelled for cleanup")
		rb := &rbacv1.RoleBinding{}
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: role}, rb), "RoleBinding %q must exist", role)
	}

	// The fetcher Role grants secret/configmap read (the function-access grant).
	fetcher := &rbacv1.Role{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: fetcherRoleName}, fetcher))
	assert.True(t, grantsGet(fetcher, "", "secrets"), "fetcher Role must grant secrets get")
	assert.True(t, grantsGet(fetcher, "fission.io", "packages"), "fetcher Role must grant packages get")
}

// TestEnsureNamespaceRBACBindsWorkloadClusterRoles pins that the executor and
// buildermgr (release-namespace SAs) get a RoleBinding to their workload
// ClusterRole in each tenant namespace, so they can manage workloads there under
// dynamic tenancy. The binding is scoped to the namespace (RoleBinding, not
// ClusterRoleBinding) and the subject points at the release namespace.
func TestEnsureNamespaceRBACBindsWorkloadClusterRoles(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a", "fission"))

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
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a", ""))

	rb := &rbacv1.RoleBinding{}
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: "fission-executor-workload"}, rb)),
		"workload binding must be skipped when the release namespace is unknown")
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: fetcherRoleName}, &rbacv1.Role{}))
}

func TestEnsureNamespaceRBACIsIdempotent(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a", "fission"))
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a", "fission"), "re-running must not error (create-if-absent)")
}

func TestDeleteNamespaceRBACRemovesManaged(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a", "fission"))
	require.NoError(t, DeleteNamespaceRBAC(ctx, c, "team-a"))

	sa := &corev1.ServiceAccount{}
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: fv1.FissionFetcherSA}, sa)), "fetcher SA must be deleted")
	role := &rbacv1.Role{}
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: fetcherRoleName}, role)), "fetcher Role must be deleted")
	rb := &rbacv1.RoleBinding{}
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: builderRoleName}, rb)), "builder RoleBinding must be deleted")
}

func grantsGet(r *rbacv1.Role, apiGroup, resource string) bool {
	for _, rule := range r.Rules {
		if !slices.Contains(rule.APIGroups, apiGroup) || !slices.Contains(rule.Resources, resource) {
			continue
		}
		if slices.Contains(rule.Verbs, "get") {
			return true
		}
	}
	return false
}

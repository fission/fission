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
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestEnsureNamespaceRBACCreatesObjects(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()

	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a"))

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

func TestEnsureNamespaceRBACIsIdempotent(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a"))
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a"), "re-running must not error (create-if-absent)")
}

func TestDeleteNamespaceRBACRemovesManaged(t *testing.T) {
	c := newFakeClient(t)
	ctx := t.Context()
	require.NoError(t, EnsureNamespaceRBAC(ctx, c, "team-a"))
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
		if !contains(rule.APIGroups, apiGroup) || !contains(rule.Resources, resource) {
			continue
		}
		if contains(rule.Verbs, "get") {
			return true
		}
	}
	return false
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestTenantWorkloadBindingAdmissionPolicy verifies the ValidatingAdmissionPolicy
// that hardens the tenant controller's `bind` privilege: a RoleBinding to one of
// the fixed tenant-workload ClusterRoles may bind ONLY the fixed Fission
// ServiceAccounts, never an arbitrary (attacker-controlled) SA. This is the
// defense-in-depth that keeps a compromised controller from granting a
// namespace's secret read to an SA it controls. Rendered only in dynamic mode.
func TestTenantWorkloadBindingAdmissionPolicy(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	f := framework.Connect(t)
	if !f.DynamicNamespacesEnabled(t, ctx) {
		t.Skip("the tenant-workload binding ValidatingAdmissionPolicy is rendered only in dynamic-namespace mode")
	}

	const (
		clusterRole = "fission-fetcher-tenant-workload"
		ns          = "default"
	)
	rb := func(name, saName string) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: ns}},
		}
	}

	// Binding a tenant-workload ClusterRole to a NON-Fission SA must be rejected.
	_, err := f.KubeClient().RbacV1().RoleBindings(ns).Create(ctx, rb("vap-bad-"+framework.RandomID(), "evil-sa"), metav1.CreateOptions{})
	require.Error(t, err, "binding a tenant-workload ClusterRole to a non-fission SA must be denied")
	assert.Contains(t, err.Error(), "may only bind", "rejection should carry the admission-policy message")

	// Binding to the fixed fission-fetcher SA is allowed (the controller's own path).
	good := "vap-good-" + framework.RandomID()
	_, err = f.KubeClient().RbacV1().RoleBindings(ns).Create(ctx, rb(good, fv1.FissionFetcherSA), metav1.CreateOptions{})
	require.NoError(t, err, "binding to the fixed fission-fetcher SA must be allowed")
	t.Cleanup(func() {
		_ = f.KubeClient().RbacV1().RoleBindings(ns).Delete(context.Background(), good, metav1.DeleteOptions{})
	})
}

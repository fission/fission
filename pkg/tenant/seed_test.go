// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fakeversioned "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/utils"
)

func TestSeedTenantsCreatesPerNamespaceWithDefaultMapping(t *testing.T) {
	fc := fakeversioned.NewSimpleClientset()
	nsr := &utils.NamespaceResolver{DefaultNamespace: "default", FunctionNamespace: "fission-fn", BuilderNamespace: "fission-build"}
	nsr.SetTenants(map[string]string{"default": "default", "team-a": "team-a"})

	require.NoError(t, SeedTenants(t.Context(), fc, nsr, logr.Discard()))

	list, err := fc.CoreV1().FissionTenants().List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 2)

	byNS := map[string]fv1.FissionTenant{}
	for _, ft := range list.Items {
		byNS[ft.Spec.Namespace] = ft
	}

	require.Contains(t, byNS, "default")
	assert.Equal(t, "fission-fn", byNS["default"].Spec.FunctionNamespace, "deprecated global override maps onto the default tenant")
	assert.Equal(t, "fission-build", byNS["default"].Spec.BuilderNamespace)
	assert.Equal(t, "helm", byNS["default"].Annotations["fission.io/managed-by"])

	require.Contains(t, byNS, "team-a")
	assert.Empty(t, byNS["team-a"].Spec.FunctionNamespace, "non-default tenants get no global mapping")
}

func TestSeedTenantsIdempotentSkipsAlreadyManaged(t *testing.T) {
	// "custom" already manages team-a under a different name.
	existing := &fv1.FissionTenant{ObjectMeta: metav1.ObjectMeta{Name: "custom"}, Spec: fv1.FissionTenantSpec{Namespace: "team-a"}}
	fc := fakeversioned.NewSimpleClientset(existing)
	nsr := &utils.NamespaceResolver{DefaultNamespace: "default"}
	nsr.SetTenants(map[string]string{"team-a": "team-a"})

	require.NoError(t, SeedTenants(t.Context(), fc, nsr, logr.Discard()))

	list, err := fc.CoreV1().FissionTenants().List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, list.Items, 1, "must not seed a duplicate for an already-managed namespace")
}

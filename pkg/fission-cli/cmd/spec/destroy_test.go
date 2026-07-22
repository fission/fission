// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// TestForceDeleteResourcesRemovesFunctionAliases proves `spec destroy
// --force-delete` (which calls forceDeleteResources with an emptied desired
// state, deleteStale=true) prunes a deployment-UID-annotated FunctionAlias —
// the gap flagged in review: FunctionAlias was missing from all three
// destroy.go call sites.
func TestForceDeleteResourcesRemovesFunctionAliases(t *testing.T) {
	owned := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prod", Namespace: "default",
			Annotations: map[string]string{FISSION_DEPLOYMENT_UID_KEY: testDeployUID},
		},
		Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
	}
	//nolint:staticcheck // FunctionAlias SMD schema not yet generated for NewClientset, see k8s#126850
	fc := fissionfake.NewSimpleClientset(owned)

	// forceDeleteResources is called with the desired state emptied but the
	// DeploymentConfig UID kept, mirroring Destroy's --force-delete branch.
	emptyFr := &FissionResources{}
	emptyFr.DeploymentConfig.UID = testDeployUID

	err := forceDeleteResources(t.Context(), cmd.Client{FissionClientSet: fc}, emptyFr)
	require.NoError(t, err)

	_, err = fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	assert.Error(t, err, "deployment-UID-annotated alias must be pruned by force-delete")
}

// TestDeleteResourcesRemovesFunctionAliases proves plain `spec destroy`
// (deleteResources, which deletes exactly what's declared in fr rather than
// diffing against the cluster) issues a Delete for every FunctionAlias in
// the read spec set — including one that never got an ownerRef (created
// before its Function existed), which k8s garbage collection would not have
// cleaned up on its own.
func TestDeleteResourcesRemovesFunctionAliases(t *testing.T) {
	unowned := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"}, // no ownerRef
		Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
	}
	//nolint:staticcheck // see k8s#126850
	fc := fissionfake.NewSimpleClientset(unowned)

	fr := &FissionResources{
		FunctionAliases: []fv1.FunctionAlias{*unowned},
	}

	err := deleteResources(t.Context(), cmd.Client{FissionClientSet: fc}, fr, false)
	require.NoError(t, err)

	_, err = fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	assert.Error(t, err, "alias declared in the spec must be deleted by `spec destroy`")
}

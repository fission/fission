// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fakeversioned "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// TestWorkflowTypesRegistered pins scheme registration and the generated
// typed clients for the RFC-0022 CRDs: a missing addKnownTypes entry or a
// stale codegen run fails here, not at controller runtime.
func TestWorkflowTypesRegistered(t *testing.T) {
	t.Parallel()
	c := fakeversioned.NewSimpleClientset()

	_, err := c.CoreV1().Workflows("default").Create(t.Context(), &fv1.Workflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf"},
		Spec: fv1.WorkflowSpec{StartAt: "a", States: map[string]fv1.WorkflowState{
			"a": {Type: fv1.WorkflowStateSucceed},
		}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = c.CoreV1().WorkflowRuns("default").Create(t.Context(), &fv1.WorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run"},
		Spec:       fv1.WorkflowRunSpec{WorkflowRef: "wf"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

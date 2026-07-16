// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
)

func makeValidWorkflow() *v1.Workflow {
	return &v1.Workflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf-1", Namespace: "default"},
		Spec: v1.WorkflowSpec{
			StartAt: "a",
			States: map[string]v1.WorkflowState{
				"a": {
					Type:     v1.WorkflowStateTask,
					Function: &v1.FunctionReference{Type: v1.FunctionReferenceTypeFunctionName, Name: "fn"},
					Next:     "done",
				},
				"done": {Type: v1.WorkflowStateSucceed},
			},
		},
	}
}

func TestWorkflowWebhookValidate(t *testing.T) {
	r := &Workflow{}

	assert.NoError(t, r.Validate(makeValidWorkflow()))

	bad := makeValidWorkflow()
	st := bad.Spec.States["a"]
	st.Next = "ghost"
	bad.Spec.States["a"] = st
	err := r.Validate(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghost")
}

func TestWorkflowRunWebhookValidate(t *testing.T) {
	r := &WorkflowRun{}

	ok := &v1.WorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"},
		Spec:       v1.WorkflowRunSpec{WorkflowRef: "wf-1"},
	}
	assert.NoError(t, r.Validate(ok))

	oversized := &v1.WorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-2", Namespace: "default"},
		Spec: v1.WorkflowRunSpec{
			WorkflowRef: "wf-1",
			Input:       &runtime.RawExtension{Raw: bytes.Repeat([]byte("x"), v1.MaxWorkflowRunInputBytes+1)},
		},
	}
	err := r.Validate(oversized)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Input")
}

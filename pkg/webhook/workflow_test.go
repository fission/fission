// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

// TestWorkflowWebhookApplyDefaults pins the "function type defaults to name"
// contract the RFC's worked example relies on: a kubectl-applied manifest
// without function.type must default, then validate.
func TestWorkflowWebhookApplyDefaults(t *testing.T) {
	r := &Workflow{}

	wf := makeValidWorkflow()
	st := wf.Spec.States["a"]
	st.Function = &v1.FunctionReference{Name: "fn"} // no Type
	wf.Spec.States["a"] = st

	require.Error(t, r.Validate(wf), "un-defaulted reference must fail validation")
	require.NoError(t, r.ApplyDefaults(wf))
	assert.EqualValues(t, v1.FunctionReferenceTypeFunctionName, wf.Spec.States["a"].Function.Type)
	assert.NoError(t, r.Validate(wf))
}

func TestWorkflowRunSpecImmutable(t *testing.T) {
	r := &WorkflowRun{}

	old := &v1.WorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"},
		Spec:       v1.WorkflowRunSpec{WorkflowRef: "wf-1"},
	}

	same := old.DeepCopy()
	same.Annotations = map[string]string{"fission.io/cancel-requested": "true"}
	assert.NoError(t, r.ValidateTransition(old, same), "annotations (cancel) stay mutable")

	changed := old.DeepCopy()
	changed.Spec.WorkflowRef = "wf-2"
	err := r.ValidateTransition(old, changed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "immutable")
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
			Input:       &apiextensionsv1.JSON{Raw: bytes.Repeat([]byte("x"), v1.MaxWorkflowRunInputBytes+1)},
		},
	}
	err := r.Validate(oversized)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Input")
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func wfTask(next string, end bool) WorkflowState {
	return WorkflowState{
		Type:     WorkflowStateTask,
		Function: &FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "fn"},
		Next:     next,
		End:      end,
	}
}

// wfBase is a minimal valid two-state machine: a Task then a Succeed.
func wfBase() WorkflowSpec {
	return WorkflowSpec{
		StartAt: "a",
		States: map[string]WorkflowState{
			"a":    wfTask("done", false),
			"done": {Type: WorkflowStateSucceed},
		},
	}
}

func TestWorkflowSpecValidate(t *testing.T) {
	t.Parallel()

	leaf := WorkflowChoiceCondition{Variable: "$.x", IsPresent: new(true)}

	cases := []struct {
		name    string
		mutate  func(*WorkflowSpec)
		wantErr string // substring; "" = valid
	}{
		{"valid linear", nil, ""},
		{"valid choice", func(s *WorkflowSpec) {
			s.States["c"] = WorkflowState{
				Type:    WorkflowStateChoice,
				Choices: []WorkflowChoiceRule{{WorkflowChoiceCondition: leaf, Next: "done"}},
				Default: "done",
			}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, ""},
		{"valid composite choice", func(s *WorkflowSpec) {
			s.States["c"] = WorkflowState{
				Type: WorkflowStateChoice,
				Choices: []WorkflowChoiceRule{{
					And:  []WorkflowChoiceCondition{leaf, {Variable: "$.y", StringEquals: new("ok")}},
					Next: "done",
				}},
				Default: "done",
			}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, ""},
		{"valid catch and cycle", func(s *WorkflowSpec) {
			// a cycle through a catch route is legal; the run Timeout bounds it.
			st := s.States["a"]
			st.Catch = []WorkflowCatchRoute{{ErrorType: WorkflowErrAll, Next: "a"}}
			s.States["a"] = st
		}, ""},

		{"empty states", func(s *WorkflowSpec) { s.States = nil }, "state"},
		{"startAt empty", func(s *WorkflowSpec) { s.StartAt = "" }, "StartAt"},
		{"startAt undeclared", func(s *WorkflowSpec) { s.StartAt = "ghost" }, "StartAt"},
		{"next target undeclared", func(s *WorkflowSpec) {
			st := s.States["a"]
			st.Next = "ghost"
			s.States["a"] = st
		}, "ghost"},
		{"unreachable state", func(s *WorkflowSpec) {
			s.States["orphan"] = wfTask("", true)
		}, "unreachable"},
		{"task with neither next nor end", func(s *WorkflowSpec) {
			st := s.States["a"]
			st.Next = ""
			st.End = false
			s.States["a"] = st
		}, "exactly one of Next or End"},
		{"task with both next and end", func(s *WorkflowSpec) {
			st := s.States["a"]
			st.End = true
			s.States["a"] = st
		}, "exactly one of Next or End"},
		{"task without function", func(s *WorkflowSpec) {
			st := s.States["a"]
			st.Function = nil
			s.States["a"] = st
		}, "Function"},
		{"succeed with next", func(s *WorkflowSpec) {
			s.States["done"] = WorkflowState{Type: WorkflowStateSucceed, Next: "a"}
		}, "terminal"},
		{"fail with retry", func(s *WorkflowSpec) {
			s.States["done"] = WorkflowState{Type: WorkflowStateFail, Retry: &RetryPolicy{}}
		}, "Fail state"},
		{"choice without rules", func(s *WorkflowSpec) {
			s.States["c"] = WorkflowState{Type: WorkflowStateChoice, Default: "done"}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, "Choices"},
		{"choice with function", func(s *WorkflowSpec) {
			s.States["c"] = WorkflowState{
				Type:     WorkflowStateChoice,
				Function: &FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "fn"},
				Choices:  []WorkflowChoiceRule{{WorkflowChoiceCondition: leaf, Next: "done"}},
				Default:  "done",
			}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, "Choice state"},
		{"choice with next", func(s *WorkflowSpec) {
			s.States["c"] = WorkflowState{
				Type:    WorkflowStateChoice,
				Choices: []WorkflowChoiceRule{{WorkflowChoiceCondition: leaf, Next: "done"}},
				Next:    "done",
			}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, "Choice state"},
		{"choice rule two operators", func(s *WorkflowSpec) {
			bad := leaf
			bad.StringEquals = new("x")
			s.States["c"] = WorkflowState{
				Type:    WorkflowStateChoice,
				Choices: []WorkflowChoiceRule{{WorkflowChoiceCondition: bad, Next: "done"}},
				Default: "done",
			}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, "exactly one comparison operator"},
		{"choice rule no operator", func(s *WorkflowSpec) {
			s.States["c"] = WorkflowState{
				Type:    WorkflowStateChoice,
				Choices: []WorkflowChoiceRule{{WorkflowChoiceCondition: WorkflowChoiceCondition{Variable: "$.x"}, Next: "done"}},
				Default: "done",
			}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, "exactly one comparison operator"},
		{"choice leaf without variable", func(s *WorkflowSpec) {
			s.States["c"] = WorkflowState{
				Type:    WorkflowStateChoice,
				Choices: []WorkflowChoiceRule{{WorkflowChoiceCondition: WorkflowChoiceCondition{IsPresent: new(true)}, Next: "done"}},
				Default: "done",
			}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, "Variable"},
		{"choice composite with inline leaf", func(s *WorkflowSpec) {
			s.States["c"] = WorkflowState{
				Type: WorkflowStateChoice,
				Choices: []WorkflowChoiceRule{{
					WorkflowChoiceCondition: leaf,
					And:                     []WorkflowChoiceCondition{leaf},
					Next:                    "done",
				}},
				Default: "done",
			}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, "either a leaf"},
		{"choice composite two of and/or/not", func(s *WorkflowSpec) {
			s.States["c"] = WorkflowState{
				Type: WorkflowStateChoice,
				Choices: []WorkflowChoiceRule{{
					And:  []WorkflowChoiceCondition{leaf},
					Or:   []WorkflowChoiceCondition{leaf},
					Next: "done",
				}},
				Default: "done",
			}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, "exactly one of"},
		{"choice rule no next", func(s *WorkflowSpec) {
			s.States["c"] = WorkflowState{
				Type:    WorkflowStateChoice,
				Choices: []WorkflowChoiceRule{{WorkflowChoiceCondition: leaf}},
				Default: "done",
			}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, "Next"},
		{"choice rule bad next", func(s *WorkflowSpec) {
			s.States["c"] = WorkflowState{
				Type:    WorkflowStateChoice,
				Choices: []WorkflowChoiceRule{{WorkflowChoiceCondition: leaf, Next: "ghost"}},
				Default: "done",
			}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, "ghost"},
		{"choice bad default", func(s *WorkflowSpec) {
			s.States["c"] = WorkflowState{
				Type:    WorkflowStateChoice,
				Choices: []WorkflowChoiceRule{{WorkflowChoiceCondition: leaf, Next: "done"}},
				Default: "ghost",
			}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, "ghost"},
		{"catch bad target", func(s *WorkflowSpec) {
			st := s.States["a"]
			st.Catch = []WorkflowCatchRoute{{ErrorType: WorkflowErrAll, Next: "ghost"}}
			s.States["a"] = st
		}, "ghost"},
		{"catch empty errorType", func(s *WorkflowSpec) {
			st := s.States["a"]
			st.Catch = []WorkflowCatchRoute{{ErrorType: "", Next: "done"}}
			s.States["a"] = st
		}, "ErrorType"},
		{"catch duplicate errorType", func(s *WorkflowSpec) {
			st := s.States["a"]
			st.Catch = []WorkflowCatchRoute{
				{ErrorType: WorkflowErrAll, Next: "done"},
				{ErrorType: WorkflowErrAll, Next: "done"},
			}
			s.States["a"] = st
		}, "duplicate"},
		{"bad inputPath", func(s *WorkflowSpec) {
			st := s.States["a"]
			st.InputPath = "not-a-path"
			s.States["a"] = st
		}, "jsonpath"},
		{"bad variable jsonpath", func(s *WorkflowSpec) {
			s.States["c"] = WorkflowState{
				Type:    WorkflowStateChoice,
				Choices: []WorkflowChoiceRule{{WorkflowChoiceCondition: WorkflowChoiceCondition{Variable: "nope", IsPresent: new(true)}, Next: "done"}},
				Default: "done",
			}
			st := s.States["a"]
			st.Next = "c"
			s.States["a"] = st
		}, "jsonpath"},
		{"retry maxAttempts over cap", func(s *WorkflowSpec) {
			s.DefaultRetry = &RetryPolicy{MaxAttempts: new(MaxWorkflowAttempts + 1)}
		}, "MaxAttempts"},
		{"retry maxAttempts zero", func(s *WorkflowSpec) {
			s.DefaultRetry = &RetryPolicy{MaxAttempts: new(0)}
		}, "MaxAttempts"},
		{"retry cap below base", func(s *WorkflowSpec) {
			s.DefaultRetry = &RetryPolicy{
				BackoffBase: &metav1.Duration{Duration: 10 * time.Second},
				BackoffCap:  &metav1.Duration{Duration: time.Second},
			}
		}, "BackoffCap"},
		{"timeout non-positive", func(s *WorkflowSpec) {
			s.Timeout = &metav1.Duration{Duration: -time.Second}
		}, "Timeout"},
		{"state timeout non-positive", func(s *WorkflowSpec) {
			st := s.States["a"]
			st.Timeout = &metav1.Duration{Duration: 0}
			s.States["a"] = st
		}, "Timeout"},
		{"no reachable terminal", func(s *WorkflowSpec) {
			// a -> b -> a with no End/Succeed/Fail anywhere.
			s.States = map[string]WorkflowState{
				"a": wfTask("b", false),
				"b": wfTask("a", false),
			}
		}, "terminal"},
		{"over MaxWorkflowStates", func(s *WorkflowSpec) {
			for i := range MaxWorkflowStates + 1 {
				s.States[fmt.Sprintf("s%d", i)] = wfTask("done", false)
			}
		}, "states"},
		{"unknown state type", func(s *WorkflowSpec) {
			s.States["w"] = WorkflowState{Type: "Wait", Next: "done"}
			st := s.States["a"]
			st.Next = "w"
			s.States["a"] = st
		}, "Type"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := wfBase()
			if tc.mutate != nil {
				tc.mutate(&s)
			}
			err := s.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestWorkflowRunSpecValidate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		spec    WorkflowRunSpec
		wantErr string
	}{
		{"valid", WorkflowRunSpec{WorkflowRef: "wf"}, ""},
		{"valid with input", WorkflowRunSpec{
			WorkflowRef: "wf",
			Input:       &runtime.RawExtension{Raw: []byte(`{"a":1}`)},
		}, ""},
		{"missing workflowRef", WorkflowRunSpec{}, "WorkflowRef"},
		{"negative generation", WorkflowRunSpec{WorkflowRef: "wf", WorkflowGeneration: -1}, "WorkflowGeneration"},
		{"input at cap", WorkflowRunSpec{
			WorkflowRef: "wf",
			Input:       &runtime.RawExtension{Raw: bytes.Repeat([]byte("x"), MaxWorkflowRunInputBytes)},
		}, ""},
		{"input over cap", WorkflowRunSpec{
			WorkflowRef: "wf",
			Input:       &runtime.RawExtension{Raw: bytes.Repeat([]byte("x"), MaxWorkflowRunInputBytes+1)},
		}, "Input"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.spec.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestWorkflowValidateForAdmission pins that admission validation includes
// metadata checks on top of the spec rules.
func TestWorkflowValidateForAdmission(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		ObjectMeta: metav1.ObjectMeta{Name: "Bad_Name", Namespace: "default"},
		Spec:       wfBase(),
	}
	require.Error(t, w.ValidateForAdmission())

	w.Name = "good-name"
	assert.NoError(t, w.ValidateForAdmission())

	wr := &WorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"},
		Spec:       WorkflowRunSpec{WorkflowRef: "wf"},
	}
	assert.NoError(t, wr.ValidateForAdmission())
}

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
		}, "must not set Next"},
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
			s.States["w"] = WorkflowState{Type: "Teleport", Next: "done"}
			st := s.States["a"]
			st.Next = "w"
			s.States["a"] = st
		}, "Type"},
		{"valid wait", func(s *WorkflowSpec) {
			s.States["w"] = WorkflowState{Type: WorkflowStateWait, Duration: &metav1.Duration{Duration: time.Minute}, Next: "done"}
			st := s.States["a"]
			st.Next = "w"
			s.States["a"] = st
		}, ""},
		{"wait without duration", func(s *WorkflowSpec) {
			s.States["w"] = WorkflowState{Type: WorkflowStateWait, Next: "done"}
			st := s.States["a"]
			st.Next = "w"
			s.States["a"] = st
		}, "Duration"},
		{"wait with function", func(s *WorkflowSpec) {
			s.States["w"] = WorkflowState{
				Type: WorkflowStateWait, Duration: &metav1.Duration{Duration: time.Minute},
				Function: &FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "fn"},
				Next:     "done",
			}
			st := s.States["a"]
			st.Next = "w"
			s.States["a"] = st
		}, "must not set Function"},
		{"task with function-weights reference", func(s *WorkflowSpec) {
			// Legal on HTTPTriggers, unexecutable by the workflow engine —
			// must be rejected at admission, not discovered at run time.
			st := s.States["a"]
			st.Function = &FunctionReference{
				Type:            FunctionReferenceTypeFunctionWeights,
				Name:            "fn",
				FunctionWeights: map[string]int{"fn": 100},
			}
			s.States["a"] = st
		}, "by name"},
		{"valid parallel", func(s *WorkflowSpec) {
			s.States["fan"] = WorkflowState{
				Type: WorkflowStateParallel,
				Branches: []WorkflowBranch{
					{StartAt: "x", States: map[string]WorkflowBranchState{
						"x": {Type: WorkflowStateTask, Function: &FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "fn"}, End: true},
					}},
					{StartAt: "y", States: map[string]WorkflowBranchState{
						"y": {Type: WorkflowStateSucceed},
					}},
				},
				Catch: []WorkflowCatchRoute{{ErrorType: WorkflowErrBranchFailed, Next: "done"}},
				Next:  "done",
			}
			st := s.States["a"]
			st.Next = "fan"
			s.States["a"] = st
		}, ""},
		{"valid map", func(s *WorkflowSpec) {
			s.States["each"] = WorkflowState{
				Type:      WorkflowStateMap,
				ItemsPath: "$.items",
				Branches: []WorkflowBranch{
					{StartAt: "x", States: map[string]WorkflowBranchState{
						"x": {Type: WorkflowStateTask, Function: &FunctionReference{Type: FunctionReferenceTypeFunctionName, Name: "fn"}, End: true},
					}},
				},
				MaxConcurrency: 3,
				Next:           "done",
			}
			st := s.States["a"]
			st.Next = "each"
			s.States["a"] = st
		}, ""},
		{"parallel without branches", func(s *WorkflowSpec) {
			s.States["fan"] = WorkflowState{Type: WorkflowStateParallel, Next: "done"}
			st := s.States["a"]
			st.Next = "fan"
			s.States["a"] = st
		}, "at least one branch"},
		{"parallel with retry", func(s *WorkflowSpec) {
			s.States["fan"] = WorkflowState{
				Type:  WorkflowStateParallel,
				Retry: &RetryPolicy{MaxAttempts: new(2)},
				Branches: []WorkflowBranch{{StartAt: "x", States: map[string]WorkflowBranchState{
					"x": {Type: WorkflowStateSucceed},
				}}},
				Next: "done",
			}
			st := s.States["a"]
			st.Next = "fan"
			s.States["a"] = st
		}, "must not set Retry"},
		{"map without itemsPath", func(s *WorkflowSpec) {
			s.States["each"] = WorkflowState{
				Type: WorkflowStateMap,
				Branches: []WorkflowBranch{{StartAt: "x", States: map[string]WorkflowBranchState{
					"x": {Type: WorkflowStateSucceed},
				}}},
				End: true,
			}
			st := s.States["a"]
			st.Next = "each"
			s.States["a"] = st
		}, "ItemsPath"},
		{"map with two branches", func(s *WorkflowSpec) {
			br := WorkflowBranch{StartAt: "x", States: map[string]WorkflowBranchState{"x": {Type: WorkflowStateSucceed}}}
			s.States["each"] = WorkflowState{
				Type: WorkflowStateMap, ItemsPath: "$.items",
				Branches: []WorkflowBranch{br, br}, End: true,
			}
			st := s.States["a"]
			st.Next = "each"
			s.States["a"] = st
		}, "exactly one branch"},
		{"branch graph with unreachable state", func(s *WorkflowSpec) {
			s.States["fan"] = WorkflowState{
				Type: WorkflowStateParallel,
				Branches: []WorkflowBranch{{StartAt: "x", States: map[string]WorkflowBranchState{
					"x":      {Type: WorkflowStateSucceed},
					"orphan": {Type: WorkflowStateSucceed},
				}}},
				Next: "done",
			}
			st := s.States["a"]
			st.Next = "fan"
			s.States["a"] = st
		}, "unreachable"},
		{"branch state cannot be parallel", func(s *WorkflowSpec) {
			s.States["fan"] = WorkflowState{
				Type: WorkflowStateParallel,
				Branches: []WorkflowBranch{{StartAt: "x", States: map[string]WorkflowBranchState{
					"x": {Type: WorkflowStateParallel, Next: "x"},
				}}},
				Next: "done",
			}
			st := s.States["a"]
			st.Next = "fan"
			s.States["a"] = st
		}, "nested fan-out"},
		{"state name with illegal characters", func(s *WorkflowSpec) {
			// Names become durable identifiers (event stream, activeStates,
			// mermaid output) — the grammar is pinned before phase 2.
			s.States["bad name: x --> y"] = wfTask("", true)
			st := s.States["a"]
			st.Next = "bad name: x --> y"
			s.States["a"] = st
		}, "state names"},
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

// TestWorkflowObjectValidate pins that object-level validation (what the
// admission webhooks enforce) includes metadata checks on top of the spec
// rules.
func TestWorkflowObjectValidate(t *testing.T) {
	t.Parallel()

	w := &Workflow{
		ObjectMeta: metav1.ObjectMeta{Name: "Bad_Name", Namespace: "default"},
		Spec:       wfBase(),
	}
	require.Error(t, w.Validate())

	w.Name = "good-name"
	assert.NoError(t, w.Validate())

	wr := &WorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"},
		Spec:       WorkflowRunSpec{WorkflowRef: "wf"},
	}
	assert.NoError(t, wr.Validate())
}

// TestWorkflowSpecApplyDefaults pins the "function type defaults to name"
// contract the RFC's worked example relies on.
func TestWorkflowSpecApplyDefaults(t *testing.T) {
	t.Parallel()

	s := wfBase()
	st := s.States["a"]
	st.Function = &FunctionReference{Name: "fn"} // no Type
	s.States["a"] = st

	require.Error(t, s.Validate(), "un-defaulted reference must fail validation")
	s.ApplyDefaults()
	assert.EqualValues(t, FunctionReferenceTypeFunctionName, s.States["a"].Function.Type)
	assert.NoError(t, s.Validate())
}

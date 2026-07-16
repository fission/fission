// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// fanSpec is a machine with one Parallel state of two single-task branches:
// start -> fan(b0: x, b1: y) -> done.
func fanSpec() *fv1.WorkflowSpec {
	fn := func(name string) *fv1.FunctionReference {
		return &fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: name}
	}
	branch := func(state, fnName string) fv1.WorkflowBranch {
		return fv1.WorkflowBranch{StartAt: state, States: map[string]fv1.WorkflowBranchState{
			state: {Type: fv1.WorkflowStateTask, Function: fn(fnName), End: true},
		}}
	}
	return &fv1.WorkflowSpec{
		StartAt: "fan",
		States: map[string]fv1.WorkflowState{
			"fan": {
				Type:     fv1.WorkflowStateParallel,
				Branches: []fv1.WorkflowBranch{branch("x", "fn-x"), branch("y", "fn-y")},
				Next:     "done",
			},
			"done": {Type: fv1.WorkflowStateSucceed},
		},
	}
}

func TestFoldParallelJoin(t *testing.T) {
	t.Parallel()

	s := newRunState()
	log := wfLog(t,
		Event{Type: EvRunStarted, Spec: fanSpec(), Input: json.RawMessage(`{"n":1}`)},
		Event{Type: EvStepScheduled, Branch: "0", State: "x", Attempt: 1},
		Event{Type: EvStepScheduled, Branch: "1", State: "y", Attempt: 1},
		Event{Type: EvStepSucceeded, Branch: "1", State: "y", Attempt: 1, Output: json.RawMessage(`"y-out"`)},
		Event{Type: EvStepSucceeded, Branch: "0", State: "x", Attempt: 1, Output: json.RawMessage(`"x-out"`)},
	)
	require.NoError(t, s.fold(log, nil))

	require.NotNil(t, s.BranchRuns)
	assert.True(t, s.BranchRuns["0"].PendingCompletion)
	assert.True(t, s.BranchRuns["1"].PendingCompletion)
	assert.Equal(t, "fan", s.Current)

	// The join closes the region and advances.
	join := wfLog(t, Event{Type: EvBranchesJoined, Output: json.RawMessage(`["x-out","y-out"]`)})
	join[0].Seq = int64(len(log) + 1)
	require.NoError(t, s.fold(join, nil))
	assert.Nil(t, s.BranchRuns)
	assert.True(t, s.PendingCompletion, "advanced through done (Succeed)")
	assert.Equal(t, json.RawMessage(`["x-out","y-out"]`), s.Doc)
}

func TestFoldParallelFailFast(t *testing.T) {
	t.Parallel()

	t.Run("without catch the run fails with BranchFailed", func(t *testing.T) {
		t.Parallel()
		s := newRunState()
		require.NoError(t, s.fold(wfLog(t,
			Event{Type: EvRunStarted, Spec: fanSpec(), Input: json.RawMessage(`{}`)},
			Event{Type: EvStepScheduled, Branch: "0", State: "x", Attempt: 1},
			Event{Type: EvStepFailed, Branch: "0", State: "x", Attempt: 1, ErrorType: fv1.WorkflowErrPermanentError},
		), nil))
		assert.Equal(t, fv1.WorkflowErrBranchFailed, s.PendingError)
		assert.Nil(t, s.BranchRuns, "fail-fast dissolved the region")

		var cause map[string]any
		require.NoError(t, json.Unmarshal(s.Cause, &cause))
		assert.Equal(t, "0", cause["branch"])
		assert.Equal(t, fv1.WorkflowErrPermanentError, cause["errorType"])
	})

	t.Run("a catch on the fan-out state routes BranchFailed", func(t *testing.T) {
		t.Parallel()
		spec := fanSpec()
		fanState := spec.States["fan"]
		fanState.Catch = []fv1.WorkflowCatchRoute{{ErrorType: fv1.WorkflowErrBranchFailed, Next: "done"}}
		spec.States["fan"] = fanState

		s := newRunState()
		require.NoError(t, s.fold(wfLog(t,
			Event{Type: EvRunStarted, Spec: spec, Input: json.RawMessage(`{}`)},
			Event{Type: EvStepScheduled, Branch: "1", State: "y", Attempt: 1},
			Event{Type: EvStepFailed, Branch: "1", State: "y", Attempt: 1, ErrorType: fv1.WorkflowErrPermanentError},
		), nil))
		assert.Empty(t, s.PendingError)
		assert.True(t, s.PendingCompletion, "catch routed to done")
	})
}

func TestFoldMapFanOut(t *testing.T) {
	t.Parallel()

	spec := fanSpec()
	spec.States["fan"] = fv1.WorkflowState{
		Type:      fv1.WorkflowStateMap,
		ItemsPath: "$.items",
		Branches:  []fv1.WorkflowBranch{spec.States["fan"].Branches[0]},
		Next:      "done",
	}

	s := newRunState()
	require.NoError(t, s.fold(wfLog(t,
		Event{Type: EvRunStarted, Spec: spec, Input: json.RawMessage(`{"items":[10,20,30]}`)},
	), nil))

	require.Len(t, s.BranchRuns, 3, "one branch per item")
	assert.Equal(t, json.RawMessage(`10`), s.BranchRuns["0"].Doc)
	assert.Equal(t, json.RawMessage(`30`), s.BranchRuns["2"].Doc)

	t.Run("non-array itemsPath is a permanent error", func(t *testing.T) {
		t.Parallel()
		s := newRunState()
		require.NoError(t, s.fold(wfLog(t,
			Event{Type: EvRunStarted, Spec: spec, Input: json.RawMessage(`{"items":"nope"}`)},
		), nil))
		assert.Equal(t, fv1.WorkflowErrPermanentError, s.PendingError)
	})
}

func TestFoldBranchCorruption(t *testing.T) {
	t.Parallel()

	base := Event{Type: EvRunStarted, Spec: fanSpec(), Input: json.RawMessage(`{}`)}
	okBoth := []Event{base,
		{Type: EvStepScheduled, Branch: "0", State: "x", Attempt: 1},
		{Type: EvStepSucceeded, Branch: "0", State: "x", Attempt: 1, Output: json.RawMessage(`1`)},
		{Type: EvStepScheduled, Branch: "1", State: "y", Attempt: 1},
		{Type: EvStepSucceeded, Branch: "1", State: "y", Attempt: 1, Output: json.RawMessage(`2`)},
	}

	cases := []struct {
		name string
		log  []Event
	}{
		{"join before all branches ok (W7)", []Event{base,
			{Type: EvStepScheduled, Branch: "0", State: "x", Attempt: 1},
			{Type: EvBranchesJoined, Output: json.RawMessage(`[]`)}}},
		{"branch step event after join (W8)", append(append([]Event{}, okBoth...),
			Event{Type: EvBranchesJoined, Output: json.RawMessage(`[1,2]`)},
			Event{Type: EvStepScheduled, Branch: "0", State: "x", Attempt: 2})},
		{"event for undeclared branch", []Event{base,
			{Type: EvStepScheduled, Branch: "7", State: "x", Attempt: 1}}},
		{"join without a region", []Event{
			{Type: EvRunStarted, Spec: pipelineSpec(), Input: json.RawMessage(`{}`)},
			{Type: EvBranchesJoined, Output: json.RawMessage(`[]`)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newRunState()
			assert.Error(t, s.fold(wfLog(t, tc.log...), nil))
		})
	}

	t.Run("late branch TimerFired after join is ignored", func(t *testing.T) {
		t.Parallel()
		s := newRunState()
		log := append(append([]Event{}, okBoth...),
			Event{Type: EvBranchesJoined, Output: json.RawMessage(`[1,2]`)},
			Event{Type: EvTimerFired, Branch: "0", State: "x", Attempt: 1})
		require.NoError(t, s.fold(wfLog(t, log...), nil))
		assert.True(t, s.PendingCompletion)
	})

	t.Run("draining sibling result after fail-fast is ignored", func(t *testing.T) {
		t.Parallel()
		// Branch 0 fails terminally (region dissolves, fail-fast); branch 1
		// was in flight and its result lands before the terminal — the
		// documented deviation from strict W8 (appendGuarded retries at head
		// where the model replans). The straggler must not poison the fold.
		s := newRunState()
		require.NoError(t, s.fold(wfLog(t, base,
			Event{Type: EvStepScheduled, Branch: "0", State: "x", Attempt: 1},
			Event{Type: EvStepScheduled, Branch: "1", State: "y", Attempt: 1},
			Event{Type: EvStepFailed, Branch: "0", State: "x", Attempt: 1, ErrorType: fv1.WorkflowErrPermanentError},
			Event{Type: EvStepSucceeded, Branch: "1", State: "y", Attempt: 1, Output: json.RawMessage(`2`)},
			Event{Type: EvRunFailed, ErrorType: fv1.WorkflowErrBranchFailed},
		), nil))
		assert.Equal(t, fv1.WorkflowRunFailed, s.Terminal)
	})
}

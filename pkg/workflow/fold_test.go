// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
)

// wfLog encodes events into a numbered stream for fold tests.
func wfLog(t *testing.T, events ...Event) []statestore.Event {
	t.Helper()
	out := make([]statestore.Event, 0, len(events))
	for i, e := range events {
		se, err := encodeEvent(e)
		require.NoError(t, err)
		se.Seq = int64(i + 1)
		out = append(out, se)
	}
	return out
}

// pipelineSpec is a 2-task linear machine: a -> b -> done.
func pipelineSpec() *fv1.WorkflowSpec {
	fn := func(name string) *fv1.FunctionReference {
		return &fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: name}
	}
	return &fv1.WorkflowSpec{
		StartAt: "a",
		States: map[string]fv1.WorkflowState{
			"a":    {Type: fv1.WorkflowStateTask, Function: fn("fn-a"), Next: "b"},
			"b":    {Type: fv1.WorkflowStateTask, Function: fn("fn-b"), Next: "done"},
			"done": {Type: fv1.WorkflowStateSucceed},
		},
	}
}

func TestFoldLinearSuccess(t *testing.T) {
	t.Parallel()

	s := newRunState()
	log := wfLog(t,
		Event{Type: EvRunStarted, Spec: pipelineSpec(), Input: json.RawMessage(`{"n":1}`)},
		Event{Type: EvStepScheduled, State: "a", Attempt: 1},
		Event{Type: EvStepSucceeded, State: "a", Attempt: 1, Output: json.RawMessage(`{"n":2}`)},
		Event{Type: EvStepScheduled, State: "b", Attempt: 1},
		Event{Type: EvStepSucceeded, State: "b", Attempt: 1, Output: json.RawMessage(`{"n":3}`)},
	)
	require.NoError(t, s.fold(log, nil))

	assert.Equal(t, "", s.Current, "advanced through the Succeed state")
	assert.True(t, s.PendingCompletion)
	assert.Equal(t, json.RawMessage(`{"n":3}`), s.Doc)
	assert.Equal(t, fv1.WorkflowRunPhase(""), s.Terminal)

	// Terminal event closes the run.
	term := wfLog(t, Event{Type: EvRunSucceeded, Output: json.RawMessage(`{"n":3}`)})
	term[0].Seq = 6
	require.NoError(t, s.fold(term, nil))
	assert.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)
}

func TestFoldChoiceRouting(t *testing.T) {
	t.Parallel()

	spec := pipelineSpec()
	spec.States["route"] = fv1.WorkflowState{
		Type: fv1.WorkflowStateChoice,
		Choices: []fv1.WorkflowChoiceRule{{
			WorkflowChoiceCondition: fv1.WorkflowChoiceCondition{Variable: "$.big", IsPresent: new(true)},
			Next:                    "b",
		}},
		Default: "done",
	}
	a := spec.States["a"]
	a.Next = "route"
	spec.States["a"] = a

	fold := func(t *testing.T, output string) *RunState {
		s := newRunState()
		require.NoError(t, s.fold(wfLog(t,
			Event{Type: EvRunStarted, Spec: spec, Input: json.RawMessage(`{}`)},
			Event{Type: EvStepScheduled, State: "a", Attempt: 1},
			Event{Type: EvStepSucceeded, State: "a", Attempt: 1, Output: json.RawMessage(output)},
		), nil))
		return s
	}

	assert.Equal(t, "b", fold(t, `{"big":true}`).Current, "rule matched")
	s := fold(t, `{"small":true}`)
	assert.True(t, s.PendingCompletion, "default routed to Succeed")
}

func TestFoldRetryThenTerminalFailure(t *testing.T) {
	t.Parallel()

	spec := pipelineSpec()
	a := spec.States["a"]
	a.Retry = &fv1.RetryPolicy{MaxAttempts: new(2)}
	spec.States["a"] = a

	s := newRunState()
	require.NoError(t, s.fold(wfLog(t,
		Event{Type: EvRunStarted, Spec: spec, Input: json.RawMessage(`{}`)},
		Event{Type: EvStepScheduled, State: "a", Attempt: 1},
		Event{Type: EvStepFailed, State: "a", Attempt: 1, ErrorType: fv1.WorkflowErrFunctionError},
		Event{Type: EvTimerFired, State: "a", Attempt: 1},
		Event{Type: EvStepScheduled, State: "a", Attempt: 2},
		Event{Type: EvStepFailed, State: "a", Attempt: 2, ErrorType: fv1.WorkflowErrFunctionError},
		Event{Type: EvRunFailed, ErrorType: fv1.WorkflowErrFunctionError},
	), nil))

	assert.Equal(t, fv1.WorkflowRunFailed, s.Terminal)
	assert.Equal(t, int32(2), s.Attempts["a"])
	assert.True(t, s.TimersFired["a/1"])
}

func TestFoldCorruptionFailsLoud(t *testing.T) {
	t.Parallel()

	base := Event{Type: EvRunStarted, Spec: pipelineSpec(), Input: json.RawMessage(`{}`)}

	cases := []struct {
		name string
		log  []Event
	}{
		{"result without schedule (W3)", []Event{base,
			{Type: EvStepSucceeded, State: "a", Attempt: 1, Output: json.RawMessage(`{}`)}}},
		{"duplicate result (W2)", []Event{base,
			{Type: EvStepScheduled, State: "a", Attempt: 1},
			{Type: EvStepFailed, State: "a", Attempt: 1, ErrorType: fv1.WorkflowErrAll},
			{Type: EvStepSucceeded, State: "a", Attempt: 1, Output: json.RawMessage(`{}`)}}},
		{"duplicate schedule (W1)", []Event{base,
			{Type: EvStepScheduled, State: "a", Attempt: 1},
			{Type: EvStepScheduled, State: "a", Attempt: 1}}},
		{"attempt skip (W5)", []Event{base,
			{Type: EvStepScheduled, State: "a", Attempt: 2}}},
		{"event after terminal (W4)", []Event{base,
			{Type: EvRunCancelled},
			{Type: EvStepScheduled, State: "a", Attempt: 1}}},
		{"schedule for non-current state", []Event{base,
			{Type: EvStepScheduled, State: "b", Attempt: 1}}},
		{"duplicate RunStarted", []Event{base, base}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newRunState()
			assert.Error(t, s.fold(wfLog(t, tc.log...), nil))
		})
	}
}

// TestFoldDeterminism is the RFC's replay property: folding any valid prefix
// then the rest equals folding the whole, and repeated folds are identical.
func TestFoldDeterminism(t *testing.T) {
	t.Parallel()

	full := []Event{
		{Type: EvRunStarted, Spec: pipelineSpec(), Input: json.RawMessage(`{"n":1}`)},
		{Type: EvStepScheduled, State: "a", Attempt: 1},
		{Type: EvStepFailed, State: "a", Attempt: 1, ErrorType: fv1.WorkflowErrFunctionError},
		{Type: EvTimerFired, State: "a", Attempt: 1},
		{Type: EvStepScheduled, State: "a", Attempt: 2},
		{Type: EvStepSucceeded, State: "a", Attempt: 2, Output: json.RawMessage(`{"n":2}`)},
		{Type: EvStepScheduled, State: "b", Attempt: 1},
		{Type: EvStepSucceeded, State: "b", Attempt: 1, Output: json.RawMessage(`{"n":3}`)},
		{Type: EvRunSucceeded, Output: json.RawMessage(`{"n":3}`)},
	}

	rapid.Check(t, func(rt *rapid.T) {
		log := wfLog(t, full...)
		cut := rapid.IntRange(0, len(log)).Draw(rt, "cut")

		whole := newRunState()
		require.NoError(rt, whole.fold(log, nil))

		split := newRunState()
		require.NoError(rt, split.fold(log[:cut], nil))
		require.NoError(rt, split.fold(log[cut:], nil))

		again := newRunState()
		require.NoError(rt, again.fold(log, nil))

		assert.Equal(rt, whole, split, "prefix-monotone")
		assert.Equal(rt, whole, again, "repeatable")
	})
}

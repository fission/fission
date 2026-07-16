// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// foldOf builds a RunState by folding the given events.
func foldOf(t *testing.T, events ...Event) *RunState {
	t.Helper()
	s := newRunState()
	require.NoError(t, s.fold(wfLog(t, events...), nil))
	return s
}

// TestDecide covers every branch of the TLA NextOptions plus the phase-2
// extensions (timers, run timeout, cancel).
func TestDecide(t *testing.T) {
	t.Parallel()

	started := Event{Type: EvRunStarted, Spec: pipelineSpec(), Input: json.RawMessage(`{}`)}
	now := time.Now()

	cases := []struct {
		name   string
		state  *RunState
		cancel bool
		at     time.Time
		want   action
	}{
		{"empty log -> RunStarted",
			newRunState(), false, now,
			action{kind: actAppendRunStarted}},
		{"started -> schedule first attempt",
			foldOf(t, started), false, now,
			action{kind: actScheduleStep, state: "a", attempt: 1}},
		{"scheduled unresolved -> (re)invoke",
			foldOf(t, started, Event{Type: EvStepScheduled, State: "a", Attempt: 1}), false, now,
			action{kind: actInvoke, state: "a", attempt: 1}},
		{"all steps done -> complete",
			foldOf(t, started,
				Event{Type: EvStepScheduled, State: "a", Attempt: 1},
				Event{Type: EvStepSucceeded, State: "a", Attempt: 1, Output: json.RawMessage(`{}`)},
				Event{Type: EvStepScheduled, State: "b", Attempt: 1},
				Event{Type: EvStepSucceeded, State: "b", Attempt: 1, Output: json.RawMessage(`{}`)},
			), false, now,
			action{kind: actCompleteRun}},
		{"terminal -> none (W4)",
			foldOf(t, started, Event{Type: EvRunCancelled}), false, now,
			action{kind: actNone}},
		{"cancel requested -> cancel, even mid-step",
			foldOf(t, started, Event{Type: EvStepScheduled, State: "a", Attempt: 1}), true, now,
			action{kind: actCancelRun}},
		{"cancel after terminal -> none (W4 beats cancel)",
			foldOf(t, started, Event{Type: EvRunCancelled}), true, now,
			action{kind: actNone}},
		{"deadline passed -> timeout",
			foldOf(t, started), false, now.Add(fv1.DefaultWorkflowTimeout + 25*time.Hour),
			action{kind: actTimeoutRun}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, []action{tc.want}, decide(tc.state, tc.cancel, tc.at, nil))
		})
	}
}

func TestDecideRetryFlow(t *testing.T) {
	t.Parallel()

	spec := pipelineSpec()
	a := spec.States["a"]
	a.Retry = &fv1.RetryPolicy{MaxAttempts: new(3)}
	a.Catch = []fv1.WorkflowCatchRoute{{ErrorType: fv1.WorkflowErrAll, Next: "b"}}
	spec.States["a"] = a
	started := Event{Type: EvRunStarted, Spec: spec, Input: json.RawMessage(`{}`)}
	fail := func(attempt int32) Event {
		return Event{Type: EvStepFailed, State: "a", Attempt: attempt, ErrorType: fv1.WorkflowErrFunctionError}
	}
	sched := func(attempt int32) Event {
		return Event{Type: EvStepScheduled, State: "a", Attempt: attempt}
	}
	now := time.Now()

	t.Run("retryable failure arms the backoff timer", func(t *testing.T) {
		t.Parallel()
		got := decide(foldOf(t, started, sched(1), fail(1)), false, now, nil)[0]
		assert.Equal(t, actArmTimer, got.kind)
		assert.Equal(t, "a", got.state)
		assert.Equal(t, int32(1), got.attempt)
		assert.Positive(t, got.delay)
	})

	t.Run("timer fired -> schedule attempt+1 (W5)", func(t *testing.T) {
		t.Parallel()
		got := decide(foldOf(t, started, sched(1), fail(1),
			Event{Type: EvTimerFired, State: "a", Attempt: 1}), false, now, nil)[0]
		assert.Equal(t, action{kind: actScheduleStep, state: "a", attempt: 2}, got)
	})

	t.Run("exhausted with catch -> catch target scheduled (fold routed)", func(t *testing.T) {
		t.Parallel()
		s := foldOf(t, started,
			sched(1), fail(1), Event{Type: EvTimerFired, State: "a", Attempt: 1},
			sched(2), fail(2), Event{Type: EvTimerFired, State: "a", Attempt: 2},
			sched(3), fail(3))
		assert.Equal(t, "b", s.Current, "fold advanced to the catch route")
		got := decide(s, false, now, nil)[0]
		assert.Equal(t, action{kind: actScheduleStep, state: "b", attempt: 1}, got)
		// The catch target's input is the error object.
		var doc map[string]any
		require.NoError(t, json.Unmarshal(s.Doc, &doc))
		assert.Equal(t, fv1.WorkflowErrFunctionError, doc["errorType"])
	})

	t.Run("permanent error skips retries entirely", func(t *testing.T) {
		t.Parallel()
		s := foldOf(t, started, sched(1),
			Event{Type: EvStepFailed, State: "a", Attempt: 1, ErrorType: fv1.WorkflowErrPermanentError})
		assert.Equal(t, "b", s.Current, "straight to catch despite budget")
	})

	t.Run("exhausted without catch -> fail run", func(t *testing.T) {
		t.Parallel()
		noCatch := pipelineSpec() // no Retry, no Catch: one attempt only
		s := foldOf(t,
			Event{Type: EvRunStarted, Spec: noCatch, Input: json.RawMessage(`{}`)},
			Event{Type: EvStepScheduled, State: "a", Attempt: 1},
			Event{Type: EvStepFailed, State: "a", Attempt: 1, ErrorType: fv1.WorkflowErrFunctionError})
		got := decide(s, false, now, nil)[0]
		assert.Equal(t, actFailRun, got.kind)
		assert.Equal(t, fv1.WorkflowErrFunctionError, s.PendingError)
	})

	t.Run("attempts never exceed budget (W6)", func(t *testing.T) {
		t.Parallel()
		s := foldOf(t, started,
			sched(1), fail(1), Event{Type: EvTimerFired, State: "a", Attempt: 1},
			sched(2), fail(2), Event{Type: EvTimerFired, State: "a", Attempt: 2},
			sched(3), fail(3))
		// Budget is 3: decide must not open attempt 4 — the fold already
		// routed to catch, so the next action targets "b", never "a"/4.
		got := decide(s, false, now, nil)[0]
		assert.NotEqual(t, actArmTimer, got.kind)
		if got.kind == actScheduleStep {
			assert.NotEqual(t, "a", got.state)
		}
	})
}

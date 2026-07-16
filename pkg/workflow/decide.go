// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/backoff"
)

// Backoff defaults when a retry policy leaves them unset.
const (
	defaultBackoffBase = time.Second
	defaultBackoffCap  = time.Minute
)

type actionKind int

const (
	actNone actionKind = iota
	// actAppendRunStarted: empty log — snapshot the spec+input.
	actAppendRunStarted
	// actScheduleStep: append StepScheduled{State, Attempt}.
	actScheduleStep
	// actInvoke: a scheduled-but-unresolved attempt exists — (re)invoke it
	// (at-least-once by construction; a crash between schedule and result
	// re-invokes on replay).
	actInvoke
	// actArmTimer: enqueue the wf-timers backoff delay for {State, Attempt}.
	actArmTimer
	// actCompleteRun / actFailRun / actCancelRun / actTimeoutRun: append the
	// terminal event (W4: CAS makes it the last word).
	actCompleteRun
	actFailRun
	actCancelRun
	actTimeoutRun
)

type action struct {
	kind    actionKind
	state   string
	attempt int32
	delay   time.Duration // actArmTimer
}

// decide is workflowfold.tla's NextOptions in Go: a pure function of the
// fold (and the cancel annotation + clock, which the model treats as
// environment). Two racing reconcilers computing from the same log compute
// the same action; CAS arbitrates the append. Function invocation outcomes
// never appear here — they arrive as events.
func decide(s *RunState, cancelRequested bool, now time.Time, rand func() float64) action {
	if s.Terminal != "" {
		return action{kind: actNone}
	}
	if cancelRequested {
		return action{kind: actCancelRun}
	}
	if s.Spec == nil {
		return action{kind: actAppendRunStarted}
	}

	timeout := fv1.DefaultWorkflowTimeout
	if s.Spec.Timeout != nil {
		timeout = s.Spec.Timeout.Duration
	}
	if now.After(s.StartedAt.Add(timeout)) {
		return action{kind: actTimeoutRun}
	}

	if s.PendingCompletion {
		return action{kind: actCompleteRun}
	}
	if s.PendingError != "" {
		return action{kind: actFailRun}
	}
	if s.Current == "" {
		return action{kind: actNone} // nothing runnable; corrupt folds never get here (fold fails loud)
	}

	attempt := s.Attempts[s.Current]
	if attempt == 0 {
		return action{kind: actScheduleStep, state: s.Current, attempt: 1}
	}

	res, resolved := s.Results[stepKey(s.Current, attempt)]
	if !resolved {
		return action{kind: actInvoke, state: s.Current, attempt: attempt}
	}
	if res.Succeeded {
		// Only reachable when a loop revisits a state (the fold advances past
		// a fresh success): open the next attempt window.
		return action{kind: actScheduleStep, state: s.Current, attempt: attempt + 1}
	}

	// Failed and retryable with budget left (the fold routed everything
	// else): reschedule once the backoff timer has fired, else arm it.
	if s.TimersFired[stepKey(s.Current, attempt)] {
		return action{kind: actScheduleStep, state: s.Current, attempt: attempt + 1}
	}
	return action{kind: actArmTimer, state: s.Current, attempt: attempt, delay: retryDelay(s.retryPolicy(s.Current), int(attempt), rand)}
}

func retryDelay(p fv1.RetryPolicy, attempt int, rand func() float64) time.Duration {
	base, cap := defaultBackoffBase, defaultBackoffCap
	if p.BackoffBase != nil {
		base = p.BackoffBase.Duration
	}
	if p.BackoffCap != nil {
		cap = p.BackoffCap.Duration
	}
	if p.Jitter != nil && !*p.Jitter {
		rand = nil
	}
	return backoff.ExpFullJitter(base, cap, attempt, rand)
}

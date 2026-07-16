// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"slices"
	"strconv"
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
	// actJoin: every branch of the live parallel region succeeded — append
	// EvBranchesJoined with the assembled, shaped output (W7).
	actJoin
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
	branch  string // parallel-region actions; "" = main flow
	attempt int32
	delay   time.Duration // actArmTimer
}

// defaultMaxConcurrency is the engine-applied MaxConcurrency default (the
// schema deliberately does not default it — see the field comment).
const defaultMaxConcurrency = 10

// decide is the models' NextOptions in Go (workflowfold.tla for the linear
// flow, workflowbranch.tla for a live parallel region): a pure function of
// the fold (and the cancel annotation + clock, which the models treat as
// environment). Two racing reconcilers computing from the same log compute
// the same actions; CAS arbitrates appends. It returns a LIST because a
// parallel region dispatches several branches concurrently; log-changing
// actions always come alone or first, so the engine processes acts[0] when
// it is an append and batch-dispatches otherwise.
func decide(s *RunState, cancelRequested bool, now time.Time, rand func() float64) []action {
	one := func(a action) []action { return []action{a} }

	if s.Terminal != "" {
		return one(action{kind: actNone})
	}
	if cancelRequested {
		return one(action{kind: actCancelRun})
	}
	if s.Spec == nil {
		return one(action{kind: actAppendRunStarted})
	}

	// A run whose fold already reached its outcome completes/fails on that
	// outcome even when the deadline passed in the same reconcile — the
	// timeout exists to stop runs that cannot finish, not to reclassify ones
	// that did.
	if s.PendingCompletion {
		return one(action{kind: actCompleteRun})
	}
	if s.PendingError != "" {
		return one(action{kind: actFailRun})
	}

	timeout := fv1.DefaultWorkflowTimeout
	if s.Spec.Timeout != nil {
		timeout = s.Spec.Timeout.Duration
	}
	if now.After(s.StartedAt.Add(timeout)) {
		return one(action{kind: actTimeoutRun})
	}

	if s.Current == "" {
		return one(action{kind: actNone}) // nothing runnable; corrupt folds never get here (fold fails loud)
	}

	if s.BranchRuns != nil {
		return decideRegion(s, rand)
	}
	return one(decideStep(s, "", s.Current, rand))
}

// decideStep computes the next step-level action for one machine (the main
// flow, or a branch mini-run when branch != ""). NEVER emits timeouts or
// terminal actions — those are run-level (a mini-run has no RunStarted, so
// its zero StartedAt would misfire a raw decide's deadline check).
func decideStep(s *RunState, branch, current string, rand func() float64) action {
	attempt := s.Attempts[current]
	if attempt == 0 {
		return action{kind: actScheduleStep, branch: branch, state: current, attempt: 1}
	}

	res, resolved := s.Results[stepKey(current, attempt)]
	if !resolved {
		return action{kind: actInvoke, branch: branch, state: current, attempt: attempt}
	}
	if res.Succeeded {
		// Only reachable when a loop revisits a state (the fold advances past
		// a fresh success): open the next attempt window.
		return action{kind: actScheduleStep, branch: branch, state: current, attempt: attempt + 1}
	}

	// Failed and retryable with budget left (the fold routed everything
	// else): reschedule once the backoff timer has fired, else arm it.
	if s.TimersFired[stepKey(current, attempt)] {
		return action{kind: actScheduleStep, branch: branch, state: current, attempt: attempt + 1}
	}
	return action{kind: actArmTimer, branch: branch, state: current, attempt: attempt,
		delay: retryDelay(s.retryPolicy(current), int(attempt), rand)}
}

// decideRegion is workflowbranch.tla's NextOptions: all branches succeeded →
// join; otherwise per-branch step actions, opening NEW branches only under
// the MaxConcurrency cap. Iteration is over sorted keys so racing
// reconcilers compute identical action lists.
func decideRegion(s *RunState, rand func() float64) []action {
	st := s.Spec.States[s.Current]
	maxConc := int(st.MaxConcurrency)
	if maxConc <= 0 {
		maxConc = defaultMaxConcurrency
	}

	keys := make([]string, 0, len(s.BranchRuns))
	for k := range s.BranchRuns {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		ai, _ := strconv.Atoi(a)
		bi, _ := strconv.Atoi(b)
		return ai - bi
	})

	branchDone := func(m *RunState) bool { return m.PendingCompletion || m.PendingError != "" }
	branchStarted := func(m *RunState) bool { return len(m.Attempts) > 0 || branchDone(m) }

	allDone := true
	running := 0
	for _, k := range keys {
		m := s.BranchRuns[k]
		if !branchDone(m) {
			allDone = false
		}
		if branchStarted(m) && !branchDone(m) {
			running++
		}
	}
	if allDone {
		return []action{{kind: actJoin, state: s.Current}}
	}

	// Log-changing actions (schedules) sort before dispatches (invokes,
	// timers) so the engine's process-first-append loop converges.
	var appends, dispatches []action
	for _, k := range keys {
		m := s.BranchRuns[k]
		if branchDone(m) || m.Current == "" {
			continue
		}
		if !branchStarted(m) {
			if running >= maxConc {
				continue // over the cap; opens when a running branch finishes
			}
			running++
		}
		a := decideStep(m, k, m.Current, rand)
		if a.kind == actScheduleStep {
			appends = append(appends, a)
		} else {
			dispatches = append(dispatches, a)
		}
	}
	if len(appends) == 0 && len(dispatches) == 0 {
		return []action{{kind: actNone}}
	}
	return append(appends, dispatches...)
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

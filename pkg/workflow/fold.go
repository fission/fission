// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/workflow/expr"
)

// derefFn resolves a spilled document by its KV ref. Spilled entries are
// immutable (write-once per (state, attempt) key), so dereferencing keeps
// the fold deterministic: same log → same refs → same content.
type derefFn func(ref string) (json.RawMessage, error)

// stepResult is one recorded (state, attempt) outcome.
type stepResult struct {
	Succeeded bool            `json:"succeeded"`
	ErrorType string          `json:"errorType,omitempty"`
	Cause     json.RawMessage `json:"cause,omitempty"`
}

// maxRecentEvents bounds the status tail (etcd visibility only; the full
// history is the stream).
const maxRecentEvents = 20

// RunState is the deterministic fold of a run's events — everything decide
// works from. JSON-serializable for checkpointing; losing a checkpoint only
// costs a longer re-fold, never correctness.
type RunState struct {
	Spec  *fv1.WorkflowSpec `json:"spec,omitempty"`
	Input json.RawMessage   `json:"input,omitempty"`

	// Current is the state whose turn it is ("" before RunStarted). Doc /
	// DocRef is the document flowing into it (already shaped by the previous
	// step's worker). Choice states are resolved during advancement, so
	// Current is always a Task, Succeed, or Fail state — only Task states
	// append step events, matching workflowfold.tla's task-step model.
	Current string          `json:"current,omitempty"`
	Doc     json.RawMessage `json:"doc,omitempty"`
	DocRef  string          `json:"docRef,omitempty"`

	// Attempts is each state's highest scheduled attempt (TLA AttemptsOf);
	// Results records outcomes keyed "state/attempt" (TLA HasResult).
	Attempts map[string]int32      `json:"attempts,omitempty"`
	Results  map[string]stepResult `json:"results,omitempty"`
	// TimerFired records consumed backoff timers keyed "state/attempt".
	TimersFired map[string]bool `json:"timersFired,omitempty"`

	// BranchRuns holds the live parallel region's per-branch mini-runs,
	// keyed by branch index ("0", "1", ...). A branch is a run in miniature:
	// same fold machinery, spec synthesized from the branch definition
	// (checkpointed as-is — at <=10 branches / <=100 items with bounded
	// branch specs, correctness-simple beats a custom spec-stripping
	// marshal). Non-nil exactly while Current is a Parallel/Map state.
	BranchRuns map[string]*RunState `json:"branchRuns,omitempty"`

	// PendingCompletion is set when advancement reached the end (End=true or
	// a Succeed state): decide appends the terminal event.
	PendingCompletion bool `json:"pendingCompletion,omitempty"`
	// PendingError is a fold-level failure awaiting terminal/catch routing:
	// NoChoiceMatched, or a Fail state reached.
	PendingError string `json:"pendingError,omitempty"`

	// Terminal is set by a terminal event; nothing folds after it (W4).
	Terminal  fv1.WorkflowRunPhase `json:"terminal,omitempty"`
	Output    json.RawMessage      `json:"output,omitempty"`
	OutputRef string               `json:"outputRef,omitempty"`
	ErrorType string               `json:"errorType,omitempty"`
	Cause     json.RawMessage      `json:"cause,omitempty"`

	StartedAt time.Time `json:"startedAt,omitempty"`
	LastSeq   int64     `json:"lastSeq,omitempty"`

	Recent []fv1.WorkflowRunEventSummary `json:"recent,omitempty"`
}

func newRunState() *RunState {
	return &RunState{
		Attempts:    map[string]int32{},
		Results:     map[string]stepResult{},
		TimersFired: map[string]bool{},
	}
}

func stepKey(state string, attempt int32) string {
	return fmt.Sprintf("%s/%d", state, attempt)
}

// fold applies events (ascending seq) onto s. Pure given deref (immutable
// refs). The log is CAS-protected, so an impossible sequence is corruption:
// fail loud, never guess.
func (s *RunState) fold(events []statestore.Event, deref derefFn) error {
	for _, se := range events {
		if se.Seq <= s.LastSeq {
			return fmt.Errorf("fold: event seq %d not past checkpoint %d", se.Seq, s.LastSeq)
		}
		e, err := decodeEvent(se)
		if err != nil {
			return err
		}
		if s.Terminal != "" {
			// One benign exception to strict W4: a timer message that fired
			// after the run went terminal appends TimerFired without a CAS
			// conflict (fresh Head == post-terminal head). The fold ignores
			// it — TimersFired is consulted only for non-terminal runs — and
			// stays strict for every event the TLA invariants cover.
			if e.Type == EvTimerFired {
				s.LastSeq = se.Seq
				continue
			}
			return fmt.Errorf("fold: %s at seq %d after terminal %s (W4 violated — corrupt stream)", e.Type, se.Seq, s.Terminal)
		}
		if e.Branch != "" && s.BranchRuns == nil &&
			(e.Type == EvStepSucceeded || e.Type == EvStepFailed || e.Type == EvTimerFired) {
			// A draining sibling's result (or timer) landing after the region
			// closed (join, fail-fast, or catch routing). This is a
			// DOCUMENTED deviation from the model's strict W8: the TLA
			// reconcilers replan from a fresh read after a lost CAS, so no
			// branch event can ever follow the join there — but our
			// appendGuarded retries at the new head, and a sibling in flight
			// when fail-fast dissolved the region can append before the
			// terminal lands. The event changes nothing (the region's
			// outcome was decided by an earlier event, identically for every
			// replayer); schedules after closure remain corruption.
			s.LastSeq = se.Seq
			continue
		}
		if err := s.apply(e, se, deref); err != nil {
			return fmt.Errorf("fold: seq %d (%s): %w", se.Seq, e.Type, err)
		}
		s.LastSeq = se.Seq
		s.pushRecent(e, se)
	}
	return nil
}

func (s *RunState) apply(e Event, se statestore.Event, deref derefFn) error {
	if e.Branch != "" {
		return s.applyBranchEvent(e, se, deref)
	}
	switch e.Type {
	case EvBranchesJoined:
		if s.BranchRuns == nil {
			return fmt.Errorf("joined without a live parallel region (W8)")
		}
		for key, mini := range s.BranchRuns {
			if !mini.PendingCompletion {
				return fmt.Errorf("joined while branch %s is not complete (W7)", key)
			}
		}
		// The event carries the shaped post-join document, like a step result.
		s.Doc, s.DocRef = e.Output, e.OutputRef
		st := s.Spec.States[s.Current]
		s.BranchRuns = nil
		if st.End {
			s.Current = ""
			s.PendingCompletion = true
			return nil
		}
		return s.advance(st.Next, deref)

	case EvRunStarted:
		if s.Spec != nil {
			return fmt.Errorf("duplicate RunStarted")
		}
		if e.Spec == nil {
			return fmt.Errorf("RunStarted without a spec snapshot")
		}
		s.Spec = e.Spec
		s.Input = e.Input
		s.StartedAt = se.At
		s.Doc = e.Input
		return s.advance(e.Spec.StartAt, deref)

	case EvStepScheduled:
		if e.State != s.Current {
			return fmt.Errorf("scheduled %q but current state is %q", e.State, s.Current)
		}
		if e.Attempt != s.Attempts[e.State]+1 {
			return fmt.Errorf("scheduled attempt %d for %q, want %d (W1/W5)", e.Attempt, e.State, s.Attempts[e.State]+1)
		}
		s.Attempts[e.State] = e.Attempt
		return nil

	case EvStepSucceeded, EvStepFailed:
		key := stepKey(e.State, e.Attempt)
		if e.Attempt > s.Attempts[e.State] {
			return fmt.Errorf("result for unscheduled %s (W3)", key)
		}
		if _, dup := s.Results[key]; dup {
			return fmt.Errorf("duplicate result for %s (W2)", key)
		}
		if e.Type == EvStepFailed {
			s.Results[key] = stepResult{ErrorType: e.ErrorType, Cause: e.Cause}
			return s.applyStepFailure(e, deref)
		}
		s.Results[key] = stepResult{Succeeded: true}
		// The event carries the next state's (already shaped) document.
		s.Doc, s.DocRef = e.Output, e.OutputRef
		st, ok := s.Spec.States[e.State]
		if !ok {
			return fmt.Errorf("state %q not in snapshot", e.State)
		}
		if st.End {
			s.Current = ""
			s.PendingCompletion = true
			return nil
		}
		return s.advance(st.Next, deref)

	case EvTimerFired:
		s.TimersFired[stepKey(e.State, e.Attempt)] = true
		return nil

	case EvRunSucceeded:
		s.Terminal = fv1.WorkflowRunSucceeded
		s.Output, s.OutputRef = e.Output, e.OutputRef
		return nil
	case EvRunFailed:
		s.Terminal = fv1.WorkflowRunFailed
		s.ErrorType, s.Cause = e.ErrorType, e.Cause
		return nil
	case EvRunCancelled:
		s.Terminal = fv1.WorkflowRunCancelled
		return nil
	case EvRunTimedOut:
		s.Terminal = fv1.WorkflowRunTimedOut
		s.ErrorType = fv1.WorkflowErrTimeout
		return nil
	default:
		return fmt.Errorf("unhandled event type %q", e.Type)
	}
}

// maxMapItems caps Map fan-out (the RFC's "cap ItemsPath length in v1").
const maxMapItems = 100

// applyBranchEvent routes a branch-tagged event into its mini-run, then
// handles region-level consequences: a branch failing terminally is
// fail-fast (workflowbranch.tla) — route the region's Catch on
// Fission.BranchFailed or fail the run.
func (s *RunState) applyBranchEvent(e Event, se statestore.Event, deref derefFn) error {
	if s.BranchRuns == nil {
		return fmt.Errorf("branch %q event without a live parallel region (W8 violated — corrupt stream)", e.Branch)
	}
	mini, ok := s.BranchRuns[e.Branch]
	if !ok {
		return fmt.Errorf("event for undeclared branch %q", e.Branch)
	}

	// The mini-run applies the event with the SAME machinery (its own
	// current-state assertions, attempts, results, catch routing within the
	// branch); strip the branch tag so its bookkeeping is branch-local.
	inner := e
	inner.Branch = ""
	if err := mini.apply(inner, se, deref); err != nil {
		return fmt.Errorf("branch %s: %w", e.Branch, err)
	}

	if mini.PendingError != "" {
		// Fail-fast: the branch is terminally failed. The region fails with
		// Fission.BranchFailed, routable by a Catch on the fan-out state.
		cause, err := json.Marshal(map[string]any{
			"branch":    e.Branch,
			"errorType": mini.PendingError,
			"cause":     nonEmpty(mini.Cause),
		})
		if err != nil {
			return fmt.Errorf("encoding branch error object: %w", err)
		}
		st := s.Spec.States[s.Current]
		s.BranchRuns = nil
		if route := matchCatch(st.Catch, fv1.WorkflowErrBranchFailed); route != "" {
			errObj, err := json.Marshal(map[string]any{"errorType": fv1.WorkflowErrBranchFailed, "cause": json.RawMessage(cause)})
			if err != nil {
				return fmt.Errorf("encoding error object: %w", err)
			}
			s.Doc, s.DocRef = errObj, ""
			return s.advance(route, deref)
		}
		s.Current = ""
		s.PendingError = fv1.WorkflowErrBranchFailed
		s.Cause = cause
	}
	return nil
}

// enterRegion initializes the parallel region's mini-runs when advancement
// reaches a Parallel/Map state. Deterministic from the log: branch inputs
// derive from the flowing document (deref'd spill refs are immutable).
func (s *RunState) enterRegion(name string, st fv1.WorkflowState, deref derefFn) error {
	doc, err := s.currentDoc(deref)
	if err != nil {
		return err
	}
	regionInput, err := shapeInput(st, doc)
	if err != nil {
		return err
	}

	type branchSeed struct {
		branch fv1.WorkflowBranch
		input  any
	}
	var seeds []branchSeed
	switch st.Type {
	case fv1.WorkflowStateParallel:
		for _, b := range st.Branches {
			seeds = append(seeds, branchSeed{branch: b, input: regionInput})
		}
	case fv1.WorkflowStateMap:
		items, matched := mustParsePath(st.ItemsPath).Get(regionInput)
		arr, ok := items.([]any)
		if !matched || !ok {
			s.Current = ""
			s.PendingError = fv1.WorkflowErrPermanentError
			s.Cause, _ = json.Marshal(fmt.Sprintf("itemsPath %s did not select an array", st.ItemsPath))
			return nil
		}
		if len(arr) > maxMapItems {
			s.Current = ""
			s.PendingError = fv1.WorkflowErrPermanentError
			s.Cause, _ = json.Marshal(fmt.Sprintf("map has %d items; at most %d in v1", len(arr), maxMapItems))
			return nil
		}
		for _, item := range arr {
			seeds = append(seeds, branchSeed{branch: st.Branches[0], input: item})
		}
	}

	s.Current = name
	s.BranchRuns = make(map[string]*RunState, len(seeds))
	for i, seed := range seeds {
		mini := newRunState()
		mini.Spec = &fv1.WorkflowSpec{
			StartAt: seed.branch.StartAt,
			States:  seed.branch.StatesAsWorkflow(),
			// The workflow-level retry default reaches branch tasks
			// (retryPolicy falls back to Spec.DefaultRetry).
			DefaultRetry: s.Spec.DefaultRetry,
		}
		raw, err := json.Marshal(seed.input)
		if err != nil {
			return fmt.Errorf("encoding branch %d input: %w", i, err)
		}
		mini.Doc = raw
		if err := mini.advance(seed.branch.StartAt, deref); err != nil {
			return fmt.Errorf("branch %d: %w", i, err)
		}
		s.BranchRuns[strconv.Itoa(i)] = mini
	}
	return nil
}

// mustParsePath is for admission-validated paths (a parse failure here means
// a snapshot from a dialect this binary no longer accepts — the ItemsPath
// no-array error path handles the nil).
func mustParsePath(path string) expr.Path {
	p, err := expr.Parse(path)
	if err != nil {
		return expr.Path{}
	}
	return p
}

// applyStepFailure routes a recorded failure — all deterministic from the
// log (policy, attempt, class), so routing lives in the fold, not decide.
// Retryable with budget left: stay on the state (decide arms the timer and
// reschedules). Otherwise: the first matching Catch route advances with the
// error object as the flowing document, or the failure goes run-level.
func (s *RunState) applyStepFailure(e Event, deref derefFn) error {
	if isRetryable(e.ErrorType) && int(e.Attempt) < s.maxAttempts(e.State) {
		return nil
	}
	st := s.Spec.States[e.State]
	if route := matchCatch(st.Catch, e.ErrorType); route != "" {
		errObj, err := json.Marshal(map[string]any{"errorType": e.ErrorType, "cause": nonEmpty(e.Cause)})
		if err != nil {
			return fmt.Errorf("encoding error object: %w", err)
		}
		s.Doc, s.DocRef = errObj, ""
		return s.advance(route, deref)
	}
	s.Current = ""
	s.PendingError = e.ErrorType
	s.Cause = e.Cause
	return nil
}

// isRetryable: only transport-class failures retry; permanent classes and
// function-typed errors (the author's own vocabulary) go straight to Catch.
func isRetryable(errorType string) bool {
	return errorType == fv1.WorkflowErrFunctionError || errorType == fv1.WorkflowErrTimeout
}

// maxAttempts resolves the state's attempt budget; no declared policy means
// one attempt (Step Functions parity: no retry unless asked for).
func (s *RunState) maxAttempts(state string) int {
	p := s.retryPolicy(state)
	if p.MaxAttempts == nil {
		return 1
	}
	return *p.MaxAttempts
}

// matchCatch returns the first route matching the error type (exact match
// first-wins in declaration order; Fission.All matches anything).
func matchCatch(routes []fv1.WorkflowCatchRoute, errorType string) string {
	for _, r := range routes {
		if r.ErrorType == errorType || r.ErrorType == fv1.WorkflowErrAll {
			return r.Next
		}
	}
	return ""
}

func nonEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

// advance moves Current to the named state, resolving consecutive Choice
// states against the flowing document (Choice states never append events —
// they are fold-internal, matching the TLA task-step model). It stops at a
// Task (awaiting schedule), flags completion at Succeed, and flags a
// pending error at Fail or an unmatched Choice.
func (s *RunState) advance(to string, deref derefFn) error {
	doc, err := s.currentDoc(deref)
	if err != nil {
		return err
	}
	for {
		st, ok := s.Spec.States[to]
		if !ok {
			return fmt.Errorf("advance: state %q not in snapshot", to)
		}
		switch st.Type {
		case fv1.WorkflowStateTask:
			s.Current = to
			return nil
		case fv1.WorkflowStateParallel, fv1.WorkflowStateMap:
			return s.enterRegion(to, st, deref)
		case fv1.WorkflowStateSucceed:
			s.Current = ""
			s.PendingCompletion = true
			return nil
		case fv1.WorkflowStateFail:
			s.Current = ""
			s.PendingError = fv1.WorkflowErrFailed
			return nil
		case fv1.WorkflowStateChoice:
			next, matched := evalChoice(st, doc)
			if !matched {
				s.Current = ""
				s.PendingError = fv1.WorkflowErrNoChoiceMatched
				return nil
			}
			to = next
		default:
			return fmt.Errorf("advance: unsupported state type %q", st.Type)
		}
	}
}

// currentDoc decodes the flowing document, dereferencing a spill if needed.
func (s *RunState) currentDoc(deref derefFn) (any, error) {
	raw := s.Doc
	if s.DocRef != "" {
		if deref == nil {
			return nil, fmt.Errorf("document %q is spilled but no deref is available", s.DocRef)
		}
		var err error
		raw, err = deref(s.DocRef)
		if err != nil {
			return nil, fmt.Errorf("dereferencing %q: %w", s.DocRef, err)
		}
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decoding flowing document: %w", err)
	}
	return doc, nil
}

// retryPolicy resolves the effective policy for a state (state override,
// then workflow default, then no retries — Step Functions parity).
func (s *RunState) retryPolicy(state string) fv1.RetryPolicy {
	st := s.Spec.States[state]
	if st.Retry != nil {
		return *st.Retry
	}
	if s.Spec.DefaultRetry != nil {
		return *s.Spec.DefaultRetry
	}
	return fv1.RetryPolicy{}
}

func (s *RunState) pushRecent(e Event, se statestore.Event) {
	s.Recent = append(s.Recent, fv1.WorkflowRunEventSummary{
		Seq:     se.Seq,
		Type:    string(e.Type),
		State:   e.State,
		Attempt: e.Attempt,
		At:      metav1.Time{Time: se.At},
		Note:    e.ErrorType,
	})
	if len(s.Recent) > maxRecentEvents {
		s.Recent = s.Recent[len(s.Recent)-maxRecentEvents:]
	}
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
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
	Attempts map[string]int32       `json:"attempts,omitempty"`
	Results  map[string]stepResult  `json:"results,omitempty"`
	// TimerFired records consumed backoff timers keyed "state/attempt".
	TimersFired map[string]bool `json:"timersFired,omitempty"`

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
			return fmt.Errorf("fold: %s at seq %d after terminal %s (W4 violated — corrupt stream)", e.Type, se.Seq, s.Terminal)
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
	switch e.Type {
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
			return nil
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
		case fv1.WorkflowStateSucceed:
			s.Current = ""
			s.PendingCompletion = true
			return nil
		case fv1.WorkflowStateFail:
			s.Current = ""
			s.PendingError = fv1.WorkflowErrAll // a bare Fail state fails the run
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

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package workflow implements the RFC-0022 durable workflow engine: a pure
// fold over a CAS-protected statestore EventLog stream per WorkflowRun. The
// protocol (who may append what, when) is modeled and verified in
// docs/rfc/specs/workflowfold.tla — invariants W1-W6; protocol changes
// extend the spec before the code.
package workflow

import (
	"encoding/json"
	"fmt"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
)

// EventType mirrors workflowfold.tla's event vocabulary plus the extras the
// model abstracts: RunStarted carries the authoritative spec snapshot, and
// TimerFired consumes a Queue-armed backoff delay.
type EventType string

const (
	EvRunStarted    EventType = "RunStarted"    // {Spec snapshot, Input}
	EvStepScheduled EventType = "StepScheduled" // TLA "sched" {State, Attempt, InputHash}
	EvStepSucceeded EventType = "StepSucceeded" // TLA "ok"    {State, Attempt, Output|OutputRef}
	EvStepFailed    EventType = "StepFailed"    // TLA "fail"  {State, Attempt, ErrorType, Cause}
	EvTimerFired    EventType = "TimerFired"    // backoff elapsed for {State, Attempt}
	EvRunSucceeded  EventType = "RunSucceeded"  // TLA "done"  {Output|OutputRef}
	EvRunFailed     EventType = "RunFailed"     // TLA "failed" {ErrorType, Cause}
	EvRunCancelled  EventType = "RunCancelled"  // TLA "cancelled"
	EvRunTimedOut   EventType = "RunTimedOut"   // run Timeout expiry
	// EvBranchesJoined closes a parallel region (workflowbranch.tla W7/W8):
	// unique, only after every branch succeeded, and nothing but the region's
	// continuation follows it. Carries the shaped post-join document.
	EvBranchesJoined EventType = "BranchesJoined"
)

// Event is one entry of a run's log. The schema is a durable wire contract:
// existing fields are frozen the moment any run exists; additions must be
// optional and ignorable by older folds only across a single release skew.
type Event struct {
	Type    EventType `json:"type"`
	State   string    `json:"state,omitempty"`
	Attempt int32     `json:"attempt,omitempty"`
	// Branch discriminates parallel-region step events (workflowbranch.tla);
	// empty = the main flow. Region identifies WHICH region instance the
	// event belongs to ("state@entrySeq") — without it, a sibling draining
	// out of a fail-fasted region could be routed into a successor region
	// that reuses the same branch keys (loops can even re-enter the same
	// fan-out state, so the state name alone is not enough).
	Branch string `json:"branch,omitempty"`
	Region string `json:"region,omitempty"`

	// RunStarted only: the spec snapshot this run executes, forever, plus the
	// initial input. A Workflow edit or deletion mid-run can neither fork nor
	// strand the run — the stream alone determines its semantics.
	Spec  *fv1.WorkflowSpec `json:"spec,omitempty"`
	Input json.RawMessage   `json:"input,omitempty"`

	// Results carry exactly one of Output (inline, <= spill threshold) or
	// OutputRef (a statestore KV key in the run's "io" keyspace).
	Output    json.RawMessage `json:"output,omitempty"`
	OutputRef string          `json:"outputRef,omitempty"`

	// Failures carry the RFC error model's classification.
	ErrorType string          `json:"errorType,omitempty"`
	Cause     json.RawMessage `json:"cause,omitempty"`

	// InputHash fingerprints the shaped input a StepScheduled was computed
	// from (debugging aid; not consulted by the fold).
	InputHash string `json:"inputHash,omitempty"`
}

var knownEventTypes = map[EventType]bool{
	EvRunStarted: true, EvStepScheduled: true, EvStepSucceeded: true,
	EvStepFailed: true, EvTimerFired: true, EvRunSucceeded: true,
	EvRunFailed: true, EvRunCancelled: true, EvRunTimedOut: true,
	EvBranchesJoined: true,
}

func encodeEvent(e Event) (statestore.Event, error) {
	payload, err := json.Marshal(e)
	if err != nil {
		return statestore.Event{}, fmt.Errorf("encoding %s event: %w", e.Type, err)
	}
	return statestore.Event{Type: string(e.Type), Payload: payload}, nil
}

// decodeEvent is strict: an unknown type means a newer writer touched the
// stream (or the stream is corrupt) — the fold must refuse to guess rather
// than silently skip an event it does not understand.
func decodeEvent(se statestore.Event) (Event, error) {
	if !knownEventTypes[EventType(se.Type)] {
		return Event{}, fmt.Errorf("unknown workflow event type %q at seq %d", se.Type, se.Seq)
	}
	var e Event
	if err := json.Unmarshal(se.Payload, &e); err != nil {
		return Event{}, fmt.Errorf("decoding %s event at seq %d: %w", se.Type, se.Seq, err)
	}
	return e, nil
}

// streamName is the run's EventLog stream. Keyed on UID, not name: a
// delete-and-recreate under the same name must never resume the old log.
func streamName(run *fv1.WorkflowRun) string {
	return "wfrun/" + string(run.UID)
}

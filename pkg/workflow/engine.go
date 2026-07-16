// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
)

const (
	// readBatch is the EventLog read window per call.
	readBatch = 64
	// checkpointEvery bounds how much tail a restart re-folds; losing a
	// checkpoint costs a longer re-fold, never correctness.
	checkpointEvery = 32
	// CancelAnnotation requests cancellation; in-flight invocations drain
	// (no function kill signal exists) and their late completions lose the
	// CAS against the terminal event.
	CancelAnnotation = "fission.io/cancel-requested"
)

// specFetch supplies the Workflow spec for the RunStarted snapshot; called
// exactly once per run lifetime (the first reconcile).
type specFetch func(ctx context.Context) (*fv1.WorkflowSpec, error)

// Engine advances WorkflowRuns: checkpoint → read tail → fold → decide →
// CAS-append / dispatch. Correctness never depends on being the only
// instance — a lost CAS is a signal to re-read, never an error.
type Engine struct {
	logger  logr.Logger
	el      statestore.EventLog
	q       statestore.Queue
	kv      statestore.KVStore
	invoker *Invoker
	wake    func(types.NamespacedName)
	clock   func() time.Time
	rand    func() float64
}

type EngineOptions struct {
	Logger   logr.Logger
	EventLog statestore.EventLog
	Queue    statestore.Queue
	KV       statestore.KVStore
	Invoker  *Invoker
	Wake     func(types.NamespacedName)
	Clock    func() time.Time // nil = time.Now
	Rand     func() float64   // nil = math/rand/v2; injected for determinism
}

func NewEngine(o EngineOptions) *Engine {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.Rand == nil {
		o.Rand = rand.Float64
	}
	return &Engine{
		logger: o.Logger, el: o.EventLog, q: o.Queue, kv: o.KV,
		invoker: o.Invoker, wake: o.Wake, clock: o.Clock, rand: o.Rand,
	}
}

// Reconcile advances one run as far as pure decisions allow and returns the
// folded state for the status writer. A CAS conflict re-reads and continues;
// the loop exits on actNone, a dispatched invocation, or an armed timer
// (progress then arrives as an event + wake).
func (e *Engine) Reconcile(ctx context.Context, run *fv1.WorkflowRun, fetch specFetch) (*RunState, error) {
	stream := streamName(run)
	deref := e.derefFor(run)

	s, err := e.loadCheckpoint(ctx, run)
	if err != nil {
		return nil, err
	}

	for {
		if err := e.foldTail(ctx, stream, s, deref); err != nil {
			return nil, err
		}

		acts := decide(s, run.Annotations[CancelAnnotation] != "", e.clock(), e.rand)
		act := acts[0]

		var ev Event
		switch act.kind {
		case actNone:
			e.saveCheckpoint(ctx, run, s)
			return s, nil

		case actInvoke, actArmTimer:
			// Pure dispatches — a parallel region may carry several; process
			// them ALL, then wait for wakes (decide sorts appends first, so
			// reaching here means no append is pending).
			for _, a := range acts {
				switch a.kind {
				case actInvoke:
					if err := e.dispatchInvoke(run, stream, s, a, deref); err != nil {
						return nil, err
					}
				case actArmTimer:
					if err := e.armTimer(ctx, run, a); err != nil {
						return nil, err
					}
				}
			}
			e.saveCheckpoint(ctx, run, s)
			return s, nil

		case actAppendRunStarted:
			spec, err := fetch(ctx)
			if err != nil {
				return nil, err
			}
			var input json.RawMessage
			if run.Spec.Input != nil {
				input = run.Spec.Input.Raw
			}
			ev = Event{Type: EvRunStarted, Spec: spec, Input: input}

		case actScheduleStep:
			ev = Event{Type: EvStepScheduled, State: act.state, Branch: act.branch, Region: act.region, Attempt: act.attempt}

		case actJoin:
			joined, err := e.assembleJoin(ctx, run, s, deref)
			if err != nil {
				return nil, err
			}
			ev = joined

		case actCompleteRun:
			ev = Event{Type: EvRunSucceeded, Output: s.Doc, OutputRef: s.DocRef}
		case actFailRun:
			ev = Event{Type: EvRunFailed, ErrorType: s.PendingError, Cause: s.Cause}
		case actCancelRun:
			ev = Event{Type: EvRunCancelled}
		case actTimeoutRun:
			ev = Event{Type: EvRunTimedOut}

		default:
			return nil, fmt.Errorf("unhandled action %d", act.kind)
		}

		head, err := e.appendAt(ctx, stream, s.LastSeq, ev)
		if err != nil {
			if errors.Is(err, statestore.ErrVersionConflict) {
				if head < s.LastSeq {
					// The stream is BEHIND the fold: a stale checkpoint or a
					// trimmed stream. Re-reading can never converge — fail
					// loud (surfaces as an EngineError condition) instead of
					// spinning the reconcile worker forever.
					return nil, fmt.Errorf("stream %s head %d is behind folded seq %d (stale checkpoint or trimmed stream)", stream, head, s.LastSeq)
				}
				continue // someone else advanced the log; re-read and replan
			}
			return nil, err
		}
	}
}

// dispatchInvoke hands one (possibly branch-scoped) invocation to the pool.
func (e *Engine) dispatchInvoke(run *fv1.WorkflowRun, stream string, s *RunState, a action, deref derefFn) error {
	machine := s
	if a.branch != "" {
		machine = s.BranchRuns[a.branch]
		if machine == nil {
			return fmt.Errorf("invoke for unknown branch %q", a.branch)
		}
	}
	st := machine.Spec.States[a.state]
	doc := machine.Doc
	if machine.DocRef != "" {
		var err error
		if doc, err = deref(machine.DocRef); err != nil {
			return err
		}
	}
	e.invoker.Dispatch(invocation{
		runKey: types.NamespacedName{Namespace: run.Namespace, Name: run.Name},
		runUID: string(run.UID), stream: stream, namespace: run.Namespace,
		branch: a.branch, region: a.region, state: a.state, attempt: a.attempt,
		stateSpec: st, input: doc,
		expectedSeq: s.LastSeq,
	})
	return nil
}

// assembleJoin builds the EvBranchesJoined event: the ordered branch outputs
// as an array, merged into the region's input per Result/OutputPath, spilled
// when large. A join-shaping InvalidPath fails the RUN (validation rejects
// unparseable paths at admission; the residual unwritable-shape case is a
// documented v1 edge without catch routing).
func (e *Engine) assembleJoin(ctx context.Context, run *fv1.WorkflowRun, s *RunState, deref derefFn) (Event, error) {
	st := s.Spec.States[s.Current]

	outputs := make([]any, len(s.BranchRuns))
	for key, mini := range s.BranchRuns {
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || i >= len(outputs) {
			return Event{}, fmt.Errorf("malformed branch key %q", key)
		}
		doc, err := mini.currentDoc(deref)
		if err != nil {
			return Event{}, err
		}
		outputs[i] = doc
	}

	regionInput, err := s.currentDoc(deref)
	if err != nil {
		return Event{}, err
	}
	shaped, err := shapeOutput(st, regionInput, outputs)
	if err != nil {
		if errors.Is(err, errInvalidPath) {
			return Event{Type: EvRunFailed, ErrorType: fv1.WorkflowErrInvalidPath, Cause: causeOf(err)}, nil
		}
		return Event{}, err
	}
	raw, err := json.Marshal(shaped)
	if err != nil {
		return Event{}, fmt.Errorf("encoding join output: %w", err)
	}
	if len(raw) > spillThreshold {
		ref, err := spill(ctx, e.kv, run.Namespace, run.Name, s.Current+"-join", 0, raw)
		if err != nil {
			return Event{}, err
		}
		return Event{Type: EvBranchesJoined, OutputRef: ref}, nil
	}
	return Event{Type: EvBranchesJoined, Output: raw}, nil
}

// foldTail reads and folds everything past the state's checkpoint.
func (e *Engine) foldTail(ctx context.Context, stream string, s *RunState, deref derefFn) error {
	for {
		events, err := e.el.Read(ctx, stream, s.LastSeq, readBatch)
		if err != nil {
			return fmt.Errorf("reading %s past %d: %w", stream, s.LastSeq, err)
		}
		if len(events) == 0 {
			return nil
		}
		if err := s.fold(events, deref); err != nil {
			return err
		}
	}
}

func (e *Engine) appendAt(ctx context.Context, stream string, expectedSeq int64, ev Event) (int64, error) {
	se, err := encodeEvent(ev)
	if err != nil {
		return 0, err
	}
	return e.el.Append(ctx, stream, expectedSeq, []statestore.Event{se})
}

// FailUnstartable terminally fails a run that can never start (e.g. its
// Workflow does not exist past the GitOps-ordering grace). Guarded like
// every other append: an existing terminal wins.
func (e *Engine) FailUnstartable(ctx context.Context, run *fv1.WorkflowRun, cause string) error {
	stream := streamName(run)
	head, err := e.el.Head(ctx, stream)
	if err != nil {
		return err
	}
	ev := Event{Type: EvRunFailed, ErrorType: fv1.WorkflowErrPermanentError, Cause: causeOf(fmt.Errorf("%s", cause))}
	return appendGuarded(ctx, e.el, stream, head, ev, func(raced Event) bool {
		switch raced.Type {
		case EvRunSucceeded, EvRunFailed, EvRunCancelled, EvRunTimedOut:
			return true
		default:
			return false
		}
	})
}

// armTimer enqueues the backoff delay. DedupKey collapses double-arms while
// the message is unsettled; if a message is ever lost to the DLQ the next
// resync re-arms (dedup only spans not-yet-settled messages) — the DLQ is
// not drained, timer loss heals via resync.
func (e *Engine) armTimer(ctx context.Context, run *fv1.WorkflowRun, act action) error {
	body, err := json.Marshal(timerMsg{
		Namespace: run.Namespace, Name: run.Name, UID: string(run.UID),
		Branch: act.branch, Region: act.region, State: act.state, Attempt: act.attempt,
	})
	if err != nil {
		return err
	}
	_, err = e.q.Enqueue(ctx, timerQueue, statestore.Message{Body: body}, statestore.EnqueueOptions{
		Delay:    act.delay,
		DedupKey: fmt.Sprintf("%s/%s/%s/%d", string(run.UID), act.branch, act.state, act.attempt),
	})
	if err != nil {
		return fmt.Errorf("arming timer for %s/%d: %w", act.state, act.attempt, err)
	}
	return nil
}

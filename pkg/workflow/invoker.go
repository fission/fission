// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/utils"
)

const (
	// defaultStepTimeout bounds one attempt when the state declares none;
	// it must sit below the run-level Timeout, never above.
	defaultStepTimeout = 5 * time.Minute
	// defaultInvokerWorkers bounds concurrent invocations across all runs —
	// the reconciler never invokes inline (a 5-minute step would pin a
	// bounded reconcile worker; the head-of-line class the executor's
	// specialization semaphore documents).
	defaultInvokerWorkers = 64
	// maxResultBytes caps a function response the engine will buffer.
	maxResultBytes = 4 << 20 // 4MiB
)

// invocation is one dispatched (state, attempt) execution.
type invocation struct {
	runKey      types.NamespacedName
	runUID      string
	stream      string
	namespace   string
	branch      string // parallel-region invocations; "" = main flow
	region      string // the region instance (rides into result events)
	state       string
	attempt     int32
	stateSpec   fv1.WorkflowState
	input       jsontext.Value
	expectedSeq int64
}

// Invoker executes Task invocations on a bounded worker pool and CAS-appends
// the classified outcome. Late or raced completions lose the CAS and are
// discarded — W2 (one result per attempt) and W4 (nothing after a terminal)
// hold by construction.
type Invoker struct {
	logger    logr.Logger
	client    *http.Client // transport pre-wrapped with the HMAC signer
	routerURL string
	el        statestore.EventLog
	kv        statestore.KVStore
	wake      func(types.NamespacedName)
	sem       chan struct{}
	baseCtx   context.Context

	// inflight dedups dispatches: the 60s resync recomputes actInvoke for
	// attempts that are still executing, and re-running a long step's side
	// effects (or filling the pool with duplicates) must not happen.
	//
	// done closes the window inflight leaves open: a reconcile folds the log,
	// computes actInvoke for an attempt that is still unresolved in THAT
	// snapshot, and only then dispatches — if the attempt's result landed in
	// between (and its inflight key was released), the dispatch would be a
	// duplicate delivery of a step this process already completed. Remembering
	// completed attempts per run makes that dispatch a no-op. Cross-process
	// duplicates (a restart replays the log) remain — at-least-once is the
	// contract (workflowfold.tla) — but the in-process ones are free to avoid,
	// and they are what let a region exceed MaxConcurrency at the function.
	//
	// Each entry carries the seq that settled its attempt. An entry is needed
	// only until a reconcile folds past that seq: reconciles of one run are
	// serialized and every later dispatch is computed from that fold or a
	// newer one, so Prune drops it then — a run that loops through regions
	// keeps no completion history. Forget drops a run's set when it reaches a
	// terminal phase or is cleaned up. The set is created by Dispatch, never
	// by markDone, so a worker that finishes after Forget cannot resurrect it
	// (its result is discarded against the terminal anyway).
	mu       sync.Mutex
	inflight map[string]bool             // attemptKey
	done     map[string]map[string]int64 // runUID -> attemptKey -> settling seq
}

type InvokerOptions struct {
	Logger    logr.Logger
	Client    *http.Client
	RouterURL string
	EventLog  statestore.EventLog
	KV        statestore.KVStore
	Wake      func(types.NamespacedName)
	Workers   int             // 0 = defaultInvokerWorkers
	BaseCtx   context.Context // lifecycle of dispatched work; nil = Background
}

func NewInvoker(o InvokerOptions) *Invoker {
	if o.Workers <= 0 {
		o.Workers = defaultInvokerWorkers
	}
	if o.BaseCtx == nil {
		o.BaseCtx = context.Background()
	}
	return &Invoker{
		logger: o.Logger, client: o.Client, routerURL: o.RouterURL,
		el: o.EventLog, kv: o.KV, wake: o.Wake,
		sem: make(chan struct{}, o.Workers), baseCtx: o.BaseCtx,
		inflight: map[string]bool{},
		done:     map[string]map[string]int64{},
	}
}

// Dispatch runs the invocation on the pool WITHOUT ever blocking the
// reconciler: a duplicate of an in-flight attempt is a no-op (the resync
// recomputes actInvoke for attempts that are still executing), and a full
// pool skips the dispatch — the wake on completion or the next resync
// retries. Blocking here would freeze the whole run control plane behind
// long steps (the head-of-line class the executor's specialization
// semaphore documents).
func (inv *Invoker) Dispatch(iv invocation) {
	key := attemptKey(iv)
	inv.mu.Lock()
	if inv.inflight[key] {
		inv.mu.Unlock()
		return
	}
	if _, completed := inv.done[iv.runUID][key]; completed {
		// The result is already in the log; the caller's fold snapshot just
		// predates it. The next resync sees it and computes no invoke.
		inv.mu.Unlock()
		return
	}
	select {
	case inv.sem <- struct{}{}:
	default:
		inv.mu.Unlock()
		inv.logger.V(1).Info("invoker pool saturated; deferring to resync", "run", iv.runKey, "state", iv.state)
		return
	}
	inv.inflight[key] = true
	if inv.done[iv.runUID] == nil {
		inv.done[iv.runUID] = map[string]int64{}
	}
	inv.mu.Unlock()

	go func() {
		defer func() {
			inv.mu.Lock()
			delete(inv.inflight, key)
			inv.mu.Unlock()
			<-inv.sem
		}()
		inv.run(iv)
	}()
}

func (inv *Invoker) run(iv invocation) {
	begin := time.Now()
	result := inv.execute(iv)
	outcome := "success"
	switch {
	case result.skip:
		outcome = "aborted"
	case !result.succeeded:
		outcome = "failure"
	}
	recordStepDuration(inv.baseCtx, iv.state, outcome, time.Since(begin))
	if result.skip {
		// Process shutdown mid-invocation: append nothing — a restart must
		// never consume an attempt. The replay re-invokes.
		return
	}
	seq, err := inv.appendResult(iv, result)
	if err != nil {
		// Deliberately NO wake here: waking on a failed append would hot-loop
		// re-invocations of the same attempt (side effects included) as fast
		// as the function responds. The 60s resync is the retry cadence.
		inv.logger.Error(err, "recording step result (the resync will re-drive)", "run", iv.runKey, "state", iv.state, "attempt", iv.attempt)
		return
	}
	// Mark done BEFORE the inflight key is released (the deferred delete in
	// Dispatch runs after this returns), so there is no instant at which a
	// completed attempt is neither in flight nor remembered.
	inv.markDone(iv, seq)
	inv.wake(iv.runKey)
}

// attemptKey identifies one attempt of one step for the inflight and done
// sets. Dispatch, markDone and the tests must all build it here: two spellings
// that drift apart would make the completed-attempt guard silently miss.
//
// The region instance is part of the key: every Parallel/Map region starts
// fresh branch mini-runs, so successive regions (or one region re-entered by
// a loop) reuse branch "0", the same state names and attempt 1, and a stale
// record would suppress the later region's first delivery. Prune already
// drops a closed region's records before the next region dispatches (entering
// it means folding the join, which follows every branch result), so the
// region here keeps the key unambiguous rather than closing a live gap.
func attemptKey(iv invocation) string {
	return iv.runUID + "/" + iv.region + "/" + iv.branch + "/" + stepKey(iv.state, iv.attempt)
}

// markDone records a completed attempt, settled at seq, in the run's set. It
// never creates the set: if Forget has already dropped it, the run is
// terminal or deleted and the record would only leak.
func (inv *Invoker) markDone(iv invocation, seq int64) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if keys := inv.done[iv.runUID]; keys != nil {
		keys[attemptKey(iv)] = seq
	}
}

// Prune drops the completion records a fold up to foldedSeq has consumed.
// Records settled after it stay: they guard the window this fold's
// dispatches may still race.
func (inv *Invoker) Prune(runUID string, foldedSeq int64) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	maps.DeleteFunc(inv.done[runUID], func(_ string, seq int64) bool { return seq <= foldedSeq })
}

// Forget drops the completed-attempt memory for a run. Called when the run
// reaches a terminal phase (no attempt of it is ever dispatched again) and
// from CleanupRun, so the map is bounded by the number of live runs.
func (inv *Invoker) Forget(runUID string) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	delete(inv.done, runUID)
}

// outcome is a classified invocation result per the RFC error model. skip
// means "append nothing" (process shutdown mid-flight).
type outcome struct {
	succeeded bool
	skip      bool
	body      jsontext.Value
	errorType string
	cause     jsontext.Value
}

func (inv *Invoker) execute(iv invocation) outcome {
	timeout := defaultStepTimeout
	if iv.stateSpec.Timeout != nil {
		timeout = iv.stateSpec.Timeout.Duration
	}
	ctx, cancel := context.WithTimeout(inv.baseCtx, timeout)
	defer cancel()

	// Defense-in-depth: the snapshot is admission-validated, but the engine
	// treats it as authoritative forever — re-check the one field that
	// becomes a URL path segment before building the request.
	if err := fv1.ValidateKubeName("function", iv.stateSpec.Function.Name); err != nil {
		return outcome{errorType: fv1.WorkflowErrPermanentError, cause: causeOf(err)}
	}

	// The function receives the InputPath-selected view; the raw flowing
	// document stays in iv.input for the ResultPath merge (ASL semantics:
	// ResultPath merges into the pre-InputPath input).
	body, err := functionBody(iv)
	if err != nil {
		return outcome{errorType: fv1.WorkflowErrPermanentError, cause: causeOf(err)}
	}

	// RFC-0025 alias/version routing is deliberately NOT wired here:
	// WorkflowState.Function's alias/version semantics for a live-tracking
	// step are an explicitly deferred RFC-0025 decision (see
	// workflow_validation.go's Task-state guard, which rejects a state
	// carrying either field at admission) — plumbing the suffix through here
	// would make live-tracking functional and foreclose that decision.
	url := inv.routerURL + utils.UrlForFunction(iv.stateSpec.Function.Name, iv.namespace)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return outcome{errorType: fv1.WorkflowErrFunctionError, cause: causeOf(err)}
	}
	req.Header.Set("Content-Type", "application/json")
	// Idempotency contract: functions dedup on (run, attempt).
	req.Header.Set("X-Fission-Workflow-Run", iv.runUID)
	req.Header.Set("X-Fission-Workflow-Attempt", strconv.Itoa(int(iv.attempt)))
	if iv.branch != "" {
		req.Header.Set("X-Fission-Workflow-Branch", iv.branch)
	}

	resp, err := inv.client.Do(req)
	if err != nil {
		if inv.baseCtx.Err() != nil {
			// Process shutdown, not a step timeout: a pod restart must never
			// consume an attempt.
			return outcome{skip: true}
		}
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return outcome{errorType: fv1.WorkflowErrTimeout, cause: causeOf(err)}
		}
		return outcome{errorType: fv1.WorkflowErrFunctionError, cause: causeOf(err)}
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxResultBytes+1))
	if err != nil {
		return outcome{errorType: fv1.WorkflowErrFunctionError, cause: causeOf(err)}
	}
	if len(body) > maxResultBytes {
		return outcome{errorType: fv1.WorkflowErrFunctionError, cause: causeOf(fmt.Errorf("response exceeds %d bytes", maxResultBytes))}
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return outcome{succeeded: true, body: normalizeJSON(body)}
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return classifyError(body, fv1.WorkflowErrPermanentError)
	default:
		return classifyError(body, fv1.WorkflowErrFunctionError)
	}
}

// functionBody is the request body the function sees: InputPath-shaped when
// set, the raw flowing document verbatim (byte-identical) otherwise.
func functionBody(iv invocation) (jsontext.Value, error) {
	if iv.stateSpec.InputPath == "" {
		return iv.input, nil
	}
	var doc any
	if len(iv.input) > 0 {
		if err := json.Unmarshal(iv.input, &doc, docDecOpts); err != nil {
			return nil, fmt.Errorf("decoding step input: %w", err)
		}
	}
	selected, err := applyInputPath(iv.stateSpec, doc)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(selected, docEncOpts)
	if err != nil {
		return nil, fmt.Errorf("encoding step input: %w", err)
	}
	return out, nil
}

// classifyError extracts a typed error ({"errorType": ..., "cause": ...})
// from a non-2xx body, falling back to the status-class built-in.
func classifyError(body []byte, fallback string) outcome {
	var typed struct {
		ErrorType string         `json:"errorType"`
		Cause     jsontext.Value `json:"cause"`
	}
	if err := json.Unmarshal(body, &typed, docDecOpts); err == nil && typed.ErrorType != "" {
		return outcome{errorType: typed.ErrorType, cause: typed.Cause}
	}
	return outcome{errorType: fallback, cause: normalizeJSON(body)}
}

// normalizeJSON keeps valid JSON as-is and wraps anything else as a string,
// so events always carry valid JSON.
func normalizeJSON(body []byte) jsontext.Value {
	if len(body) == 0 {
		return jsontext.Value("null")
	}
	if jsontext.Value(body).IsValid(docDecOpts) {
		return body
	}
	quoted, _ := json.Marshal(string(body), docEncOpts)
	return quoted
}

func causeOf(err error) jsontext.Value {
	quoted, _ := json.Marshal(err.Error(), docEncOpts)
	return quoted
}

// appendResult shapes the outcome into the next state's document and
// CAS-appends it. The conflict guard drops the append when a result for this
// attempt already landed (W2) or a terminal event is present (W4).
func (inv *Invoker) appendResult(iv invocation, res outcome) (int64, error) {
	var ev Event
	if !res.succeeded {
		ev = Event{Type: EvStepFailed, State: iv.state, Branch: iv.branch, Region: iv.region, Attempt: iv.attempt, ErrorType: res.errorType, Cause: res.cause}
	} else {
		nextDoc, err := inv.documentAfterSuccess(iv, res.body)
		if err != nil {
			if errors.Is(err, errInvalidPath) {
				ev = Event{Type: EvStepFailed, State: iv.state, Branch: iv.branch, Region: iv.region, Attempt: iv.attempt,
					ErrorType: fv1.WorkflowErrInvalidPath, Cause: causeOf(err)}
			} else {
				return 0, err
			}
		} else if len(nextDoc) > spillThreshold {
			ref, err := spill(inv.baseCtx, inv.kv, iv.namespace, iv.runKey.Name, spillKeyPrefix(iv.branch, iv.state), iv.attempt, nextDoc)
			if err != nil {
				return 0, err
			}
			ev = Event{Type: EvStepSucceeded, State: iv.state, Branch: iv.branch, Region: iv.region, Attempt: iv.attempt, OutputRef: ref}
		} else {
			ev = Event{Type: EvStepSucceeded, State: iv.state, Branch: iv.branch, Region: iv.region, Attempt: iv.attempt, Output: nextDoc}
		}
	}
	return appendGuarded(inv.baseCtx, inv.el, iv.stream, iv.expectedSeq, ev, resultGuard(iv.region, iv.branch, iv.state, iv.attempt))
}

// documentAfterSuccess merges the function result into the state's input per
// ResultPath/OutputPath, producing the next state's document.
func (inv *Invoker) documentAfterSuccess(iv invocation, body jsontext.Value) (jsontext.Value, error) {
	var input, result any
	if len(iv.input) > 0 {
		if err := json.Unmarshal(iv.input, &input, docDecOpts); err != nil {
			return nil, fmt.Errorf("decoding step input: %w", err)
		}
	}
	// normalizeJSON already ran on the body (non-JSON success responses —
	// plain-text functions — become JSON strings), so this cannot fail on
	// function output; a failure here means engine corruption.
	if err := json.Unmarshal(body, &result, docDecOpts); err != nil {
		return nil, fmt.Errorf("decoding step result: %w", err)
	}
	next, err := applyOutputPath(iv.stateSpec, input, result)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(next, docEncOpts)
	if err != nil {
		return nil, fmt.Errorf("encoding next document: %w", err)
	}
	return out, nil
}

// resultGuard drops an append when the log already resolved this attempt or
// went terminal.
func resultGuard(region, branch, state string, attempt int32) func(Event) bool {
	return func(e Event) bool {
		switch e.Type {
		case EvStepSucceeded, EvStepFailed:
			return e.Region == region && e.Branch == branch && e.State == state && e.Attempt == attempt
		case EvBranchesJoined:
			// The region closed (W8): any late branch result is discarded.
			return branch != ""
		default:
			return isTerminalEvent(e.Type)
		}
	}
}

// spillKeyPrefix scopes spill keys per branch; the main flow keeps the
// phase-2 key shape.
func spillKeyPrefix(branch, state string) string {
	if branch == "" {
		return state
	}
	return branch + "/" + state
}

// appendGuarded CAS-appends ev at expectedSeq; on conflict it re-reads the
// tail and drops silently if any event matches the guard (the race was
// decided by someone else — expected, not an error), otherwise retries at
// the new head. On success it returns the seq that settles the race: ev's
// own seq when appended, the deciding event's seq when dropped, or the head
// of a trimmed stream — a fold that reaches it has seen the outcome.
func appendGuarded(ctx context.Context, el statestore.EventLog, stream string, expectedSeq int64, ev Event, dropIf func(Event) bool) (int64, error) {
	se, err := encodeEvent(ev)
	if err != nil {
		return 0, err
	}
	for {
		head, err := el.Append(ctx, stream, expectedSeq, []statestore.Event{se})
		if err == nil {
			return head, nil
		}
		if !errors.Is(err, statestore.ErrVersionConflict) {
			return 0, err
		}
		// Walk the events we lost to; drop if the race is already decided.
		sawAny := false
		for seq := expectedSeq; seq < head; {
			events, err := el.Read(ctx, stream, seq, readBatch)
			if err != nil {
				return 0, err
			}
			if len(events) == 0 {
				break
			}
			sawAny = true
			for _, raced := range events {
				decoded, err := decodeEvent(raced)
				if err != nil {
					return 0, err
				}
				if dropIf(decoded) {
					return raced.Seq, nil
				}
				seq = raced.Seq
			}
		}
		if !sawAny && head > 0 {
			// head > 0 with an empty walk means the stream was TRIMMED —
			// the run is deleted and cleanup already ran. Appending would
			// recreate rows in a reclaimed stream with no CR to ever clean
			// them; drop instead.
			return head, nil
		}
		expectedSeq = head
	}
}

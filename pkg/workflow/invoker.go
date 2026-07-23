// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	input       json.RawMessage
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
	mu       sync.Mutex
	inflight map[string]bool // "uid/state/attempt"
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
	key := iv.runUID + "/" + iv.branch + "/" + stepKey(iv.state, iv.attempt)
	inv.mu.Lock()
	if inv.inflight[key] {
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
	if err := inv.appendResult(iv, result); err != nil {
		// Deliberately NO wake here: waking on a failed append would hot-loop
		// re-invocations of the same attempt (side effects included) as fast
		// as the function responds. The 60s resync is the retry cadence.
		inv.logger.Error(err, "recording step result (the resync will re-drive)", "run", iv.runKey, "state", iv.state, "attempt", iv.attempt)
		return
	}
	inv.wake(iv.runKey)
}

// outcome is a classified invocation result per the RFC error model. skip
// means "append nothing" (process shutdown mid-flight).
type outcome struct {
	succeeded bool
	skip      bool
	body      json.RawMessage
	errorType string
	cause     json.RawMessage
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

	// RFC-0025: append the alias/version suffix when the Task state's
	// FunctionReference carries one; resolution stays entirely router-side.
	suffix := iv.stateSpec.Function.Alias
	if suffix == "" {
		suffix = iv.stateSpec.Function.Version
	}
	url := inv.routerURL + utils.UrlForFunctionRef(iv.stateSpec.Function.Name, iv.namespace, suffix)
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
func functionBody(iv invocation) (json.RawMessage, error) {
	if iv.stateSpec.InputPath == "" {
		return iv.input, nil
	}
	var doc any
	if len(iv.input) > 0 {
		if err := json.Unmarshal(iv.input, &doc); err != nil {
			return nil, fmt.Errorf("decoding step input: %w", err)
		}
	}
	shaped, err := shapeInput(iv.stateSpec, doc)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(shaped)
	if err != nil {
		return nil, fmt.Errorf("encoding shaped input: %w", err)
	}
	return out, nil
}

// classifyError extracts a typed error ({"errorType": ..., "cause": ...})
// from a non-2xx body, falling back to the status-class built-in.
func classifyError(body []byte, fallback string) outcome {
	var typed struct {
		ErrorType string          `json:"errorType"`
		Cause     json.RawMessage `json:"cause"`
	}
	if err := json.Unmarshal(body, &typed); err == nil && typed.ErrorType != "" {
		return outcome{errorType: typed.ErrorType, cause: typed.Cause}
	}
	return outcome{errorType: fallback, cause: normalizeJSON(body)}
}

// normalizeJSON keeps valid JSON as-is and wraps anything else as a string,
// so events always carry valid JSON.
func normalizeJSON(body []byte) json.RawMessage {
	if len(body) == 0 {
		return json.RawMessage("null")
	}
	if json.Valid(body) {
		return body
	}
	quoted, _ := json.Marshal(string(body))
	return quoted
}

func causeOf(err error) json.RawMessage {
	quoted, _ := json.Marshal(err.Error())
	return quoted
}

// appendResult shapes the outcome into the next state's document and
// CAS-appends it. The conflict guard drops the append when a result for this
// attempt already landed (W2) or a terminal event is present (W4).
func (inv *Invoker) appendResult(iv invocation, res outcome) error {
	var ev Event
	if !res.succeeded {
		ev = Event{Type: EvStepFailed, State: iv.state, Branch: iv.branch, Region: iv.region, Attempt: iv.attempt, ErrorType: res.errorType, Cause: res.cause}
	} else {
		nextDoc, err := inv.shapeSuccess(iv, res.body)
		if err != nil {
			if errors.Is(err, errInvalidPath) {
				ev = Event{Type: EvStepFailed, State: iv.state, Branch: iv.branch, Region: iv.region, Attempt: iv.attempt,
					ErrorType: fv1.WorkflowErrInvalidPath, Cause: causeOf(err)}
			} else {
				return err
			}
		} else if len(nextDoc) > spillThreshold {
			ref, err := spill(inv.baseCtx, inv.kv, iv.namespace, iv.runKey.Name, spillKeyPrefix(iv.branch, iv.state), iv.attempt, nextDoc)
			if err != nil {
				return err
			}
			ev = Event{Type: EvStepSucceeded, State: iv.state, Branch: iv.branch, Region: iv.region, Attempt: iv.attempt, OutputRef: ref}
		} else {
			ev = Event{Type: EvStepSucceeded, State: iv.state, Branch: iv.branch, Region: iv.region, Attempt: iv.attempt, Output: nextDoc}
		}
	}
	return appendGuarded(inv.baseCtx, inv.el, iv.stream, iv.expectedSeq, ev, resultGuard(iv.region, iv.branch, iv.state, iv.attempt))
}

// shapeSuccess merges the function result into the state's input per
// Result/OutputPath, producing the next state's document.
func (inv *Invoker) shapeSuccess(iv invocation, body json.RawMessage) (json.RawMessage, error) {
	var input, result any
	if len(iv.input) > 0 {
		if err := json.Unmarshal(iv.input, &input); err != nil {
			return nil, fmt.Errorf("decoding step input: %w", err)
		}
	}
	// normalizeJSON already ran on the body (non-JSON success responses —
	// plain-text functions — become JSON strings), so this cannot fail on
	// function output; a failure here means engine corruption.
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decoding step result: %w", err)
	}
	shaped, err := shapeOutput(iv.stateSpec, input, result)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(shaped)
	if err != nil {
		return nil, fmt.Errorf("encoding shaped output: %w", err)
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
// the new head.
func appendGuarded(ctx context.Context, el statestore.EventLog, stream string, expectedSeq int64, ev Event, dropIf func(Event) bool) error {
	se, err := encodeEvent(ev)
	if err != nil {
		return err
	}
	for {
		head, err := el.Append(ctx, stream, expectedSeq, []statestore.Event{se})
		if err == nil {
			return nil
		}
		if !errors.Is(err, statestore.ErrVersionConflict) {
			return err
		}
		// Walk the events we lost to; drop if the race is already decided.
		sawAny := false
		for seq := expectedSeq; seq < head; {
			events, err := el.Read(ctx, stream, seq, readBatch)
			if err != nil {
				return err
			}
			if len(events) == 0 {
				break
			}
			sawAny = true
			for _, raced := range events {
				decoded, err := decodeEvent(raced)
				if err != nil {
					return err
				}
				if dropIf(decoded) {
					return nil
				}
				seq = raced.Seq
			}
		}
		if !sawAny && head > 0 {
			// head > 0 with an empty walk means the stream was TRIMMED —
			// the run is deleted and cleanup already ran. Appending would
			// recreate rows in a reclaimed stream with no CR to ever clean
			// them; drop instead.
			return nil
		}
		expectedSeq = head
	}
}

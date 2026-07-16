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
	}
}

// Dispatch runs the invocation on the pool. It never blocks the reconciler
// beyond pool admission.
func (inv *Invoker) Dispatch(iv invocation) {
	inv.sem <- struct{}{}
	go func() {
		defer func() { <-inv.sem }()
		inv.run(iv)
		inv.wake(iv.runKey)
	}()
}

func (inv *Invoker) run(iv invocation) {
	result := inv.execute(iv)
	if err := inv.appendResult(iv, result); err != nil {
		// The next resync re-invokes (at-least-once); nothing to do here.
		inv.logger.Error(err, "recording step result", "run", iv.runKey, "state", iv.state, "attempt", iv.attempt)
	}
}

// outcome is a classified invocation result per the RFC error model.
type outcome struct {
	succeeded bool
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

	url := inv.routerURL + utils.UrlForFunction(iv.stateSpec.Function.Name, iv.namespace)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(iv.input))
	if err != nil {
		return outcome{errorType: fv1.WorkflowErrFunctionError, cause: causeOf(err)}
	}
	req.Header.Set("Content-Type", "application/json")
	// Idempotency contract: functions dedup on (run, attempt).
	req.Header.Set("X-Fission-Workflow-Run", iv.runUID)
	req.Header.Set("X-Fission-Workflow-Attempt", strconv.Itoa(int(iv.attempt)))

	resp, err := inv.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return outcome{errorType: fv1.WorkflowErrTimeout, cause: causeOf(err)}
		}
		return outcome{errorType: fv1.WorkflowErrFunctionError, cause: causeOf(err)}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResultBytes+1))
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
		ev = Event{Type: EvStepFailed, State: iv.state, Attempt: iv.attempt, ErrorType: res.errorType, Cause: res.cause}
	} else {
		nextDoc, err := inv.shapeSuccess(iv, res.body)
		if err != nil {
			if errors.Is(err, errInvalidPath) {
				ev = Event{Type: EvStepFailed, State: iv.state, Attempt: iv.attempt,
					ErrorType: fv1.WorkflowErrInvalidPath, Cause: causeOf(err)}
			} else {
				return err
			}
		} else if len(nextDoc) > spillThreshold {
			ref, err := spill(inv.baseCtx, inv.kv, iv.namespace, iv.runKey.Name, iv.state, iv.attempt, nextDoc)
			if err != nil {
				return err
			}
			ev = Event{Type: EvStepSucceeded, State: iv.state, Attempt: iv.attempt, OutputRef: ref}
		} else {
			ev = Event{Type: EvStepSucceeded, State: iv.state, Attempt: iv.attempt, Output: nextDoc}
		}
	}
	return appendGuarded(inv.baseCtx, inv.el, iv.stream, iv.expectedSeq, ev, resultGuard(iv.state, iv.attempt))
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
func resultGuard(state string, attempt int32) func(Event) bool {
	return func(e Event) bool {
		switch e.Type {
		case EvStepSucceeded, EvStepFailed:
			return e.State == state && e.Attempt == attempt
		case EvRunSucceeded, EvRunFailed, EvRunCancelled, EvRunTimedOut:
			return true
		default:
			return false
		}
	}
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
		for seq := expectedSeq; seq < head; {
			events, err := el.Read(ctx, stream, seq, readBatch)
			if err != nil {
				return err
			}
			if len(events) == 0 {
				break
			}
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
		expectedSeq = head
	}
}

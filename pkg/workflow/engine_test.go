// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/memory"
)

// harness is a full engine wired to the memory statestore and a scripted
// httptest router.
type harness struct {
	t      *testing.T
	caps   statestore.Capabilities
	el     statestore.EventLog
	q      statestore.Queue
	kv     statestore.KVStore
	engine *Engine
	run    *fv1.WorkflowRun
	spec   *fv1.WorkflowSpec
	server *httptest.Server

	mu    sync.Mutex
	calls map[string]int // function name -> invocations

	// script maps function name to per-call HTTP status; missing = 200.
	script map[string][]int

	wakes atomic.Int64
}

func newHarness(t *testing.T, spec *fv1.WorkflowSpec) *harness {
	t.Helper()

	caps, err := memory.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })

	el, err := caps.EventLog()
	require.NoError(t, err)
	q, err := caps.Queue()
	require.NoError(t, err)
	kv, err := caps.KV()
	require.NoError(t, err)

	h := &harness{t: t, caps: caps, el: el, q: q, kv: kv, spec: spec,
		calls: map[string]int{}, script: map[string][]int{}}

	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /fission-function/<name> (default ns is folded).
		name := strings.TrimPrefix(r.URL.Path, "/fission-function/")
		h.mu.Lock()
		h.calls[name]++
		call := h.calls[name]
		status := http.StatusOK
		if s := h.script[name]; call <= len(s) {
			status = s[call-1]
		}
		h.mu.Unlock()

		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"failed":true}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"fn":"` + name + `","call":` + strconv.Itoa(call) + `}`))
	}))
	t.Cleanup(h.server.Close)

	h.run = &fv1.WorkflowRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default", UID: types.UID("uid-run-1")},
		Spec: fv1.WorkflowRunSpec{
			WorkflowRef: "wf",
			Input:       &runtime.RawExtension{Raw: []byte(`{"seed":1}`)},
		},
	}

	h.engine = h.newEngine()
	return h
}

// newEngine builds a fresh engine over the SAME statestore — the
// crash-and-resume primitive.
func (h *harness) newEngine() *Engine {
	wake := func(types.NamespacedName) { h.wakes.Add(1) }
	inv := NewInvoker(InvokerOptions{
		Logger: logr.Discard(), Client: h.server.Client(), RouterURL: h.server.URL,
		EventLog: h.el, KV: h.kv, Wake: wake, Workers: 4,
	})
	return NewEngine(EngineOptions{
		Logger: logr.Discard(), EventLog: h.el, Queue: h.q, KV: h.kv,
		Invoker: inv, Wake: wake,
		Rand: func() float64 { return 0.5 }, // deterministic backoff
	})
}

func (h *harness) fetch(context.Context) (*fv1.WorkflowSpec, error) { return h.spec, nil }

// drive reconciles + drains timers until the run reaches a terminal phase or
// the deadline passes. It polls: production wakes are event-driven, but the
// test loop only needs eventual progress.
func (h *harness) drive(t *testing.T, e *Engine, deadline time.Duration) *RunState {
	t.Helper()
	ctx := t.Context()
	var s *RunState
	require.Eventually(t, func() bool {
		e.timerPollOnce(ctx)
		var err error
		s, err = e.Reconcile(ctx, h.run, h.fetch)
		require.NoError(t, err)
		return s.Terminal != ""
	}, deadline, 10*time.Millisecond)
	return s
}

func (h *harness) log(t *testing.T) []Event {
	t.Helper()
	raw, err := h.el.Read(t.Context(), streamName(h.run), 0, 10_000)
	require.NoError(t, err)
	out := make([]Event, 0, len(raw))
	for _, se := range raw {
		e, err := decodeEvent(se)
		require.NoError(t, err)
		out = append(out, e)
	}
	return out
}

// assertInvariants re-verifies W1-W6 over the final log.
func assertInvariants(t *testing.T, log []Event, maxAttempts int) {
	t.Helper()
	scheds := map[string]int{}
	results := map[string]int{}
	terminalAt := -1
	for i, e := range log {
		switch e.Type {
		case EvStepScheduled:
			scheds[stepKey(e.State, e.Attempt)]++
			assert.LessOrEqual(t, int(e.Attempt), maxAttempts, "W6: attempt within budget")
		case EvStepSucceeded, EvStepFailed:
			key := stepKey(e.State, e.Attempt)
			results[key]++
			assert.Positive(t, scheds[key], "W3: result only after its schedule")
		case EvRunSucceeded, EvRunFailed, EvRunCancelled, EvRunTimedOut:
			terminalAt = i
		}
	}
	for key, n := range scheds {
		assert.Equal(t, 1, n, "W1: %s scheduled once", key)
	}
	for key, n := range results {
		assert.Equal(t, 1, n, "W2: one result for %s", key)
	}
	require.GreaterOrEqual(t, terminalAt, 0, "run reached a terminal event")
	assert.Equal(t, len(log)-1, terminalAt, "W4: terminal is last")
}

func TestEngineLinearPipeline(t *testing.T) {
	t.Parallel()

	h := newHarness(t, pipelineSpec())
	s := h.drive(t, h.engine, 10*time.Second)

	assert.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)
	assert.JSONEq(t, `{"fn":"fn-b","call":1}`, string(s.Output))
	assertInvariants(t, h.log(t), 1)
	assert.Equal(t, map[string]int{"fn-a": 1, "fn-b": 1}, h.calls)
}

func TestEngineRetryThenCatch(t *testing.T) {
	t.Parallel()

	spec := pipelineSpec()
	a := spec.States["a"]
	a.Retry = &fv1.RetryPolicy{
		MaxAttempts: new(2),
		BackoffBase: &metav1.Duration{Duration: time.Millisecond},
		BackoffCap:  &metav1.Duration{Duration: 2 * time.Millisecond},
	}
	a.Catch = []fv1.WorkflowCatchRoute{{ErrorType: fv1.WorkflowErrAll, Next: "b"}}
	spec.States["a"] = a

	h := newHarness(t, spec)
	h.script["fn-a"] = []int{500, 500} // both attempts fail

	s := h.drive(t, h.engine, 10*time.Second)

	assert.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal, "catch routed to b, which succeeded")
	assertInvariants(t, h.log(t), 2)
	assert.Equal(t, 2, h.calls["fn-a"], "retried to budget")
	assert.Equal(t, 1, h.calls["fn-b"])
}

func TestEnginePermanentErrorFailsRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t, pipelineSpec())
	h.script["fn-a"] = []int{400} // permanent, no catch declared

	s := h.drive(t, h.engine, 10*time.Second)

	assert.Equal(t, fv1.WorkflowRunFailed, s.Terminal)
	assert.Equal(t, fv1.WorkflowErrPermanentError, s.ErrorType)
	assert.Equal(t, 1, h.calls["fn-a"], "4xx never retries")
	assertInvariants(t, h.log(t), 1)
}

func TestEngineCancellation(t *testing.T) {
	t.Parallel()

	h := newHarness(t, pipelineSpec())
	h.run.Annotations = map[string]string{CancelAnnotation: "true"}

	s := h.drive(t, h.engine, 5*time.Second)
	assert.Equal(t, fv1.WorkflowRunCancelled, s.Terminal)
}

// TestEngineCrashPointResume is the RFC's flagship verification: kill the
// engine at every point of the step lifecycle, resume with a fresh instance
// over the same store, and assert convergence with the invariants intact.
// Crash points are expressed as "reconcile/poll N times, then switch
// engines" — each bounded prefix of the drive loop IS a crash point (the
// abandoned instance simply never runs again).
func TestEngineCrashPointResume(t *testing.T) {
	t.Parallel()

	spec := pipelineSpec()
	a := spec.States["a"]
	a.Retry = &fv1.RetryPolicy{
		MaxAttempts: new(2),
		BackoffBase: &metav1.Duration{Duration: time.Millisecond},
		BackoffCap:  &metav1.Duration{Duration: 2 * time.Millisecond},
	}
	spec.States["a"] = a

	// 0..6 pre-crash iterations walk the crash window across: before
	// RunStarted, after schedule/before result, between result and next
	// schedule, between fail and timer, after timer/before reschedule.
	for crashAfter := range 7 {
		t.Run(map[bool]string{true: "crash-early"}[crashAfter < 2]+string(rune('0'+crashAfter)), func(t *testing.T) {
			t.Parallel()

			h := newHarness(t, spec)
			h.script["fn-a"] = []int{500} // first attempt fails, second succeeds

			first := h.engine
			ctx := t.Context()
			for range crashAfter {
				first.timerPollOnce(ctx)
				_, err := first.Reconcile(ctx, h.run, h.fetch)
				require.NoError(t, err)
				time.Sleep(5 * time.Millisecond) // let dispatched workers land
			}

			// The first engine is abandoned mid-flight; a fresh instance
			// resumes from the same store.
			s := h.drive(t, h.newEngine(), 10*time.Second)

			assert.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)
			assertInvariants(t, h.log(t), 2)
			assert.GreaterOrEqual(t, h.calls["fn-a"], 1, "at-least-once execution")
		})
	}
}

// TestEngineConcurrentReconcilers races two engine instances over the same
// run under -race: CAS must keep the log consistent (the TLA model's racing
// reconcilers).
func TestEngineConcurrentReconcilers(t *testing.T) {
	t.Parallel()

	h := newHarness(t, pipelineSpec())
	second := h.newEngine()

	ctx := t.Context()
	var wg sync.WaitGroup
	for _, e := range []*Engine{h.engine, second} {
		wg.Go(func() {
			for range 50 {
				e.timerPollOnce(ctx)
				_, err := e.Reconcile(ctx, h.run, h.fetch)
				if err != nil {
					// A conflict surfaced as error would be a bug; record it.
					assert.NoError(t, err)
					return
				}
				time.Sleep(2 * time.Millisecond)
			}
		})
	}
	wg.Wait()

	s := h.drive(t, h.engine, 10*time.Second)
	assert.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)
	assertInvariants(t, h.log(t), 1)
}

// TestEngineInputPathShapesRequestBody pins that a function receives the
// InputPath-selected view while ResultPath still merges into the raw flowing
// document (ASL semantics). The regression this guards: InputPath accepted
// at admission but silently ignored at invoke time.
func TestEngineInputPathShapesRequestBody(t *testing.T) {
	t.Parallel()

	spec := pipelineSpec()
	a := spec.States["a"]
	a.InputPath = "$.order"
	a.ResultPath = "$.charge"
	spec.States["a"] = a
	b := spec.States["b"]
	b.InputPath = "$.charge"
	spec.States["b"] = b

	h := newHarness(t, spec)
	h.run.Spec.Input = &runtime.RawExtension{Raw: []byte(`{"order":{"id":4711},"noise":true}`)}

	var mu sync.Mutex
	bodies := map[string]string{}
	h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/fission-function/")
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[name] = string(body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"txn":"t-1"}`))
	})

	s := h.drive(t, h.engine, 10*time.Second)
	require.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)

	mu.Lock()
	defer mu.Unlock()
	assert.JSONEq(t, `{"id":4711}`, bodies["fn-a"], "fn-a sees only $.order")
	assert.JSONEq(t, `{"txn":"t-1"}`, bodies["fn-b"], "fn-b sees $.charge merged by fn-a's ResultPath")
}

// TestEnginePlainTextFunction pins the lenient success contract: a function
// returning non-JSON (plain text) folds as a JSON string instead of wedging
// the run in a decode-retry loop.
func TestEnginePlainTextFunction(t *testing.T) {
	t.Parallel()

	h := newHarness(t, pipelineSpec())
	h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello, world!\n"))
	})

	s := h.drive(t, h.engine, 10*time.Second)
	assert.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)
	assert.JSONEq(t, `"hello, world!\n"`, string(s.Output))
}

// TestEngineSpillsLargeOutputs pins the 64KiB spill path end to end.
func TestEngineSpillsLargeOutputs(t *testing.T) {
	t.Parallel()

	spec := pipelineSpec()
	h := newHarness(t, spec)

	// fn-a returns a >64KiB payload.
	big := `{"blob":"` + strings.Repeat("x", spillThreshold+1024) + `"}`
	h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/fission-function/")
		h.mu.Lock()
		h.calls[name]++
		h.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		if name == "fn-a" {
			_, _ = w.Write([]byte(big))
			return
		}
		_, _ = w.Write([]byte(`{"small":true}`))
	})

	s := h.drive(t, h.engine, 10*time.Second)
	assert.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)

	var sawRef bool
	for _, e := range h.log(t) {
		if e.Type == EvStepSucceeded && e.State == "a" {
			assert.Empty(t, e.Output, "large output must not inline")
			assert.NotEmpty(t, e.OutputRef)
			sawRef = true
		}
	}
	assert.True(t, sawRef)
}

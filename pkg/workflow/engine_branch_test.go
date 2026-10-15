// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// assertRegionInvariants extends the W1-W6 log check with the join
// discipline (workflowbranch.tla W7/W8): join unique, only after every
// branch succeeded, and only the terminal follows it.
func assertRegionInvariants(t *testing.T, log []Event, branches int) {
	t.Helper()

	joinAt := -1
	okBranches := map[string]bool{}
	for i, e := range log {
		switch e.Type {
		case EvBranchesJoined:
			assert.Equal(t, -1, joinAt, "W7: join is unique")
			joinAt = i
			assert.Len(t, okBranches, branches, "W7: join only after every branch succeeded")
		case EvStepSucceeded:
			if e.Branch != "" {
				okBranches[e.Branch] = true
			}
		}
		if joinAt >= 0 && i > joinAt {
			assert.NotEqual(t, "", string(e.Type), "sanity")
			assert.True(t, e.Branch == "" || e.Type == EvTimerFired,
				"W8: no branch step events after join (got %s branch %s)", e.Type, e.Branch)
		}
	}
	require.GreaterOrEqual(t, joinAt, 0, "the region joined")
}

func TestEngineParallelJoin(t *testing.T) {
	t.Parallel()

	h := newHarness(t, fanSpec())
	s := h.drive(t, h.engine, 10*time.Second)

	require.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)
	var out []any
	require.NoError(t, json.Unmarshal(s.Output, &out))
	require.Len(t, out, 2, "join output is the ordered branch array")
	assert.Contains(t, out[0].(map[string]any)["fn"], "fn-x")
	assert.Contains(t, out[1].(map[string]any)["fn"], "fn-y")

	log := h.log(t)
	assertInvariants(t, log, 1)
	assertRegionInvariants(t, log, 2)
	assert.Equal(t, 1, attempts(log, "x"), "one attempt per branch state")
	assert.Equal(t, 1, attempts(log, "y"), "one attempt per branch state")
	assert.GreaterOrEqual(t, h.callCount("fn-x"), 1, "at-least-once execution")
	assert.GreaterOrEqual(t, h.callCount("fn-y"), 1, "at-least-once execution")
}

func TestEngineParallelFailFast(t *testing.T) {
	t.Parallel()

	h := newHarness(t, fanSpec())
	h.script["fn-x"] = []int{400} // permanent failure in branch 0

	s := h.drive(t, h.engine, 10*time.Second)

	require.Equal(t, fv1.WorkflowRunFailed, s.Terminal)
	assert.Equal(t, fv1.WorkflowErrBranchFailed, s.ErrorType)
	var cause map[string]any
	require.NoError(t, json.Unmarshal(s.Cause, &cause))
	assert.Equal(t, "0", cause["branch"])
	assertInvariants(t, h.log(t), 1) // incl. W4: terminal last
}

func TestEngineMap(t *testing.T) {
	t.Parallel()

	spec := fanSpec()
	spec.States["fan"] = fv1.WorkflowState{
		Type:      fv1.WorkflowStateMap,
		ItemsPath: "$.items",
		Branches:  []fv1.WorkflowBranch{spec.States["fan"].Branches[0]},
		// Cap concurrency below the item count to exercise the throttle.
		MaxConcurrency: 2,
		Next:           "done",
	}

	h := newHarness(t, spec)
	h.run.Spec.Input = &apiextensionsv1.JSON{Raw: []byte(`{"items":[1,2,3,4,5]}`)}

	// Track the max concurrent in-flight requests the throttle allows.
	var mu sync.Mutex
	inflight, maxInflight := 0, 0
	h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inflight++
		if inflight > maxInflight {
			maxInflight = inflight
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		inflight--
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	s := h.drive(t, h.engine, 15*time.Second)

	require.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)
	var out []any
	require.NoError(t, json.Unmarshal(s.Output, &out))
	assert.Len(t, out, 5, "one output per item, ordered")
	assertRegionInvariants(t, h.log(t), 5)

	mu.Lock()
	defer mu.Unlock()
	assert.LessOrEqual(t, maxInflight, 2, "MaxConcurrency throttles branch dispatch")
}

// TestInvokerSkipsCompletedAttemptInProcess pins the stale-snapshot guard:
// a dispatch for an attempt whose result this process already recorded is a
// no-op (no second delivery), and Forget releases that memory so a genuinely
// fresh replay (a restart, modeled by a forgotten run) invokes again. Without
// the guard, TestEngineMap's throttle overshoots: the reconcile folds the log,
// the attempt completes and releases its inflight key, then the dispatch
// computed from the older snapshot fires a duplicate alongside the branches
// opened by the completion.
func TestInvokerSkipsCompletedAttemptInProcess(t *testing.T) {
	t.Parallel()

	h := newHarness(t, pipelineSpec())
	s := h.drive(t, h.engine, 10*time.Second)
	require.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)
	before := h.callCount("fn-a")
	require.Equal(t, 1, before, "one delivery for the single attempt")

	// A dispatch computed from a snapshot that predates the result: the
	// attempt is complete in the log, so the invoker must not deliver again.
	inv := invocation{
		runKey: types.NamespacedName{Namespace: h.run.Namespace, Name: h.run.Name},
		runUID: string(h.run.UID), stream: streamName(h.run), namespace: h.run.Namespace,
		state: "a", attempt: 1, stateSpec: s.Spec.States["a"], input: []byte(`{}`),
		expectedSeq: 1,
	}
	// The terminal reconcile already called Forget; recreate the run's set
	// (as a Dispatch does) and mark the attempt, to model the window between
	// the result append and terminal (the run is still live).
	invoker := h.engine.invoker
	invoker.mu.Lock()
	invoker.done[inv.runUID] = map[string]struct{}{}
	invoker.mu.Unlock()
	invoker.markDone(inv)
	h.engine.invoker.Dispatch(inv)
	require.Never(t, func() bool { return h.callCount("fn-a") > before }, 200*time.Millisecond, 20*time.Millisecond,
		"a completed attempt must not be delivered again by the same process")

	// Forget (terminal / cleanup) drops the memory: a replay after a restart
	// is at-least-once by contract and delivers.
	h.engine.invoker.Forget(inv.runUID)
	h.engine.invoker.Dispatch(inv)
	require.Eventually(t, func() bool { return h.callCount("fn-a") == before+1 }, 5*time.Second, 10*time.Millisecond,
		"after Forget the attempt is deliverable again")
}

// TestEngineSuccessiveRegionsReuseBranchKeys: two Parallel regions in a row
// both run branch "0", state "x", attempt 1; only the region instance tells
// them apart. The completed-attempt guard must key on it, or the second
// region's first delivery is suppressed as already done and the run stalls.
func TestEngineSuccessiveRegionsReuseBranchKeys(t *testing.T) {
	t.Parallel()

	fn := &fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn-x"}
	region := func(next string) fv1.WorkflowState {
		return fv1.WorkflowState{
			Type: fv1.WorkflowStateParallel,
			Branches: []fv1.WorkflowBranch{{StartAt: "x", States: map[string]fv1.WorkflowBranchState{
				"x": {Type: fv1.WorkflowStateTask, Function: fn, End: true},
			}}},
			Next: next,
		}
	}
	h := newHarness(t, &fv1.WorkflowSpec{
		StartAt: "first",
		States: map[string]fv1.WorkflowState{
			"first":  region("second"),
			"second": region("done"),
			"done":   {Type: fv1.WorkflowStateSucceed},
		},
	})

	s := h.drive(t, h.engine, 10*time.Second)
	require.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal, "the second region must not stall behind the first")
	assert.Equal(t, 2, attempts(h.log(t), "x"), "one attempt of x per region")
	assert.GreaterOrEqual(t, h.callCount("fn-x"), 2, "x is delivered in each region")
}

// TestInvokerLateCompletionAfterForget: a worker still executing when its
// run is forgotten (the terminal reconcile, or CleanupRun after deletion)
// finishes later. Its result is discarded against the terminal or trimmed
// stream, and its markDone must not recreate the run's done set: nothing
// would ever Forget it again.
func TestInvokerLateCompletionAfterForget(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		forget func(t *testing.T, h *harness, e *Engine)
	}{
		{"terminal", func(_ *testing.T, h *harness, e *Engine) { e.invoker.Forget(string(h.run.UID)) }},
		{"deleted", func(t *testing.T, h *harness, e *Engine) {
			require.NoError(t, e.CleanupRun(t.Context(), h.run.Namespace, h.run.Name, h.run.UID))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t, pipelineSpec())
			s := h.drive(t, h.engine, 10*time.Second)
			require.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)

			// A function that blocks until released stands in for a step
			// still executing when the run is forgotten.
			arrived, release := make(chan struct{}), make(chan struct{})
			slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				close(arrived)
				<-release
				_, _ = w.Write([]byte(`{}`))
			}))
			t.Cleanup(slow.Close)
			noWake := func(types.NamespacedName) {}
			invoker := NewInvoker(InvokerOptions{
				Logger: logr.Discard(), Client: slow.Client(), RouterURL: slow.URL,
				EventLog: h.el, KV: h.kv, Wake: noWake, Workers: 1,
			})
			e := NewEngine(EngineOptions{
				Logger: logr.Discard(), EventLog: h.el, Queue: h.q, KV: h.kv,
				Invoker: invoker, Wake: noWake,
			})

			invoker.Dispatch(invocation{
				runKey: types.NamespacedName{Namespace: h.run.Namespace, Name: h.run.Name},
				runUID: string(h.run.UID), stream: streamName(h.run), namespace: h.run.Namespace,
				state: "a", attempt: 1, stateSpec: s.Spec.States["a"], input: []byte(`{}`),
				expectedSeq: 1,
			})
			select {
			case <-arrived:
			case <-time.After(5 * time.Second):
				t.Fatal("the dispatched step never reached the function")
			}
			tc.forget(t, h, e)
			close(release)

			// The pool slot is released only after run (and its markDone) returns.
			require.Eventually(t, func() bool { return len(invoker.sem) == 0 }, 5*time.Second, 10*time.Millisecond)
			invoker.mu.Lock()
			_, leaked := invoker.done[string(h.run.UID)]
			invoker.mu.Unlock()
			assert.False(t, leaked, "a late completion must not recreate a forgotten run's done set")
		})
	}
}

// TestEngineCrashPointResumeThroughRegion resumes a fresh engine mid-region
// and asserts convergence with the join discipline intact.
func TestEngineCrashPointResumeThroughRegion(t *testing.T) {
	t.Parallel()

	for crashAfter := range 5 {
		t.Run(strings.Repeat("i", crashAfter+1), func(t *testing.T) {
			t.Parallel()

			h := newHarness(t, fanSpec())
			ctx := t.Context()
			first := h.engine
			for range crashAfter {
				first.timerPollOnce(ctx)
				_, err := first.Reconcile(ctx, h.run, h.fetch)
				require.NoError(t, err)
				time.Sleep(5 * time.Millisecond)
			}

			s := h.drive(t, h.newEngine(), 10*time.Second)
			require.Equal(t, fv1.WorkflowRunSucceeded, s.Terminal)
			assertInvariants(t, h.log(t), 1)
			assertRegionInvariants(t, h.log(t), 2)
		})
	}
}

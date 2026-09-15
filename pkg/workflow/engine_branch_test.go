// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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
	// The terminal reconcile already called Forget; re-mark to model the
	// window between the result append and terminal (the run is still live).
	h.engine.invoker.markDone(inv.runUID, inv.runUID+"//"+stepKey("a", 1))
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

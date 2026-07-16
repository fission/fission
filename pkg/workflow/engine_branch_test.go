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

	assertInvariants(t, h.log(t), 1)
	assertRegionInvariants(t, h.log(t), 2)
	assert.Equal(t, 1, h.calls["fn-x"])
	assert.Equal(t, 1, h.calls["fn-y"])
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

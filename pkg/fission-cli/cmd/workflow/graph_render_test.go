// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func task(fn, next string) fv1.WorkflowBranchState {
	return fv1.WorkflowBranchState{
		Type:     fv1.WorkflowStateTask,
		Function: &fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: fn},
		Next:     next,
	}
}

func endTask(fn string) fv1.WorkflowBranchState {
	st := task(fn, "")
	st.End = true
	return st
}

// fanSpec: a Parallel region whose branches deliberately reuse a top-level
// state name ("charge") to pin id scoping.
func fanSpec() fv1.WorkflowSpec {
	return fv1.WorkflowSpec{
		StartAt: "screening",
		States: map[string]fv1.WorkflowState{
			"screening": {
				Type: fv1.WorkflowStateParallel,
				Branches: []fv1.WorkflowBranch{
					{StartAt: "fraud", States: map[string]fv1.WorkflowBranchState{"fraud": endTask("fraud-fn")}},
					{StartAt: "stock", States: map[string]fv1.WorkflowBranchState{"stock": endTask("stock-fn")}},
				},
				Next: "charge",
			},
			"charge": {Type: fv1.WorkflowStateTask, End: true},
		},
	}
}

func TestRenderMermaidParallelBranches(t *testing.T) {
	t.Parallel()
	out, _ := renderMermaid(fanSpec(), nil)

	// The region is a composite state with one concurrent region per branch.
	assert.Contains(t, out, "state screening {")
	assert.Contains(t, out, "--\n", "concurrent regions are separated by --")
	assert.Contains(t, out, `state "fraud" as screening__0__fraud`)
	assert.Contains(t, out, `state "stock" as screening__1__stock`)
	assert.Contains(t, out, "[*] --> screening__0__fraud")
	assert.Contains(t, out, "[*] --> screening__1__stock")
	assert.Contains(t, out, "screening__0__fraud --> [*]")
	// The region still wires into the outer flow.
	assert.Contains(t, out, "screening --> charge")
	again, _ := renderMermaid(fanSpec(), nil)
	assert.Equal(t, out, again, "deterministic")
}

func TestRenderMermaidScopesBranchIDs(t *testing.T) {
	t.Parallel()
	spec := fanSpec()
	// A branch state that shares a name with a top-level state must not
	// collide: mermaid ids are global.
	spec.States["screening"].Branches[0].States["charge"] = endTask("branch-charge-fn")
	out, _ := renderMermaid(spec, nil)

	assert.Contains(t, out, `state "charge" as screening__0__charge`)
	assert.Contains(t, out, "charge --> [*]", "the top-level charge keeps its bare id")
	assert.Contains(t, out, "screening__0__charge --> [*]")
}

func TestRenderMermaidSanitizesIDs(t *testing.T) {
	t.Parallel()
	// State names may contain '-' (^[A-Za-z0-9_-]{1,64}$), which mermaid will
	// not take as an id — it must be sanitized, with the real name as label.
	spec := fv1.WorkflowSpec{
		StartAt: "first-attempt",
		States: map[string]fv1.WorkflowState{
			"first-attempt": {Type: fv1.WorkflowStateTask, Next: "grace-period"},
			"grace-period":  {Type: fv1.WorkflowStateWait, Duration: &metav1.Duration{Duration: 15 * time.Second}, Next: "done"},
			"done":          {Type: fv1.WorkflowStateSucceed},
		},
	}
	out, _ := renderMermaid(spec, nil)

	assert.Contains(t, out, `state "first-attempt" as first_attempt`)
	assert.Contains(t, out, `state "grace-period" as grace_period`)
	assert.Contains(t, out, "first_attempt --> grace_period")
	assert.NotContains(t, out, "first-attempt --> ", "a hyphenated id would not parse")
	// A Wait's delay is invisible in the graph shape, so it rides in a note.
	assert.Contains(t, out, "note right of grace_period : Wait 15s")
}

func TestRenderMermaidMapRendersTemplateOnce(t *testing.T) {
	t.Parallel()
	spec := fv1.WorkflowSpec{
		StartAt: "enrich",
		States: map[string]fv1.WorkflowState{
			"enrich": {
				Type:           fv1.WorkflowStateMap,
				ItemsPath:      "$.leads",
				MaxConcurrency: 3,
				Branches: []fv1.WorkflowBranch{
					{StartAt: "one", States: map[string]fv1.WorkflowBranchState{"one": endTask("enrich-fn")}},
				},
				Next: "done",
			},
			"done": {Type: fv1.WorkflowStateSucceed},
		},
	}
	out, _ := renderMermaid(spec, nil)

	assert.Contains(t, out, "state enrich {")
	assert.Contains(t, out, `state "one" as enrich__0__one`)
	assert.NotContains(t, out, "--\n", "a Map's branch is one template, not concurrent regions")
	// Fan-out width is data-driven, so it cannot be drawn — say it instead.
	assert.Contains(t, out, "note right of enrich : Map over $.leads (maxConcurrency 3)")
}

func TestRenderMermaidTypeClasses(t *testing.T) {
	t.Parallel()
	spec := fv1.WorkflowSpec{
		StartAt: "a",
		States: map[string]fv1.WorkflowState{
			"a":    {Type: fv1.WorkflowStateTask, Next: "c"},
			"c":    {Type: fv1.WorkflowStateChoice, Default: "done"},
			"done": {Type: fv1.WorkflowStateSucceed},
		},
	}
	out, _ := renderMermaid(spec, nil)

	assert.Contains(t, out, "classDef wftask")
	assert.Contains(t, out, "classDef wfchoice")
	assert.Contains(t, out, "classDef wfsucceed")
	assert.Contains(t, out, "class a wftask")
	assert.Contains(t, out, "class c wfchoice")
	assert.NotContains(t, out, "classDef wfmap", "unused classes are not emitted")
}

func TestOverlayFromRun(t *testing.T) {
	t.Parallel()
	spec := fanSpec()
	run := &fv1.WorkflowRun{}
	events := []historyEvent{
		{Type: "RunStarted"},
		// Branch events are placed by (region, branch, state).
		{Type: "StepScheduled", State: "fraud", Branch: "0", Region: "screening@1"},
		{Type: "StepSucceeded", State: "fraud", Branch: "0", Region: "screening@1"},
		{Type: "StepScheduled", State: "stock", Branch: "1", Region: "screening@1"},
		{Type: "StepFailed", State: "stock", Branch: "1", Region: "screening@1"},
	}
	overlay := overlayFromRun(spec, events, run)

	assert.Equal(t, statusOK, overlay["screening__0__fraud"])
	assert.Equal(t, statusFailed, overlay["screening__1__stock"])
	_, reached := overlay["charge"]
	assert.False(t, reached, "a state with no events stays unreached")

	// Unreached nodes render grey rather than falling back to a type color.
	out, _ := renderMermaid(spec, overlay)
	assert.Contains(t, out, "classDef unreached")
	assert.Contains(t, out, "class charge unreached")
	assert.Contains(t, out, "class screening__0__fraud ok")
	// The container is structure: never classed, so it cannot compete with the
	// branches that carry the actual status.
	assert.NotContains(t, out, "class screening ")
	assert.NotContains(t, out, "classDef wftask", "a run view colors by status, not by type")
}

func TestOverlayRetrySucceedsWins(t *testing.T) {
	t.Parallel()
	spec := fv1.WorkflowSpec{StartAt: "a", States: map[string]fv1.WorkflowState{"a": {Type: fv1.WorkflowStateTask, End: true}}}
	events := []historyEvent{
		{Type: "StepScheduled", State: "a", Attempt: 1},
		{Type: "StepFailed", State: "a", Attempt: 1},
		{Type: "StepScheduled", State: "a", Attempt: 2},
		{Type: "StepSucceeded", State: "a", Attempt: 2},
	}
	overlay := overlayFromRun(spec, events, &fv1.WorkflowRun{})
	assert.Equal(t, statusOK, overlay["a"], "a step that failed then succeeded on retry ended green")
}

func TestOverlayActiveStatesFromStatus(t *testing.T) {
	t.Parallel()
	spec := fv1.WorkflowSpec{StartAt: "wait", States: map[string]fv1.WorkflowState{
		"wait": {Type: fv1.WorkflowStateWait, End: true},
	}}
	run := &fv1.WorkflowRun{Status: fv1.WorkflowRunStatus{ActiveStates: []string{"wait"}}}
	overlay := overlayFromRun(spec, nil, run)
	assert.Equal(t, statusActive, overlay["wait"], "the engine's own in-flight view is honored")
}

func TestOverlayMapItemsCollapseToTemplate(t *testing.T) {
	t.Parallel()
	spec := fv1.WorkflowSpec{
		StartAt: "enrich",
		States: map[string]fv1.WorkflowState{
			"enrich": {
				Type:     fv1.WorkflowStateMap,
				Branches: []fv1.WorkflowBranch{{StartAt: "one", States: map[string]fv1.WorkflowBranchState{"one": endTask("fn")}}},
				End:      true,
			},
		},
	}
	// Item 3's events must land on the single rendered template node (region 0),
	// not on a node that was never drawn.
	events := []historyEvent{{Type: "StepSucceeded", State: "one", Branch: "3", Region: "enrich@1"}}
	overlay := overlayFromRun(spec, events, &fv1.WorkflowRun{})

	require.Contains(t, overlay, "enrich__0__one")
	assert.Equal(t, statusOK, overlay["enrich__0__one"])
	assert.NotContains(t, overlay, "enrich__3__one")
}

func TestOverlayKeepsRoutingStatesUncolored(t *testing.T) {
	t.Parallel()
	// A Choice is resolved inside the fold and emits no events, so the history
	// cannot say whether the run passed through it — claiming "unreached"
	// would be a lie (the run below routed THROUGH decision to reject).
	spec := fv1.WorkflowSpec{
		StartAt: "validate",
		States: map[string]fv1.WorkflowState{
			"validate": {Type: fv1.WorkflowStateTask, Next: "decision"},
			"decision": {Type: fv1.WorkflowStateChoice, Default: "reject"},
			"reject":   {Type: fv1.WorkflowStateTask, End: true},
			"fulfil":   {Type: fv1.WorkflowStateTask, End: true},
		},
	}
	events := []historyEvent{
		{Type: "StepSucceeded", State: "validate"},
		{Type: "StepSucceeded", State: "reject"},
	}
	out, _ := renderMermaid(spec, overlayFromRun(spec, events, &fv1.WorkflowRun{}))

	assert.Contains(t, out, "class decision wfchoice", "a routing-only state keeps its type color")
	assert.Contains(t, out, "class fulfil unreached", "a Task with no events really was not reached")
	assert.Contains(t, out, "class reject,validate ok")
}

func TestOverlayTimerFiredCompletesWait(t *testing.T) {
	t.Parallel()
	spec := fv1.WorkflowSpec{StartAt: "grace-period", States: map[string]fv1.WorkflowState{
		"grace-period": {Type: fv1.WorkflowStateWait, End: true},
	}}
	// A Wait is never "scheduled" — the timer firing is the whole step.
	events := []historyEvent{{Type: "TimerFired", State: "grace-period"}}
	overlay := overlayFromRun(spec, events, &fv1.WorkflowRun{})
	assert.Equal(t, statusOK, overlay["grace_period"])
}

func TestEventNodeIDIgnoresNonStepEvents(t *testing.T) {
	t.Parallel()
	spec := fanSpec()
	assert.Empty(t, eventNodeID(spec, historyEvent{Type: "RunStarted"}))
	assert.Empty(t, eventNodeID(spec, historyEvent{Type: "BranchesJoined"}))
	// A branch event with no region cannot be placed — better unplaced than
	// colored onto the wrong node.
	assert.Empty(t, eventNodeID(spec, historyEvent{Type: "StepSucceeded", State: "fraud", Branch: "0"}))
}

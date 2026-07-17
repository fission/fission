// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"errors"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type RunsGraphSubCommand struct {
	cmd.CommandActioner
}

// RunsGraph renders the run's state machine with every state colored by what
// this run actually did — the visual form of "where did this run stop".
func RunsGraph(input cli.Input) error {
	return (&RunsGraphSubCommand{}).do(input)
}

func (opts *RunsGraphSubCommand) do(input cli.Input) error {
	events, run, err := fetchHistory(input, &opts.CommandActioner)
	if err != nil {
		return err
	}
	spec, err := opts.runSpec(input, events, run)
	if err != nil {
		return err
	}
	diagram := renderMermaid(*spec, overlayFromRun(*spec, events, run))
	if input.Bool(flagkey.WfOpen) {
		return serveDiagram(input.Context(), diagram, run.Name, runLegend)
	}
	fmt.Println(diagram)
	return nil
}

// runSpec returns the spec this run is executing. The RunStarted event carries
// the snapshot the run is bound to for its whole life, so it — not the live
// Workflow — is what the history must be drawn against: the definition may have
// been edited (a later run would differ) or deleted outright while this run
// kept going. The live object is only a fallback for a run whose RunStarted has
// been trimmed out of history.
func (opts *RunsGraphSubCommand) runSpec(input cli.Input, events []historyEvent, run *fv1.WorkflowRun) (*fv1.WorkflowSpec, error) {
	for _, e := range events {
		if e.Spec != nil {
			return e.Spec, nil
		}
	}
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return nil, fmt.Errorf("error in rendering run graph: %w", err)
	}
	wf, err := opts.Client().FissionClientSet.CoreV1().Workflows(namespace).Get(input.Context(), run.Spec.WorkflowRef, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("this run's spec snapshot is no longer in history, and its workflow %q could not be read: %w",
			run.Spec.WorkflowRef, err)
	}
	if wf.Spec.StartAt == "" || len(wf.Spec.States) == 0 {
		return nil, errors.New("the workflow has no states; nothing to render")
	}
	return &wf.Spec, nil
}

// overlayFromRun derives each node's status from the run's event log, last
// event wins: a step scheduled then succeeded is green, and a step that failed
// an attempt then succeeded on retry ends green, which is what happened.
func overlayFromRun(spec fv1.WorkflowSpec, events []historyEvent, run *fv1.WorkflowRun) map[string]nodeStatus {
	overlay := map[string]nodeStatus{}
	for _, e := range events {
		id := eventNodeID(spec, e)
		if id == "" {
			continue
		}
		switch e.Type {
		case "StepScheduled":
			overlay[id] = statusActive
		case "StepSucceeded":
			overlay[id] = statusOK
		case "StepFailed":
			overlay[id] = statusFailed
		case "TimerFired":
			// A Wait is never "scheduled"; its timer firing is the whole step.
			// A Wait still parked is caught by the ActiveStates pass below.
			overlay[id] = statusOK
		}
	}
	rollUpRegions(spec, overlay)
	// The engine's own view of what is in flight, for states whose last event
	// does not say so (a Wait parked on a timer, say).
	for _, s := range run.Status.ActiveStates {
		overlay[mermaidID(s)] = statusActive
	}
	return overlay
}

// rollUpRegions gives a fan-out state the status of its branches. The composite
// never appears in the log itself — only its branch steps do — so without this
// a region that actually ran would render as never-reached.
func rollUpRegions(spec fv1.WorkflowSpec, overlay map[string]nodeStatus) {
	for name, st := range spec.States {
		if len(st.Branches) == 0 {
			continue
		}
		worst, seen := statusOK, false
		for i := range st.Branches {
			// Only region 0 of a Map is drawn (see eventNodeID).
			if st.Type == fv1.WorkflowStateMap && i > 0 {
				break
			}
			for bn := range st.Branches[i].States {
				s, ok := overlay[branchNodeID(name, i, bn)]
				if !ok {
					continue
				}
				seen = true
				worst = worseStatus(worst, s)
			}
		}
		if seen {
			overlay[mermaidID(name)] = worst
		}
	}
}

// worseStatus ranks failed > active > ok so a region surfaces the branch that
// most needs attention — one failed branch fails the region (fail-fast), and a
// still-running sibling keeps it active.
func worseStatus(a, b nodeStatus) nodeStatus {
	rank := map[nodeStatus]int{statusOK: 0, statusActive: 1, statusFailed: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// eventNodeID maps a history event to the diagram node it colors, or "" for
// events that are not a step (RunStarted, BranchesJoined, terminals).
func eventNodeID(spec fv1.WorkflowSpec, e historyEvent) string {
	if e.State == "" {
		return ""
	}
	if e.Branch == "" {
		return mermaidID(e.State)
	}
	// A branch event. Region is "<fanOutState>@<entrySeq>": the state name
	// alone would be ambiguous, since a loop can re-enter the same fan-out and
	// regions reuse branch keys.
	parent, _, ok := strings.Cut(e.Region, "@")
	if !ok || parent == "" {
		return ""
	}
	branch := e.Branch
	// A Map's branches are per-item instances of ONE template, and the diagram
	// draws that template once (region 0), so every item's events collapse onto
	// it — the node then shows the latest item activity rather than one item's.
	if spec.States[parent].Type == fv1.WorkflowStateMap {
		branch = "0"
	}
	return mermaidID(parent, branch, e.State)
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
)

type DescribeSubCommand struct {
	cmd.CommandActioner
}

// Describe is the one-command answer to "where did this run stop": phase,
// active state, last error, per-state attempts, and duration — from status,
// enriched with the head's history endpoint when reachable.
func Describe(input cli.Input) error {
	return (&DescribeSubCommand{}).do(input)
}

func (opts *DescribeSubCommand) do(input cli.Input) error {
	events, run, err := fetchHistory(input, &opts.CommandActioner)
	if err != nil {
		// Status-only degradation needs the run itself; a missing run is
		// still fatal, but an unreachable head is not.
		if run == nil {
			return err
		}
		console.Warn(fmt.Sprintf("history unavailable (%v); showing status only", err))
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Name:\t%s\n", run.Name)
	fmt.Fprintf(w, "Workflow:\t%s (generation %d)\n", run.Spec.WorkflowRef, run.Spec.WorkflowGeneration)
	fmt.Fprintf(w, "Phase:\t%s\n", runPhase(*run))
	if len(run.Status.ActiveStates) > 0 {
		fmt.Fprintf(w, "Active:\t%s\n", activeStates(*run))
	}
	if run.Status.StartedAt != nil {
		end := time.Now()
		if run.Status.FinishedAt != nil {
			end = run.Status.FinishedAt.Time
		}
		fmt.Fprintf(w, "Started:\t%s\n", run.Status.StartedAt.Format(time.RFC3339))
		fmt.Fprintf(w, "Duration:\t%s\n", end.Sub(run.Status.StartedAt.Time).Round(time.Millisecond))
	}
	if run.Status.Output != nil {
		fmt.Fprintf(w, "Output:\t%s\n", string(run.Status.Output.Raw))
	}
	if run.Status.OutputRef != "" {
		fmt.Fprintf(w, "OutputRef:\t%s (use history --io to dereference)\n", run.Status.OutputRef)
	}

	if len(events) > 0 {
		attempts := map[string]int32{}
		var lastError *historyEvent
		for i, e := range events {
			if e.Type == "StepScheduled" && e.Attempt > attempts[e.State] {
				attempts[e.State] = e.Attempt
			}
			if e.ErrorType != "" {
				lastError = &events[i]
			}
		}
		if lastError != nil {
			fmt.Fprintf(w, "Last error:\t%s (state %s, attempt %d)\n", lastError.ErrorType, orDash(lastError.State), lastError.Attempt)
			if len(lastError.Cause) > 0 {
				fmt.Fprintf(w, "Cause:\t%s\n", string(lastError.Cause))
			}
		}
		fmt.Fprintf(w, "Attempts:\n")
		for state, n := range attempts {
			fmt.Fprintf(w, "  %s:\t%d\n", state, n)
		}
	}
	return w.Flush()
}

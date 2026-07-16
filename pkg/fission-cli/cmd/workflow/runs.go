// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

// notAcceptedGrace is how old a non-terminal run without an Accepted
// condition must be before it is flagged NOT-ACCEPTED (a fresh run may
// simply not have been reconciled yet).
const notAcceptedGrace = 30 * time.Second

type RunsSubCommand struct {
	cmd.CommandActioner
}

func Runs(input cli.Input) error {
	return (&RunsSubCommand{}).do(input)
}

func (opts *RunsSubCommand) do(input cli.Input) error {
	ns, err := opts.ResolveNamespace(input)
	if err != nil {
		return fmt.Errorf("error in listing workflow runs: %w", err)
	}

	runs, err := opts.Client().FissionClientSet.CoreV1().WorkflowRuns(ns).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list workflow runs: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	headers := []string{"NAME", "WORKFLOW", "PHASE", "ACTIVE", "AGE"}
	row := func(r fv1.WorkflowRun) []string {
		return []string{
			r.Name, r.Spec.WorkflowRef, runPhase(r), activeStates(r), util.AgeOf(r.CreationTimestamp),
		}
	}
	wideExtra := []string{"STARTED", "FINISHED"}
	wideRow := func(r fv1.WorkflowRun) []string {
		return []string{timeOrDash(r.Status.StartedAt), timeOrDash(r.Status.FinishedAt)}
	}

	return util.PrintObjects(format, runs.Items, headers, row, wideExtra, wideRow)
}

// runPhase renders the phase, surfacing the no-controller case: a status
// condition cannot exist without a running writer, so the disabled-head
// signal is computed client-side (see fv1.WorkflowRunReasonNoController).
func runPhase(r fv1.WorkflowRun) string {
	phase := string(r.Status.Phase)
	if phase == "" {
		phase = string(fv1.WorkflowRunPending)
	}
	if !r.Status.Phase.Terminal() &&
		meta.FindStatusCondition(r.Status.Conditions, fv1.WorkflowRunConditionAccepted) == nil &&
		time.Since(r.CreationTimestamp.Time) > notAcceptedGrace {
		return phase + " (" + fv1.WorkflowRunReasonNoController + ")"
	}
	return phase
}

func activeStates(r fv1.WorkflowRun) string {
	if len(r.Status.ActiveStates) == 0 {
		return "-"
	}
	return r.Status.ActiveStates[0]
}

func timeOrDash(t *metav1.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.RFC3339)
}

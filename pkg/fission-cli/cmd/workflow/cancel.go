// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/workflow"
)

type CancelSubCommand struct {
	cmd.CommandActioner
}

// Cancel requests cancellation of a run via the cancel annotation (the run
// spec is immutable; annotations are the cancellation channel). In-flight
// invocations drain — no function kill signal exists — and their late
// completions lose the CAS against the terminal event.
func Cancel(input cli.Input) error {
	return (&CancelSubCommand{}).do(input)
}

func (opts *CancelSubCommand) do(input cli.Input) error {
	runName := input.String(flagkey.WfName)
	if runName == "" {
		return errors.New("need a workflow run, use --name")
	}
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error resolving namespace: %w", err)
	}

	// A terminal run cannot be cancelled — say so instead of claiming a
	// cancellation the engine will ignore forever.
	run, err := opts.Client().FissionClientSet.CoreV1().WorkflowRuns(namespace).Get(input.Context(), runName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting workflow run: %w", err)
	}
	if run.Status.Phase.Terminal() {
		fmt.Printf("workflow run '%v' already finished (%s) — nothing to cancel\n", runName, run.Status.Phase)
		return nil
	}

	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:"true"}}}`, workflow.CancelAnnotation)
	_, err = opts.Client().FissionClientSet.CoreV1().WorkflowRuns(namespace).
		Patch(input.Context(), runName, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("error requesting cancellation: %w", err)
	}

	fmt.Printf("workflow run '%v' cancellation requested (in-flight steps drain; no kill signal exists)\n", runName)
	return nil
}

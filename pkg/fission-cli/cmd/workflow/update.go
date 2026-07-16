// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
)

type UpdateSubCommand struct {
	cmd.CommandActioner
}

func Update(input cli.Input) error {
	return (&UpdateSubCommand{}).do(input)
}

func (opts *UpdateSubCommand) do(input cli.Input) error {
	wf, err := loadWorkflow(input)
	if err != nil {
		return err
	}

	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in updating workflow: %w", err)
	}
	if wf.Namespace == "" {
		wf.Namespace = namespace
	}

	if err := wf.Validate(); err != nil {
		return fv1.AggregateValidationErrors("Workflow", err)
	}

	existing, err := opts.Client().FissionClientSet.CoreV1().Workflows(wf.Namespace).Get(input.Context(), wf.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting workflow '%s': %w", wf.Name, err)
	}

	existing.Spec = wf.Spec
	_, err = opts.Client().FissionClientSet.CoreV1().Workflows(wf.Namespace).Update(input.Context(), existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("error updating workflow: %w", err)
	}

	fmt.Printf("workflow '%v' updated\n", wf.Name)
	return nil
}

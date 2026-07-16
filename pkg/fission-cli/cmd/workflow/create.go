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
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type CreateSubCommand struct {
	cmd.CommandActioner
	wf *fv1.Workflow
}

func Create(input cli.Input) error {
	return (&CreateSubCommand{}).do(input)
}

func (opts *CreateSubCommand) do(input cli.Input) error {
	if err := opts.complete(input); err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *CreateSubCommand) complete(input cli.Input) error {
	wf, err := loadWorkflow(input)
	if err != nil {
		return err
	}

	userProvidedNS, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in creating workflow: %w", err)
	}
	if wf.Namespace == "" {
		wf.Namespace = namespace
	}
	if input.Bool(flagkey.SpecSave) || input.Bool(flagkey.SpecDry) {
		wf.Namespace = userProvidedNS
	}

	// Fast local feedback; the admission webhook still gates the API call.
	if err := wf.Validate(); err != nil {
		return fv1.AggregateValidationErrors("Workflow", err)
	}

	opts.wf = wf
	return nil
}

func (opts *CreateSubCommand) run(input cli.Input) error {
	if handled, err := spec.SaveOrDry(input, *opts.wf, fmt.Sprintf("workflow-%v.yaml", opts.wf.Name)); handled {
		return err
	}

	_, err := opts.Client().FissionClientSet.CoreV1().Workflows(opts.wf.Namespace).Create(input.Context(), opts.wf, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("error creating workflow: %w", err)
	}

	fmt.Printf("workflow '%v' created\n", opts.wf.Name)
	return nil
}

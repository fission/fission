// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ListSubCommand struct {
	cmd.CommandActioner
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {
	ns, err := opts.ResolveNamespace(input)
	if err != nil {
		return fmt.Errorf("error in listing workflows: %w", err)
	}

	wfs, err := opts.Client().FissionClientSet.CoreV1().Workflows(ns).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list workflows: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	headers := []string{"NAME", "STARTAT", "STATES", "VALIDATED"}
	row := func(wf fv1.Workflow) []string {
		return []string{
			wf.Name, wf.Spec.StartAt, strconv.Itoa(len(wf.Spec.States)),
			util.ConditionStatus(wf.Status.Conditions, fv1.WorkflowConditionValidated),
		}
	}
	wideExtra := []string{"AGE"}
	wideRow := func(wf fv1.Workflow) []string { return []string{util.AgeOf(wf.CreationTimestamp)} }

	return util.PrintObjects(format, wfs.Items, headers, row, wideExtra, wideRow)
}

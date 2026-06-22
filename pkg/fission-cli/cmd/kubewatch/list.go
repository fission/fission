// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kubewatch

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ListSubCommand struct {
	cmd.CommandActioner
	namespace string
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *ListSubCommand) complete(input cli.Input) (err error) {
	opts.namespace, err = opts.ResolveNamespace(input)
	if err != nil {
		return fmt.Errorf("error listing kubewatchers: %w", err)
	}
	return nil
}

func (opts *ListSubCommand) run(input cli.Input) (err error) {
	ws, err := opts.Client().FissionClientSet.CoreV1().KubernetesWatchTriggers(opts.namespace).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing kubewatchers: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	headers := []string{"NAME", "NAMESPACE", "OBJTYPE", "LABELS", "FUNCTION_NAME", "READY"}
	row := func(wa v1.KubernetesWatchTrigger) []string {
		return []string{
			wa.Name, wa.Spec.Namespace, wa.Spec.Type, fmt.Sprintf("%v", wa.Spec.LabelSelector), wa.Spec.FunctionReference.Name,
			util.ConditionStatus(wa.Status.Conditions, v1.KubernetesWatchTriggerConditionReady),
		}
	}
	wideExtra := []string{"AGE"}
	wideRow := func(wa v1.KubernetesWatchTrigger) []string { return []string{util.AgeOf(wa.CreationTimestamp)} }

	return util.PrintObjects(format, ws.Items, headers, row, wideExtra, wideRow)
}

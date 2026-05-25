/*
Copyright 2019 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
	opts.namespace, err = opts.ResolveNamespace(input, flagkey.NamespaceTrigger)
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

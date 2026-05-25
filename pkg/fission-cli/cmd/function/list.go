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

package function

import (
	"fmt"
	"strings"

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
	namespace, err := opts.ResolveNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return fmt.Errorf("error in listing function : %w", err)
	}

	fns, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing functions: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	headers := []string{"NAME", "ENV", "EXECUTORTYPE", "MINSCALE", "MAXSCALE", "MINCPU", "MAXCPU", "MINMEMORY", "MAXMEMORY", "SECRETS", "CONFIGMAPS", "READY", "NAMESPACE"}
	row := func(f fv1.Function) []string {
		var secretsList, configMapList []string
		for _, secret := range f.Spec.Secrets {
			secretsList = append(secretsList, secret.Name)
		}
		for _, configMap := range f.Spec.ConfigMaps {
			configMapList = append(configMapList, configMap.Name)
		}
		return []string{
			f.Name, f.Spec.Environment.Name,
			string(f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType),
			fmt.Sprintf("%v", f.Spec.InvokeStrategy.ExecutionStrategy.MinScale),
			fmt.Sprintf("%v", f.Spec.InvokeStrategy.ExecutionStrategy.MaxScale),
			f.Spec.Resources.Requests.Cpu().String(),
			f.Spec.Resources.Limits.Cpu().String(),
			f.Spec.Resources.Requests.Memory().String(),
			f.Spec.Resources.Limits.Memory().String(),
			strings.Join(secretsList, ","),
			strings.Join(configMapList, ","),
			util.ConditionStatus(f.Status.Conditions, fv1.FunctionConditionReady),
			f.Namespace,
		}
	}
	wideExtra := []string{"AGE"}
	wideRow := func(f fv1.Function) []string { return []string{util.AgeOf(f.CreationTimestamp)} }

	return util.PrintObjects(format, fns.Items, headers, row, wideExtra, wideRow)
}

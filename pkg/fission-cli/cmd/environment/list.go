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

package environment

import (
	"fmt"

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

func (opts *ListSubCommand) do(input cli.Input) (err error) {
	currentNS, err := opts.ResolveNamespace(input, flagkey.NamespaceEnvironment)
	if err != nil {
		return fmt.Errorf("error listing environments: %w", err)
	}

	response, err := opts.Client().FissionClientSet.CoreV1().Environments(currentNS).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing environments: %w", err)
	}

	// Environment.Status.Conditions is intentionally not surfaced here: the
	// buildermgr never writes Environment status (a write would bump
	// ResourceVersion and break in-flight source-archive builds), so a READY
	// column would always be empty. See pkg/apis/core/v1/conditions.go.
	headers := []string{"NAME", "IMAGE", "BUILDER_IMAGE", "POOLSIZE", "MINCPU", "MAXCPU", "MINMEMORY", "MAXMEMORY", "EXTNET", "GRACETIME", "NAMESPACE"}
	util.PrintItems(headers, response.Items, func(env fv1.Environment) []string {
		return []string{
			env.Name, env.Spec.Runtime.Image, env.Spec.Builder.Image, fmt.Sprintf("%v", env.Spec.Poolsize),
			env.Spec.Resources.Requests.Cpu().String(), env.Spec.Resources.Limits.Cpu().String(),
			env.Spec.Resources.Requests.Memory().String(), env.Spec.Resources.Limits.Memory().String(),
			fmt.Sprintf("%v", env.Spec.AllowAccessToExternalNetwork), fmt.Sprintf("%v", env.Spec.TerminationGracePeriod), env.Namespace,
		}
	})

	return nil
}

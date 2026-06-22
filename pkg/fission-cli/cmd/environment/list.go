// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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
	currentNS, err := opts.ResolveNamespace(input)
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
	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	headers := []string{"NAME", "IMAGE", "BUILDER_IMAGE", "POOLSIZE", "MINCPU", "MAXCPU", "MINMEMORY", "MAXMEMORY", "EXTNET", "GRACETIME", "NAMESPACE"}
	row := func(env fv1.Environment) []string {
		return []string{
			env.Name, env.Spec.Runtime.Image, env.Spec.Builder.Image, fmt.Sprintf("%v", env.Spec.Poolsize),
			env.Spec.Resources.Requests.Cpu().String(), env.Spec.Resources.Limits.Cpu().String(),
			env.Spec.Resources.Requests.Memory().String(), env.Spec.Resources.Limits.Memory().String(),
			fmt.Sprintf("%v", env.Spec.AllowAccessToExternalNetwork), fmt.Sprintf("%v", env.Spec.TerminationGracePeriod), env.Namespace,
		}
	}
	wideExtra := []string{"AGE"}
	wideRow := func(env fv1.Environment) []string { return []string{util.AgeOf(env.CreationTimestamp)} }

	return util.PrintObjects(format, response.Items, headers, row, wideExtra, wideRow)
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package environment

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type GetSubCommand struct {
	cmd.CommandActioner
}

func Get(input cli.Input) error {
	return (&GetSubCommand{}).do(input)
}

func (opts *GetSubCommand) do(input cli.Input) (err error) {

	_, currentNS, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error getting environment: %w", err)
	}

	env, err := opts.Client().FissionClientSet.CoreV1().Environments(currentNS).Get(input.Context(), input.String(flagkey.EnvName), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting environment: %w", err)
	}

	// Historically this was the one get subcommand with no -o json/yaml and no
	// CONDITIONS block; routing through PrintGet closes that gap. The table
	// keeps its original two columns so scripted output stays parseable.
	_, err = util.PrintGet(input, env, func() error {
		util.PrintTable([]string{"NAME", "IMAGE"}, [][]string{{env.Name, env.Spec.Runtime.Image}})
		return nil
	})
	return err
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"fmt"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type DeleteSubCommand struct {
	cmd.CommandActioner
}

func Delete(input cli.Input) error {
	return (&DeleteSubCommand{}).do(input)
}

func (opts *DeleteSubCommand) do(input cli.Input) error {
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in deleting workflow: %w", err)
	}

	name := input.String(flagkey.WfName)
	deleted, err := util.DeleteOne(input, opts.Client().FissionClientSet.CoreV1().Workflows(namespace), name, "workflow")
	if err != nil {
		return err
	}
	if deleted {
		fmt.Printf("workflow '%v' deleted\n", name)
	}
	return nil
}

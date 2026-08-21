// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timetrigger

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

func (opts *DeleteSubCommand) do(input cli.Input) (err error) {

	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in deleting time trigger : %w", err)
	}

	deleted, err := util.DeleteOne(input, opts.Client().FissionClientSet.CoreV1().TimeTriggers(namespace), input.String(flagkey.TtName), "trigger")
	if err != nil {
		return err
	}
	if deleted {
		fmt.Printf("trigger '%v' deleted\n", input.String(flagkey.TtName))
	}
	return nil
}

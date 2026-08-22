// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kubewatch

import (
	"fmt"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type DeleteSubCommand struct {
	cmd.CommandActioner
	name      string
	namespace string
}

func Delete(input cli.Input) error {
	return (&DeleteSubCommand{}).do(input)
}

func (opts *DeleteSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *DeleteSubCommand) complete(input cli.Input) (err error) {
	opts.name = input.String(flagkey.KwName)
	_, opts.namespace, err = opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in deleting kubewatch: %w", err)
	}
	return nil
}

func (opts *DeleteSubCommand) run(input cli.Input) error {
	deleted, err := util.DeleteOne(input, opts.Client().FissionClientSet.CoreV1().KubernetesWatchTriggers(opts.namespace), opts.name, "kubewatch")
	if err != nil {
		return err
	}
	if deleted {
		fmt.Printf("trigger '%v' deleted\n", opts.name)
	}
	return nil
}

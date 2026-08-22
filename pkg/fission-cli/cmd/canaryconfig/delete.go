// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfig

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
	return (&DeleteSubCommand{}).run(input)
}

func (opts *DeleteSubCommand) run(input cli.Input) (err error) {
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in deleting canaryConfig: %w", err)
	}

	deleted, err := util.DeleteOne(input, opts.Client().FissionClientSet.CoreV1().CanaryConfigs(namespace), input.String(flagkey.CanaryName), "canary config")
	if err != nil {
		return err
	}
	if deleted {
		fmt.Printf("canaryconfig '%v.%v' deleted\n", input.String(flagkey.CanaryName), namespace)
	}
	return nil
}

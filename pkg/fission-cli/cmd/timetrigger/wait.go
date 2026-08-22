// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timetrigger

import (
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type WaitSubCommand struct {
	cmd.CommandActioner
}

// Wait blocks until the TimeTrigger reaches the --for condition or the timeout.
func Wait(input cli.Input) error {
	return (&WaitSubCommand{}).do(input)
}

func (opts *WaitSubCommand) do(input cli.Input) error {
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return err
	}
	return util.WaitOn(input, opts.Client().FissionClientSet.CoreV1().TimeTriggers(namespace), "TimeTrigger", flagkey.TtName)
}

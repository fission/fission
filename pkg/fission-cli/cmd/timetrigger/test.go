// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timetrigger

import (
	"fmt"

	"errors"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ShowSubCommand struct {
	cmd.CommandActioner
}

func Show(input cli.Input) error {
	return (&ShowSubCommand{}).do(input)
}

func (opts *ShowSubCommand) do(input cli.Input) error {
	return opts.run(input)
}

func (opts *ShowSubCommand) run(flaginput cli.Input) error {
	round := flaginput.Int(flagkey.TtRound)
	cronSpec := flaginput.String(flagkey.TtCron)

	if len(cronSpec) == 0 {
		return errors.New("need a cron spec like '0 30 * * * *', '*/2 * * * *', '@every 1h30m', or '@hourly'; use --cron")
	}

	t := util.GetServerInfo(flaginput, opts.Client()).ServerTime.CurrentTime.UTC()

	err := getCronNextNActivationTime(cronSpec, t, round)
	if err != nil {
		return fmt.Errorf("error passing cron spec examination: %w", err)
	}

	return nil
}

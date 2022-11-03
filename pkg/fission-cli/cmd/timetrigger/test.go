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

package timetrigger

import (
	"github.com/pkg/errors"

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
		return errors.New("need a cron spec like '0 30 * * * *', '@every 1h30m', or '@hourly'; use --cron")
	}

	t := util.GetServerInfo().ServerTime.CurrentTime.UTC()

	err := getCronNextNActivationTime(cronSpec, t, round)
	if err != nil {
		return errors.Wrap(err, "error passing cron spec examination")
	}

	return nil
}

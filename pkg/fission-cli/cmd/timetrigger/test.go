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

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
)

type ShowSubCommand struct {
	client *client.Client
}

func Show(flags cli.Input) error {
	opts := ShowSubCommand{
		client: cmd.GetServer(flags),
	}
	return opts.do(flags)
}

func (opts *ShowSubCommand) do(flags cli.Input) error {
	return opts.run(flags)
}

func (opts *ShowSubCommand) run(flags cli.Input) error {
	round := flags.Int("round")
	cronSpec := flags.String("cron")
	if len(cronSpec) == 0 {
		return errors.New("need a cron spec like '0 30 * * * *', '@every 1h30m', or '@hourly'; use --cron")
	}

	t, err := getAPITimeInfo(opts.client)
	if err != nil {
		return err
	}

	err = getCronNextNActivationTime(cronSpec, t, round)
	if err != nil {
		return errors.Wrap(err, "error passing cron spec examination")
	}

	return nil
}

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

package replay

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/pkg/errors"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ReplaySubCommand struct {
	client *client.Client
}

func Replay(flags cli.Input) error {
	c, err := util.GetServer(flags)
	if err != nil {
		return err
	}
	opts := ReplaySubCommand{
		client: c,
	}
	return opts.do(flags)
}

func (opts *ReplaySubCommand) do(flags cli.Input) error {
	return opts.run(flags)
}

func (opts *ReplaySubCommand) run(flags cli.Input) error {
	reqUID := flags.String("reqUID")
	if len(reqUID) == 0 {
		return errors.New("Need a reqUID, use --reqUID flag to specify")
	}

	responses, err := opts.client.ReplayByReqUID(reqUID)
	if err != nil {
		return errors.Wrap(err, "error replaying records")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	for _, resp := range responses {
		fmt.Fprintf(w, "%v",
			resp,
		)
	}

	w.Flush()
	return nil
}

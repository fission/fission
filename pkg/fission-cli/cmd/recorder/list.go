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

package recorder

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/pkg/errors"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ListSubCommand struct {
	client *client.Client
}

func List(flags cli.Input) error {
	c, err := util.GetServer(flags)
	if err != nil {
		return err
	}
	opts := ListSubCommand{
		client: c,
	}
	return opts.do(flags)
}

func (opts *ListSubCommand) do(flags cli.Input) error {
	return opts.run(flags)
}

func (opts *ListSubCommand) run(flags cli.Input) error {
	recorders, err := opts.client.RecorderList("default")
	if err != nil {
		return errors.Wrap(err, "error listing recorders")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\n",
		"NAME", "ENABLED", "FUNCTIONS", "TRIGGERS", "RETENTION_POLICY", "EVICTION_POLICY")
	for _, r := range recorders {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\n",
			r.Metadata.Name, r.Spec.Enabled, r.Spec.Function, r.Spec.Triggers, r.Spec.RetentionPolicy, r.Spec.EvictionPolicy)
	}
	w.Flush()
	return nil
}

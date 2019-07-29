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

package environment

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	cmdutils "github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type GetSubCommand struct {
	client *client.Client
}

func Get(flags cli.Input) error {
	opts := GetSubCommand{
		client: cmdutils.GetServer(flags),
	}
	return opts.do(flags)
}

func (opts *GetSubCommand) do(flags cli.Input) error {
	m, err := cmdutils.GetMetadata(flags)
	if err != nil {
		return err
	}

	env, err := opts.client.EnvironmentGet(m)
	util.CheckErr(err, "get environment")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\t%v\n", "NAME", "UID", "IMAGE")
	fmt.Fprintf(w, "%v\t%v\t%v\n",
		env.Metadata.Name, env.Metadata.UID, env.Spec.Runtime.Image)

	w.Flush()
	return nil
}

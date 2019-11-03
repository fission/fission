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

package function

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/pkg/errors"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
)

type GetMetaSubCommand struct {
	client *client.Client
}

func GetMeta(flags cli.Input) error {
	opts := GetMetaSubCommand{
		client: cmd.GetServer(flags),
	}
	return opts.do(flags)
}

func (opts *GetMetaSubCommand) do(flags cli.Input) error {
	m, err := cmd.GetMetadata("name", "fnNamespace", flags)
	if err != nil {
		return err
	}

	fn, err := opts.client.FunctionGet(m)
	if err != nil {
		return errors.Wrap(err, "error getting function")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\n", "NAME", "ENV")
	fmt.Fprintf(w, "%v\t%v\n", fn.Metadata.Name, fn.Spec.Environment.Name)
	w.Flush()

	return nil
}

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

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type GetSubCommand struct {
	cmd.CommandActioner
}

func Get(input cli.Input) error {
	return (&GetSubCommand{}).do(input)
}

func (opts *GetSubCommand) do(input cli.Input) error {
	m := &metav1.ObjectMeta{
		Name:      input.String(flagkey.EnvName),
		Namespace: input.String(flagkey.NamespaceEnvironment),
	}

	env, err := opts.Client().V1().Environment().Get(m)
	if err != nil {
		return errors.Wrap(err, "error getting environment")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\n", "NAME", "IMAGE")
	fmt.Fprintf(w, "%v\t%v\n",
		env.Metadata.Name, env.Spec.Runtime.Image)

	w.Flush()
	return nil
}

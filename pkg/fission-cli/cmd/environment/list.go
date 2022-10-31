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
	"time"

	"github.com/pkg/errors"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

type ListSubCommand struct {
	cmd.CommandActioner
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) (err error) {

	_, currentNS, err := util.GetResourceNamespace(input, flagkey.NamespaceEnvironment)
	if err != nil {
		return errors.Wrap(err, "error creating environment")
	}

	var envs []v1.Environment
	if input.Bool(flagkey.AllNamespaces) {
		// envs, err = opts.Client().DefaultClientset.V1().Environment().List("")

		var timeout time.Duration
		if opts.TimeoutSeconds != nil {
			timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
		}
		result = &v1.EnvironmentList{}
		err = c.client.Get().
			Namespace(c.ns).
			Resource("environments").
			VersionedParams(&opts, scheme.ParameterCodec).
			Timeout(timeout).
			Do(ctx).
			Into(result)

	} else {
		envs, err = opts.Client().DefaultClientset.V1().Environment().List(currentNS)
	}

	if err != nil {
		return errors.Wrap(err, "error listing environments")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "IMAGE", "BUILDER_IMAGE", "POOLSIZE", "MINCPU", "MAXCPU", "MINMEMORY", "MAXMEMORY", "EXTNET", "GRACETIME", "NAMESPACE")
	for _, env := range envs {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
			env.ObjectMeta.Name, env.Spec.Runtime.Image, env.Spec.Builder.Image, env.Spec.Poolsize,
			env.Spec.Resources.Requests.Cpu(), env.Spec.Resources.Limits.Cpu(),
			env.Spec.Resources.Requests.Memory(), env.Spec.Resources.Limits.Memory(),
			env.Spec.AllowAccessToExternalNetwork, env.Spec.TerminationGracePeriod, env.Namespace,
		)
	}
	w.Flush()

	return nil
}

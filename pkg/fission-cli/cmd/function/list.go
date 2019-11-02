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
	"github.com/pkg/errors"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
)

type ListSubCommand struct {
	client *client.Client
}

func List(flags cli.Input) error {
	opts := ListSubCommand{
		client: cmd.GetServer(flags),
	}
	return opts.do(flags)
}

func (opts *ListSubCommand) do(flags cli.Input) error {
	ns := flags.String("fnNamespace")

	fns, err := opts.client.FunctionList(ns)
	if err != nil {
		return errors.Wrap(err, "error listing functions")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "ENV", "EXECUTORTYPE", "MINSCALE", "MAXSCALE", "MINCPU", "MAXCPU", "MINMEMORY", "MAXMEMORY", "TARGETCPU", "SECRETS", "CONFIGMAPS")
	for _, f := range fns {
		secrets := f.Spec.Secrets
		configMaps := f.Spec.ConfigMaps
		var secretsList, configMapList []string
		for _, secret := range secrets {
			secretsList = append(secretsList, secret.Name)
		}
		for _, configMap := range configMaps {
			configMapList = append(configMapList, configMap.Name)
		}

		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
			f.Metadata.Name, f.Spec.Environment.Name,
			f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType,
			f.Spec.InvokeStrategy.ExecutionStrategy.MinScale,
			f.Spec.InvokeStrategy.ExecutionStrategy.MaxScale,
			f.Spec.Resources.Requests.Cpu().String(),
			f.Spec.Resources.Limits.Cpu().String(),
			f.Spec.Resources.Requests.Memory().String(),
			f.Spec.Resources.Limits.Memory().String(),
			f.Spec.InvokeStrategy.ExecutionStrategy.TargetCPUPercent,
			strings.Join(secretsList, ","),
			strings.Join(configMapList, ","))
	}
	w.Flush()

	return nil
}

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
	"strings"
	"text/tabwriter"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

const (
	notApplicable = "NA"
)

type ListSubCommand struct {
	cmd.CommandActioner
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {
	ns := input.String(flagkey.NamespaceFunction)

	fns, err := opts.Client().V1().Function().List(ns)
	if err != nil {
		return errors.Wrap(err, "error listing functions")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "ENV", "EXECUTORTYPE", "MINSCALE", "MAXSCALE", "MINCPU", "MAXCPU", "MINMEMORY", "MAXMEMORY", "TARGETCPU", "SECRETS", "CONFIGMAPS", "CONCURRENCY", "ENV_POOLSIZE")
	for _, f := range fns {
		secretsList, configMapList := prepareScrentsAndConfigs(f)
		if f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == "poolmgr" {
			m := &metav1.ObjectMeta{
				Name:      f.Spec.Environment.Name,
				Namespace: f.Spec.Environment.Namespace,
			}
			env, err := opts.Client().V1().Environment().Get(m)
			if err != nil {
				return errors.Wrap(err, "error getting environment for fuction")
			}
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				f.ObjectMeta.Name, f.Spec.Environment.Name,
				f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType,
				f.Spec.InvokeStrategy.ExecutionStrategy.MinScale,
				f.Spec.InvokeStrategy.ExecutionStrategy.MaxScale,
				notApplicable,
				notApplicable,
				notApplicable,
				notApplicable,
				notApplicable,
				strings.Join(secretsList, ","),
				strings.Join(configMapList, ","),
				f.Spec.Concurrency, env.Spec.Poolsize)
		} else {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				f.ObjectMeta.Name, f.Spec.Environment.Name,
				f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType,
				f.Spec.InvokeStrategy.ExecutionStrategy.MinScale,
				f.Spec.InvokeStrategy.ExecutionStrategy.MaxScale,
				f.Spec.Resources.Requests.Cpu().String(),
				f.Spec.Resources.Limits.Cpu().String(),
				f.Spec.Resources.Requests.Memory().String(),
				f.Spec.Resources.Limits.Memory().String(),
				f.Spec.InvokeStrategy.ExecutionStrategy.TargetCPUPercent,
				strings.Join(secretsList, ","),
				strings.Join(configMapList, ","),
				f.Spec.Concurrency, notApplicable)
		}

	}
	w.Flush()

	return nil
}

func prepareScrentsAndConfigs(f v1.Function) ([]string, []string) {
	secrets := f.Spec.Secrets
	configMaps := f.Spec.ConfigMaps
	var secretsList, configMapList []string
	for _, secret := range secrets {
		secretsList = append(secretsList, secret.Name)
	}
	for _, configMap := range configMaps {
		configMapList = append(configMapList, configMap.Name)
	}
	return secretsList, configMapList
}

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

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ListSubCommand struct {
	cmd.CommandActioner
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {
	_, namespace, err := util.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return errors.Wrap(err, "error in listing function ")
	}

	var fns *v1.FunctionList
	if input.Bool(flagkey.AllNamespaces) {
		fns, err = opts.Client().FissionClientSet.CoreV1().Functions(metav1.NamespaceAll).List(input.Context(), metav1.ListOptions{})
	} else {
		fns, err = opts.Client().FissionClientSet.CoreV1().Functions(namespace).List(input.Context(), metav1.ListOptions{})
	}
	if err != nil {
		return errors.Wrap(err, "error listing functions")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "ENV", "EXECUTORTYPE", "MINSCALE", "MAXSCALE", "MINCPU", "MAXCPU", "MINMEMORY", "MAXMEMORY", "SECRETS", "CONFIGMAPS", "NAMESPACE")
	for _, f := range fns.Items {
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
			f.ObjectMeta.Name, f.Spec.Environment.Name,
			f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType,
			f.Spec.InvokeStrategy.ExecutionStrategy.MinScale,
			f.Spec.InvokeStrategy.ExecutionStrategy.MaxScale,
			f.Spec.Resources.Requests.Cpu().String(),
			f.Spec.Resources.Limits.Cpu().String(),
			f.Spec.Resources.Requests.Memory().String(),
			f.Spec.Resources.Limits.Memory().String(),
			strings.Join(secretsList, ","),
			strings.Join(configMapList, ","),
			f.ObjectMeta.Namespace)
	}
	w.Flush()

	return nil
}

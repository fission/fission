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

package canaryconfig

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ListSubCommand struct {
	cmd.CommandActioner
	namespace string
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *ListSubCommand) complete(input cli.Input) (err error) {
	_, opts.namespace, err = util.GetResourceNamespace(input, flagkey.NamespaceCanary)
	if err != nil {
		return errors.Wrap(err, "error in listing canary config ")
	}
	return nil
}

func (opts *ListSubCommand) run(input cli.Input) (err error) {

	var canaryCfgs *v1.CanaryConfigList
	if input.Bool(flagkey.AllNamespaces) {
		canaryCfgs, err = opts.Client().FissionClientSet.CoreV1().CanaryConfigs(metav1.NamespaceAll).List(input.Context(), metav1.ListOptions{})
	} else {
		canaryCfgs, err = opts.Client().FissionClientSet.CoreV1().CanaryConfigs(opts.namespace).List(input.Context(), metav1.ListOptions{})
	}
	if err != nil {
		return errors.Wrap(err, "error listing canary config")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "TRIGGER", "FUNCTION-N", "FUNCTION-N-1", "WEIGHT-INCREMENT", "INTERVAL", "FAILURE-THRESHOLD", "FAILURE-TYPE", "STATUS")
	for _, canaryCfg := range canaryCfgs.Items {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
			canaryCfg.ObjectMeta.Name, canaryCfg.Spec.Trigger, canaryCfg.Spec.NewFunction, canaryCfg.Spec.OldFunction, canaryCfg.Spec.WeightIncrement, canaryCfg.Spec.WeightIncrementDuration,
			canaryCfg.Spec.FailureThreshold, canaryCfg.Spec.FailureType, canaryCfg.Status.Status)
	}

	w.Flush()
	return nil
}

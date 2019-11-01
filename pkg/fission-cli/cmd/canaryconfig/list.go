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

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ListSubCommand struct {
	client    *client.Client
	namespace string
}

func List(flags cli.Input) error {
	opts := ListSubCommand{
		client: cmd.GetServer(flags),
	}
	return opts.do(flags)
}

func (opts *ListSubCommand) do(flags cli.Input) error {
	err := opts.complete(flags)
	if err != nil {
		return err
	}
	return opts.run(flags)
}

func (opts *ListSubCommand) complete(flags cli.Input) error {
	opts.namespace = flags.String("canaryNamespace")
	return nil
}

func (opts *ListSubCommand) run(flags cli.Input) error {
	canaryCfgs, err := opts.client.CanaryConfigList(opts.namespace)
	util.CheckErr(err, "list canary config")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "TRIGGER", "FUNCTION-N", "FUNCTION-N-1", "WEIGHT-INCREMENT", "INTERVAL", "FAILURE-THRESHOLD", "FAILURE-TYPE", "STATUS")
	for _, canaryCfg := range canaryCfgs {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
			canaryCfg.Metadata.Name, canaryCfg.Spec.Trigger, canaryCfg.Spec.NewFunction, canaryCfg.Spec.OldFunction, canaryCfg.Spec.WeightIncrement, canaryCfg.Spec.WeightIncrementDuration,
			canaryCfg.Spec.FailureThreshold, canaryCfg.Spec.FailureType, canaryCfg.Status.Status)
	}

	w.Flush()
	return nil
}

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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type GetSubCommand struct {
	cmd.CommandActioner
}

func Get(input cli.Input) error {
	return (&GetSubCommand{}).run(input)
}

func (opts *GetSubCommand) run(input cli.Input) (err error) {

	_, namespace, err := opts.GetResourceNamespace(input, flagkey.NamespaceCanary)
	if err != nil {
		return fmt.Errorf("error getting canary config: %w", err)
	}

	canaryCfg, err := opts.Client().FissionClientSet.CoreV1().CanaryConfigs(namespace).Get(input.Context(), input.String(flagkey.CanaryName), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting canary config: %w", err)
	}

	headers := []string{"NAME", "TRIGGER", "FUNCTION-N", "FUNCTION-N-1", "WEIGHT-INCREMENT", "INTERVAL", "FAILURE-THRESHOLD", "FAILURE-TYPE", "STATUS"}
	rows := [][]string{{
		canaryCfg.Name, canaryCfg.Spec.Trigger, canaryCfg.Spec.NewFunction, canaryCfg.Spec.OldFunction,
		fmt.Sprintf("%v", canaryCfg.Spec.WeightIncrement), fmt.Sprintf("%v", canaryCfg.Spec.WeightIncrementDuration),
		fmt.Sprintf("%v", canaryCfg.Spec.FailureThreshold), string(canaryCfg.Spec.FailureType), canaryCfg.Status.Status,
	}}
	util.PrintTable(headers, rows)
	util.PrintConditions(canaryCfg.Status.Conditions)

	return nil
}

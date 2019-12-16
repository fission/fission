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
	"time"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type UpdateSubCommand struct {
	cmd.CommandActioner
	canary *fv1.CanaryConfig
}

func Update(input cli.Input) error {
	return (&UpdateSubCommand{}).do(input)
}

func (opts *UpdateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *UpdateSubCommand) complete(input cli.Input) error {
	// get the current config
	name := input.String(flagkey.CanaryName)
	ns := input.String(flagkey.NamespaceCanary)
	incrementStep := input.Int(flagkey.CanaryWeightIncrement)
	failureThreshold := input.Int(flagkey.CanaryFailureThreshold)
	incrementInterval := input.String(flagkey.CanaryIncrementInterval)

	// check for time parsing
	_, err := time.ParseDuration(incrementInterval)
	if err != nil {
		return errors.Wrap(err, "error parsing time duration")
	}

	canaryCfg, err := opts.Client().V1().CanaryConfig().Get(&metav1.ObjectMeta{
		Name:      name,
		Namespace: ns,
	})
	if err != nil {
		return errors.Wrap(err, "error getting canary config")
	}

	var updateNeeded bool

	if incrementStep != canaryCfg.Spec.WeightIncrement {
		canaryCfg.Spec.WeightIncrement = incrementStep
	}

	if failureThreshold != canaryCfg.Spec.FailureThreshold {
		canaryCfg.Spec.FailureThreshold = failureThreshold
	}

	if incrementInterval != canaryCfg.Spec.WeightIncrementDuration {
		canaryCfg.Spec.WeightIncrementDuration = incrementInterval
	}

	if updateNeeded {
		canaryCfg.Status.Status = fv1.CanaryConfigStatusPending
	}

	opts.canary = canaryCfg

	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	_, err := opts.Client().V1().CanaryConfig().Update(opts.canary)
	if err != nil {
		return errors.Wrap(err, "error updating canary config")
	}
	fmt.Printf("canary config '%v' updated\n", opts.canary.Metadata.Name)
	return nil
}

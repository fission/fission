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

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type UpdateSubCommand struct {
	client *client.Client
	canary *fv1.CanaryConfig
}

func Update(flags cli.Input) error {
	c, err := util.GetServer(flags)
	if err != nil {
		return err
	}
	opts := UpdateSubCommand{
		client: c,
	}
	return opts.do(flags)
}

func (opts *UpdateSubCommand) do(flags cli.Input) error {
	err := opts.complete(flags)
	if err != nil {
		return err
	}
	return opts.run(flags)
}

func (opts *UpdateSubCommand) complete(flags cli.Input) error {
	// get the current config
	m, err := util.GetMetadata("name", "canaryNamespace", flags)
	if err != nil {
		return err
	}

	incrementStep := flags.Int("increment-step")
	failureThreshold := flags.Int("failure-threshold")
	incrementInterval := flags.String("increment-interval")

	// check for time parsing
	_, err = time.ParseDuration(incrementInterval)
	if err != nil {
		return errors.Wrap(err, "error parsing time duration")
	}

	canaryCfg, err := opts.client.CanaryConfigGet(m)
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

func (opts *UpdateSubCommand) run(flags cli.Input) error {
	_, err := opts.client.CanaryConfigUpdate(opts.canary)
	if err != nil {
		return errors.Wrap(err, "error updating canary config")
	}
	fmt.Printf("canary config '%v' updated\n", opts.canary.Metadata.Name)
	return nil
}

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
	"github.com/fission/fission/pkg/types"
)

type CreateSubCommand struct {
	client *client.Client
	canary *fv1.CanaryConfig
}

func Create(flags cli.Input) error {
	c, err := util.GetServer(flags)
	if err != nil {
		return err
	}
	opts := CreateSubCommand{
		client: c,
	}
	return opts.do(flags)
}

func (opts *CreateSubCommand) do(flags cli.Input) error {
	err := opts.complete(flags)
	if err != nil {
		return err
	}
	return opts.run(flags)
}

func (opts *CreateSubCommand) complete(flags cli.Input) error {
	// canary configs can be created for functions in the same namespace

	trigger := flags.String("httptrigger")
	newFunc := flags.String("newfunction")
	oldFunc := flags.String("oldfunction")
	ns := flags.String("fnNamespace")
	incrementStep := flags.Int("increment-step")
	failureThreshold := flags.Int("failure-threshold")
	incrementInterval := flags.String("increment-interval")

	// check for time parsing
	_, err := time.ParseDuration(incrementInterval)
	if err != nil {
		return errors.Wrap(err, "error parsing time duration")
	}

	// check that the trigger exists in the same namespace.
	m, err := util.GetMetadata("httptrigger", "fnNamespace", flags)
	if err != nil {
		return errors.Wrap(err, "error finding http trigger in given namespace")
	}

	htTrigger, err := opts.client.HTTPTriggerGet(m)
	if err != nil {
		return errors.Wrap(err, "error finding trigger referenced in the canary config")
	}

	// check that the trigger has function reference type function weights
	if htTrigger.Spec.FunctionReference.Type != types.FunctionReferenceTypeFunctionWeights {
		return errors.New("canary config cannot be created for http triggers that do not reference functions by weights")
	}

	// check that the trigger references same functions in the function weights
	_, ok := htTrigger.Spec.FunctionReference.FunctionWeights[newFunc]
	if !ok {
		return fmt.Errorf("HTTP Trigger doesn't reference the function %s in Canary Config", newFunc)
	}

	_, ok = htTrigger.Spec.FunctionReference.FunctionWeights[oldFunc]
	if !ok {
		return fmt.Errorf("HTTP Trigger doesn't reference the function %s in Canary Config", oldFunc)
	}

	// check that the functions exist in the same namespace
	fnList := []string{newFunc, oldFunc}
	err = util.CheckFunctionExistence(opts.client, fnList, ns)
	if err != nil {
		return errors.Wrap(err, "error checking functions existence")
	}

	canaryMetadata, err := util.GetMetadata("name", "fnNamespace", flags)
	if err != nil {
		return err
	}

	// finally create canaryCfg in the same namespace as the functions referenced
	opts.canary = &fv1.CanaryConfig{
		Metadata: *canaryMetadata,
		Spec: fv1.CanaryConfigSpec{
			Trigger:                 trigger,
			NewFunction:             newFunc,
			OldFunction:             oldFunc,
			WeightIncrement:         incrementStep,
			WeightIncrementDuration: incrementInterval,
			FailureThreshold:        failureThreshold,
			FailureType:             fv1.FailureTypeStatusCode,
		},
		Status: fv1.CanaryConfigStatus{
			Status: fv1.CanaryConfigStatusPending,
		},
	}

	return nil
}

func (opts *CreateSubCommand) run(flags cli.Input) error {
	_, err := opts.client.CanaryConfigCreate(opts.canary)
	if err != nil {
		return errors.Wrap(err, "error creating canary config")
	}

	fmt.Printf("canary config '%v' created\n", opts.canary.Metadata.Name)
	return nil
}

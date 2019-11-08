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
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/types"
)

type CreateSubCommand struct {
	client *client.Client
	canary *fv1.CanaryConfig
}

func Create(input cli.Input) error {
	c, err := util.GetServer(input)
	if err != nil {
		return err
	}
	opts := CreateSubCommand{
		client: c,
	}
	return opts.do(input)
}

func (opts *CreateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *CreateSubCommand) complete(input cli.Input) error {
	// canary configs can be created for functions in the same namespace

	name := input.String(flagkey.CanaryName)
	ht := input.String(flagkey.CanaryHTTPTriggerName)
	newFunc := input.String(flagkey.CanaryNewFunc)
	oldFunc := input.String(flagkey.CanaryOldFunc)
	fnNs := input.String(flagkey.NamespaceFunction)
	incrementStep := input.Int(flagkey.CanaryWeightIncrement)
	failureThreshold := input.Int(flagkey.CanaryFailureThreshold)
	incrementInterval := input.String(flagkey.CanaryIncrementInterval)

	// check for time parsing
	_, err := time.ParseDuration(incrementInterval)
	if err != nil {
		return errors.Wrap(err, "error parsing time duration")
	}

	// check that the trigger exists in the same namespace.
	htTrigger, err := opts.client.HTTPTriggerGet(&metav1.ObjectMeta{
		Name:      ht,
		Namespace: fnNs,
	})
	if err != nil {
		return errors.Wrap(err, "error finding http trigger referenced in the canary config")
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
	err = util.CheckFunctionExistence(opts.client, fnList, fnNs)
	if err != nil {
		return errors.Wrap(err, "error checking functions existence")
	}

	// finally create canaryCfg in the same namespace as the functions referenced
	opts.canary = &fv1.CanaryConfig{
		Metadata: metav1.ObjectMeta{
			Name:      name,
			Namespace: fnNs,
		},
		Spec: fv1.CanaryConfigSpec{
			Trigger:                 ht,
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

func (opts *CreateSubCommand) run(input cli.Input) error {
	_, err := opts.client.CanaryConfigCreate(opts.canary)
	if err != nil {
		return errors.Wrap(err, "error creating canary config")
	}

	fmt.Printf("canary config '%v' created\n", opts.canary.Metadata.Name)
	return nil
}

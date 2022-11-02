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

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type CreateSubCommand struct {
	cmd.CommandActioner
	canary *fv1.CanaryConfig
}

func Create(input cli.Input) error {
	return (&CreateSubCommand{}).do(input)
}

func (opts *CreateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *CreateSubCommand) complete(input cli.Input) (err error) {
	// canary configs can be created for functions in the same namespace

	name := input.String(flagkey.CanaryName)
	ht := input.String(flagkey.CanaryHTTPTriggerName)
	newFunc := input.String(flagkey.CanaryNewFunc)
	oldFunc := input.String(flagkey.CanaryOldFunc)
	_, fnNs, err := util.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return errors.Wrap(err, "error in creating canaryconfig")
	}
	incrementStep := input.Int(flagkey.CanaryWeightIncrement)
	failureThreshold := input.Int(flagkey.CanaryFailureThreshold)
	incrementInterval := input.String(flagkey.CanaryIncrementInterval)

	// check for time parsing
	_, err = time.ParseDuration(incrementInterval)
	if err != nil {
		return errors.Wrap(err, "error parsing time duration")
	}

	// check that the trigger exists in the same namespace.
	htTrigger, err := opts.Client().FissionClientSet.CoreV1().HTTPTriggers(fnNs).Get(input.Context(), ht, metav1.GetOptions{})
	if err != nil {
		return errors.Wrap(err, "error finding http trigger referenced in the canary config")
	}

	// check that the trigger has function reference type function weights
	if htTrigger.Spec.FunctionReference.Type != fv1.FunctionReferenceTypeFunctionWeights {
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
	err = util.CheckFunctionExistence(input.Context(), opts.Client(), fnList, fnNs)
	if err != nil {
		return errors.Wrap(err, "error checking functions existence")
	}

	// finally create canaryCfg in the same namespace as the functions referenced
	opts.canary = &fv1.CanaryConfig{
		ObjectMeta: metav1.ObjectMeta{
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
	_, err := opts.Client().FissionClientSet.CoreV1().CanaryConfigs(opts.canary.ObjectMeta.Namespace).Create(input.Context(), opts.canary, metav1.CreateOptions{})
	if err != nil {
		return errors.Wrap(err, "error creating canary config")
	}

	fmt.Printf("canary config '%v' created\n", opts.canary.ObjectMeta.Name)
	return nil
}

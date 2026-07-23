// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfig

import (
	"errors"
	"fmt"
	"time"

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

	_, fnNs, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in creating canaryconfig: %w", err)
	}
	incrementStep := input.Int(flagkey.CanaryWeightIncrement)
	failureThreshold := input.Int(flagkey.CanaryFailureThreshold)
	incrementInterval := input.String(flagkey.CanaryIncrementInterval)

	// check for time parsing
	_, err = time.ParseDuration(incrementInterval)
	if err != nil {
		return fmt.Errorf("error parsing time duration: %w", err)
	}

	// check that the trigger exists in the same namespace.
	htTrigger, err := opts.Client().FissionClientSet.CoreV1().HTTPTriggers(fnNs).Get(input.Context(), ht, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error finding http trigger referenced in the canary config: %w", err)
	}

	// ALIAS MODE (RFC-0025): an HTTPTrigger referencing a FunctionAlias drives
	// the rollout by stepping FunctionAlias.Weight instead of
	// HTTPTrigger.FunctionWeights (see pkg/canaryconfigmgr's alias-mode
	// branch). --newfn/--oldfn then name FunctionVersions of the alias's
	// function, not functions — the weights-map checks below don't apply.
	if htTrigger.Spec.FunctionReference.Type == fv1.FunctionReferenceTypeFunctionName && htTrigger.Spec.FunctionReference.Alias != "" {
		if err := opts.validateAliasTargets(input, fnNs, htTrigger.Spec.FunctionReference.Alias, newFunc, oldFunc); err != nil {
			return err
		}
	} else {
		// check that the trigger has function reference type function weights
		if htTrigger.Spec.FunctionReference.Type != fv1.FunctionReferenceTypeFunctionWeights {
			return errors.New("canary config cannot be created for http triggers that do not reference functions by weights or a function alias")
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
			return fmt.Errorf("error checking functions existence: %w", err)
		}
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

// validateAliasTargets checks that newFunc/oldFunc each name a
// FunctionVersion belonging to aliasName's function — the alias-mode
// equivalent of the FunctionWeights-map membership checks in complete().
// Mirrors pkg/canaryconfigmgr's validateAliasRollout, run client-side at
// create time so a typo'd --newfn/--oldfn is caught immediately instead of
// silently failing the rollout at the first reconcile.
func (opts *CreateSubCommand) validateAliasTargets(input cli.Input, ns, aliasName, newFunc, oldFunc string) error {
	alias, err := opts.Client().FissionClientSet.CoreV1().FunctionAliases(ns).Get(input.Context(), aliasName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error finding function alias '%s' referenced by http trigger: %w", aliasName, err)
	}

	for _, versionName := range []string{newFunc, oldFunc} {
		v, err := opts.Client().FissionClientSet.CoreV1().FunctionVersions(ns).Get(input.Context(), versionName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("error finding function version '%s' referenced in canary config: %w", versionName, err)
		}
		if v.Spec.FunctionName != alias.Spec.FunctionName {
			return fmt.Errorf("function version '%s' belongs to function '%s', not function alias '%s's function '%s'",
				versionName, v.Spec.FunctionName, aliasName, alias.Spec.FunctionName)
		}
	}

	return nil
}

func (opts *CreateSubCommand) run(input cli.Input) error {
	_, err := opts.Client().FissionClientSet.CoreV1().CanaryConfigs(opts.canary.ObjectMeta.Namespace).Create(input.Context(), opts.canary, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("error creating canary config: %w", err)
	}

	fmt.Printf("canary config '%s' created\n", opts.canary.Name)
	return nil
}

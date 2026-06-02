// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfig

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
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

func (opts *UpdateSubCommand) complete(input cli.Input) (err error) {
	// get the current config
	_, ns, err := opts.GetResourceNamespace(input, flagkey.NamespaceCanary)
	if err != nil {
		return fmt.Errorf("error updating canary config: %w", err)
	}
	canaryCfg, err := opts.Client().FissionClientSet.CoreV1().CanaryConfigs(ns).Get(input.Context(), input.String(flagkey.CanaryName), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting canary config: %w", err)
	}

	// Only flags the user actually set should mutate the resource; these flags
	// have non-zero defaults, so comparing the raw value would clobber stored
	// fields (and reset status) on an update that never mentioned them.
	var updateNeeded bool

	if input.IsSet(flagkey.CanaryWeightIncrement) {
		if incrementStep := input.Int(flagkey.CanaryWeightIncrement); incrementStep != canaryCfg.Spec.WeightIncrement {
			canaryCfg.Spec.WeightIncrement = incrementStep
			updateNeeded = true
		}
	}

	if input.IsSet(flagkey.CanaryFailureThreshold) {
		if failureThreshold := input.Int(flagkey.CanaryFailureThreshold); failureThreshold != canaryCfg.Spec.FailureThreshold {
			canaryCfg.Spec.FailureThreshold = failureThreshold
			updateNeeded = true
		}
	}

	if input.IsSet(flagkey.CanaryIncrementInterval) {
		incrementInterval := input.String(flagkey.CanaryIncrementInterval)
		if _, err := time.ParseDuration(incrementInterval); err != nil {
			return fmt.Errorf("error parsing time duration: %w", err)
		}
		if incrementInterval != canaryCfg.Spec.WeightIncrementDuration {
			canaryCfg.Spec.WeightIncrementDuration = incrementInterval
			updateNeeded = true
		}
	}

	// When any spec field changed, re-arm the rollout by resetting the status to
	// pending so the canary controller re-evaluates from the start.
	if updateNeeded {
		canaryCfg.Status.Status = fv1.CanaryConfigStatusPending
	}

	opts.canary = canaryCfg

	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	_, err := opts.Client().FissionClientSet.CoreV1().CanaryConfigs(opts.canary.ObjectMeta.Namespace).Update(input.Context(), opts.canary, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("error updating canary config: %w", err)
	}
	fmt.Printf("canary config '%v' updated\n", opts.canary.Name)
	return nil
}

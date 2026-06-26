// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfig

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type UpdateSubCommand struct {
	cmd.CommandActioner
	canary *fv1.CanaryConfig
	// rearm is set when a spec field changed, so run() resets the rollout status
	// to pending (via UpdateStatus) after applying the spec.
	rearm bool
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
	_, ns, err := opts.GetResourceNamespace(input)
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

	// When any spec field changed, re-arm the rollout so the canary controller
	// re-evaluates from the start. The reset is applied in run() via UpdateStatus
	// (CanaryConfig has a /status subresource, so a plain Update can't carry it).
	opts.rearm = updateNeeded
	opts.canary = canaryCfg

	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	canaries := opts.Client().FissionClientSet.CoreV1().CanaryConfigs(opts.canary.Namespace)
	_, err := util.UpdateOnConflict(input.Context(), canaries, opts.canary.Name,
		func(cur *fv1.CanaryConfig) { cur.Spec = opts.canary.Spec })
	if err != nil {
		return fmt.Errorf("error updating canary config: %w", err)
	}

	// Re-arm the rollout when a spec field changed: CanaryConfig has a /status
	// subresource, so the spec Update above ignores .status — the reset must go
	// through UpdateStatus (also conflict-retried).
	if opts.rearm {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cur, gerr := canaries.Get(input.Context(), opts.canary.Name, metav1.GetOptions{})
			if gerr != nil {
				return gerr
			}
			cur.Status.Status = fv1.CanaryConfigStatusPending
			_, uerr := canaries.UpdateStatus(input.Context(), cur, metav1.UpdateOptions{})
			return uerr
		}); err != nil {
			return fmt.Errorf("error re-arming canary config rollout: %w", err)
		}
	}
	fmt.Printf("canary config '%v' updated\n", opts.canary.Name)
	return nil
}

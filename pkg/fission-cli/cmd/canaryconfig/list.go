// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfig

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ListSubCommand struct {
	cmd.CommandActioner
	namespace string
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *ListSubCommand) complete(input cli.Input) (err error) {
	opts.namespace, err = opts.ResolveNamespace(input, flagkey.NamespaceCanary)
	if err != nil {
		return fmt.Errorf("error in listing canary config: %w", err)
	}
	return nil
}

func (opts *ListSubCommand) run(input cli.Input) (err error) {
	canaryCfgs, err := opts.Client().FissionClientSet.CoreV1().CanaryConfigs(opts.namespace).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing canary config: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	headers := []string{"NAME", "TRIGGER", "FUNCTION-N", "FUNCTION-N-1", "WEIGHT-INCREMENT", "INTERVAL", "FAILURE-THRESHOLD", "FAILURE-TYPE", "STATUS", "READY"}
	row := func(canaryCfg fv1.CanaryConfig) []string {
		return []string{
			canaryCfg.Name, canaryCfg.Spec.Trigger, canaryCfg.Spec.NewFunction, canaryCfg.Spec.OldFunction,
			fmt.Sprintf("%v", canaryCfg.Spec.WeightIncrement), fmt.Sprintf("%v", canaryCfg.Spec.WeightIncrementDuration),
			fmt.Sprintf("%v", canaryCfg.Spec.FailureThreshold), string(canaryCfg.Spec.FailureType), canaryCfg.Status.Status,
			util.ConditionStatus(canaryCfg.Status.Conditions, fv1.CanaryConfigConditionReady),
		}
	}
	wideExtra := []string{"AGE"}
	wideRow := func(canaryCfg fv1.CanaryConfig) []string { return []string{util.AgeOf(canaryCfg.CreationTimestamp)} }

	return util.PrintObjects(format, canaryCfgs.Items, headers, row, wideExtra, wideRow)
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timetrigger

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
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) (err error) {
	ttNs, err := opts.ResolveNamespace(input, flagkey.NamespaceTrigger)
	if err != nil {
		return fmt.Errorf("error in listing time triggers: %w", err)
	}

	tts, err := opts.Client().FissionClientSet.CoreV1().TimeTriggers(ttNs).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list Time triggers: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	headers := []string{"NAME", "CRON", "FUNCTION_NAME", "METHOD", "SUBPATH", "READY"}
	row := func(tt fv1.TimeTrigger) []string {
		return []string{
			tt.Name, tt.Spec.Cron, tt.Spec.Name, tt.Spec.Method, tt.Spec.Subpath,
			util.ConditionStatus(tt.Status.Conditions, fv1.TimeTriggerConditionReady),
		}
	}
	wideExtra := []string{"AGE"}
	wideRow := func(tt fv1.TimeTrigger) []string { return []string{util.AgeOf(tt.CreationTimestamp)} }

	return util.PrintObjects(format, tts.Items, headers, row, wideExtra, wideRow)
}

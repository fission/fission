// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httptrigger

import (
	"fmt"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

func (opts *ListSubCommand) do(input cli.Input) error {
	return opts.run(input)
}

func (opts *ListSubCommand) run(input cli.Input) (err error) {
	namespace, err := opts.ResolveNamespace(input)
	if err != nil {
		return fmt.Errorf("error in listing HTTP triggers: %w", err)
	}

	hts, err := opts.Client().FissionClientSet.CoreV1().HTTPTriggers(namespace).List(input.Context(), v1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing HTTP triggers: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	filterFunctionName := input.String(flagkey.HtFnName)

	var triggers []fv1.HTTPTrigger
	for _, ht := range hts.Items {
		// TODO: list canary http triggers as well.
		if len(filterFunctionName) == 0 ||
			(len(filterFunctionName) > 0 && filterFunctionName == ht.Spec.FunctionReference.Name) {

			triggers = append(triggers, ht)
		}
	}

	return printHtSummary(format, triggers)
}

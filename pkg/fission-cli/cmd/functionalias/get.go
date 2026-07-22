// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type GetSubCommand struct {
	cmd.CommandActioner
}

func Get(input cli.Input) error {
	return (&GetSubCommand{}).run(input)
}

func (opts *GetSubCommand) run(input cli.Input) (err error) {
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error getting function alias: %w", err)
	}

	alias, err := opts.Client().FissionClientSet.CoreV1().FunctionAliases(namespace).Get(input.Context(), input.String(flagkey.AliasName), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting function alias: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}
	if handled, err := util.PrintStructured(format, alias); err != nil || handled {
		return err
	}

	headers := []string{"NAME", "FUNCTION", "VERSION", "PACKAGE-DIGEST", "WEIGHT", "SECONDARY-VERSION", "RESOLVED-VERSION"}
	rows := [][]string{aliasRow(alias)}
	util.PrintTable(headers, rows)
	util.PrintConditions(alias.Status.Conditions)

	return nil
}

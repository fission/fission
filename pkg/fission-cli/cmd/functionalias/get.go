// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

import (
	"fmt"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
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

	format, err := util.PrintGet(input, alias, func() error {
		headers := []string{"NAME", "FUNCTION", "VERSION", "PACKAGE-DIGEST", "WEIGHT", "SECONDARY-VERSION", "RESOLVED-VERSION"}
		util.PrintTable(headers, [][]string{aliasRow(alias)})
		return nil
	})
	if err != nil {
		return err
	}
	if format == util.OutputTable {
		printHistory(alias.Status.History)
	}

	return nil
}

// printHistory writes a HISTORY block (VERSION / SWITCHED-AT) for a
// FunctionAlias's Status.History, most recent last (mirrors the field's own
// ordering — see AliasTargetRecord's doc comment). It is a no-op when
// History is empty, matching util.PrintConditionsTo's "nothing to print when
// nothing to show" contract, so `alias get` output is unchanged for an alias
// that has never repointed.
func printHistory(hist []fv1.AliasTargetRecord) {
	if len(hist) == 0 {
		return
	}
	w := util.NewTabWriter(os.Stdout)
	fmt.Fprintln(w, "\nHISTORY:")
	fmt.Fprintln(w, "VERSION\tSWITCHED-AT")
	for _, h := range hist {
		fmt.Fprintf(w, "%s\t%s\n", h.Version, util.AgeOf(h.SwitchedAt))
	}
	w.Flush()
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

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

func (opts *ListSubCommand) do(input cli.Input) error {
	namespace, err := opts.ResolveNamespace(input)
	if err != nil {
		return fmt.Errorf("error in listing function aliases: %w", err)
	}

	aliases, err := opts.Client().FissionClientSet.CoreV1().FunctionAliases(namespace).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing function aliases: %w", err)
	}

	items := filterByFunction(aliases.Items, input.String(flagkey.AliasFunction))

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	headers := []string{"NAME", "FUNCTION", "VERSION", "PACKAGE-DIGEST", "WEIGHT", "SECONDARY-VERSION", "RESOLVED-VERSION"}
	row := func(a fv1.FunctionAlias) []string { return aliasRow(&a) }
	wideExtra := []string{"NAMESPACE", "AGE"}
	wideRow := func(a fv1.FunctionAlias) []string { return []string{a.Namespace, util.AgeOf(a.CreationTimestamp)} }

	return util.PrintObjects(format, items, headers, row, wideExtra, wideRow)
}

// filterByFunction returns the subset of aliases targeting fnName. Newly
// created aliases now carry fv1.VersionFunctionNameLabel (stamped by `fission
// alias create` and `spec apply`), but the filter still runs client-side on
// Spec.FunctionName rather than a server-side label selector: aliases created
// before that label existed, or by hand, would silently be dropped from a
// selector-based List. Filtering stays client-side until a phase-3
// reconciler backfills the label onto every legacy FunctionAlias, at which
// point this can flip to a label selector like function/versions.go's.
// An empty fnName returns items unchanged.
func filterByFunction(items []fv1.FunctionAlias, fnName string) []fv1.FunctionAlias {
	if fnName == "" {
		return items
	}
	out := make([]fv1.FunctionAlias, 0, len(items))
	for _, a := range items {
		if a.Spec.FunctionName == fnName {
			out = append(out, a)
		}
	}
	return out
}

// aliasRow renders a's table cells, shared by `alias get` and `alias list`.
func aliasRow(a *fv1.FunctionAlias) []string {
	weight := util.NoneValue
	if a.Spec.Weight != nil {
		weight = fmt.Sprintf("%d", *a.Spec.Weight)
	}
	resolved := a.Status.ResolvedVersion
	if resolved == "" {
		resolved = util.NoneValue
	}
	return []string{
		a.Name, a.Spec.FunctionName, a.Spec.Version, a.Spec.PackageDigest,
		weight, a.Spec.SecondaryVersion, resolved,
	}
}

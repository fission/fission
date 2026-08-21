// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"fmt"
	"sort"
	"strings"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/versioning"
)

type DeleteSubCommand struct {
	cmd.CommandActioner
}

func Delete(input cli.Input) error {
	return (&DeleteSubCommand{}).do(input)
}

func (opts *DeleteSubCommand) do(input cli.Input) error {

	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in deleting function : %w", err)
	}
	m := &metav1.ObjectMeta{
		Name:      input.String(flagkey.FnName),
		Namespace: namespace,
	}

	printCascadeWarning(input.Context(), opts.Client().FissionClientSet, namespace, m.Name)

	err = opts.Client().FissionClientSet.CoreV1().Functions(namespace).Delete(input.Context(), input.String(flagkey.FnName), metav1.DeleteOptions{})
	if err != nil {
		if input.Bool(flagkey.IgnoreNotFound) && kerrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete function '%s': %w", m.Name, err)
	}

	fmt.Printf("function '%s' deleted\n", m.Name)
	return nil
}

// printCascadeWarning is a best-effort preflight for the cascade that
// deleting a Function triggers (RFC-0025): ownerRefs make Kubernetes
// garbage-collect every FunctionVersion and FunctionAlias for fnName
// silently, and any HTTPTrigger whose functionref.alias names one of those
// aliases starts 404ing with zero signal. It counts what is about to be
// swept and prints a warning before the delete call proceeds.
//
// Every lookup here is advisory only: an error listing versions, aliases,
// or triggers (RBAC denial, API server hiccup, etc.) is swallowed rather
// than surfaced, and must never block or fail the delete itself. When a
// function has no versions and no aliases (the common, unversioned case)
// this prints nothing at all, so `fn delete` output is byte-identical to
// before this warning existed.
func printCascadeWarning(ctx context.Context, fc versioned.Interface, namespace, fnName string) {
	versionCount := countVersions(ctx, fc, namespace, fnName)
	aliasNames := aliasNamesForFunction(ctx, fc, namespace, fnName)

	var parts []string
	if versionCount > 0 {
		parts = append(parts, versionPhrase(versionCount))
	}
	if len(aliasNames) > 0 {
		parts = append(parts, aliasPhrase(aliasNames))
	}
	if len(parts) == 0 {
		return
	}

	msg := fmt.Sprintf("warning: deleting function '%s' also deletes %s", fnName, strings.Join(parts, " and "))

	if triggerNames := triggersReferencingAliases(ctx, fc, namespace, aliasNames); len(triggerNames) > 0 {
		msg += fmt.Sprintf("; HTTPTriggers [%s] reference these aliases and will stop resolving", strings.Join(triggerNames, ", "))
	}

	fmt.Println(msg)
}

// countVersions returns the number of FunctionVersions owned by fnName, via
// versioning.ListVersionsForFunction (the same selector `fn versions` uses).
// Any List error is treated as "nothing to report", not a failure.
func countVersions(ctx context.Context, fc versioned.Interface, namespace, fnName string) int {
	items, err := versioning.ListVersionsForFunction(ctx, fc, namespace, fnName)
	if err != nil {
		return 0
	}
	return len(items)
}

// aliasNamesForFunction returns the sorted names of the FunctionAliases
// targeting fnName. It mirrors functionalias/list.go's filterByFunction:
// client-side filtering on Spec.FunctionName rather than a label selector,
// since aliases predating VersionFunctionNameLabel would silently be
// dropped from a selector-based List. Any List error is treated as
// "nothing to report", not a failure.
func aliasNamesForFunction(ctx context.Context, fc versioned.Interface, namespace, fnName string) []string {
	list, err := fc.CoreV1().FunctionAliases(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	var names []string
	for _, a := range list.Items {
		if a.Spec.FunctionName == fnName {
			names = append(names, a.Name)
		}
	}
	sort.Strings(names)
	return names
}

// triggersReferencingAliases returns the sorted names of the HTTPTriggers in
// namespace whose functionref.alias names one of aliasNames. Any List error
// is treated as "nothing to report", not a failure.
func triggersReferencingAliases(ctx context.Context, fc versioned.Interface, namespace string, aliasNames []string) []string {
	if len(aliasNames) == 0 {
		return nil
	}
	aliasSet := make(map[string]struct{}, len(aliasNames))
	for _, n := range aliasNames {
		aliasSet[n] = struct{}{}
	}

	list, err := fc.CoreV1().HTTPTriggers(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	var names []string
	for _, t := range list.Items {
		if _, ok := aliasSet[t.Spec.FunctionReference.Alias]; ok {
			names = append(names, t.Name)
		}
	}
	sort.Strings(names)
	return names
}

// versionPhrase renders the version-count clause of the cascade warning,
// e.g. "1 version" or "3 versions".
func versionPhrase(n int) string {
	if n == 1 {
		return "1 version"
	}
	return fmt.Sprintf("%d versions", n)
}

// aliasPhrase renders the alias-count clause of the cascade warning, e.g.
// "1 alias (prod)" or "2 aliases (prod, dev)".
func aliasPhrase(names []string) string {
	word := "aliases"
	if len(names) == 1 {
		word = "alias"
	}
	return fmt.Sprintf("%d %s (%s)", len(names), word, strings.Join(names, ", "))
}

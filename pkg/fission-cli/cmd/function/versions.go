// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// unaliasedCell is the ALIASED-BY table cell for a version no FunctionAlias
// currently targets, matching aliasEnvDriftStatus's (describe.go) "-" for
// "not applicable" rather than util.NoneValue's "<none>" (which this package
// otherwise reserves for "controller hasn't reported this yet").
const unaliasedCell = "-"

// digestTableWidth is how many characters of a package digest the VersionsList
// table format shows in the DIGEST column; wide/json/yaml always print the
// full digest.
const digestTableWidth = 19

type VersionsSubCommand struct {
	cmd.CommandActioner
}

// Versions lists the FunctionVersions published for a function, newest last
// (ascending Sequence).
func Versions(input cli.Input) error {
	return (&VersionsSubCommand{}).do(input)
}

func (opts *VersionsSubCommand) do(input cli.Input) error {
	fnName := input.String(flagkey.FnName)
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error listing function versions: %w", err)
	}

	selector := labels.SelectorFromSet(labels.Set{fv1.VersionFunctionNameLabel: fnName}).String()
	list, err := opts.Client().FissionClientSet.CoreV1().FunctionVersions(namespace).List(input.Context(), metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("error listing function versions: %w", err)
	}

	versions := sortedBySequence(list.Items)

	// "name" prints the bare version names and nothing else -- handled
	// locally, same as printPublishResult (publish.go), since
	// util.ParseOutputFormat doesn't recognize it and it needs neither the
	// alias join below nor the ENVDRIFT computation.
	outStr := input.String(flagkey.Output)
	if outStr == "name" {
		return printVersionNames(input.Stdout(), versions)
	}

	format, err := util.ParseOutputFormat(outStr)
	if err != nil {
		return err
	}

	aliases := aliasesByFunction(input.Context(), opts.Client().FissionClientSet, namespace, fnName)
	aliasedBy := aliasedByColumn(aliases)

	var drift map[string]string
	if format == util.OutputWide {
		drift = envDriftByVersion(input.Context(), opts.Client().FissionClientSet, namespace, versions)
	}

	return printVersionsList(versions, format, drift, aliasedBy)
}

// printVersionNames renders `fn versions -o name`: bare FunctionVersion
// names, one per line, in versions' own order -- which do() already sorted
// ascending by Sequence (sortedBySequence), so the newest version prints
// last, mirroring `fn publish`'s and `fn describe`'s own version-order
// conventions.
func printVersionNames(w io.Writer, versions []fv1.FunctionVersion) error {
	for _, v := range versions {
		if _, err := fmt.Fprintln(w, v.Name); err != nil {
			return err
		}
	}
	return nil
}

// aliasesByFunction lists fnName's FunctionAliases in namespace, filtered
// client-side on Spec.FunctionName -- mirrors describe.go's aliasesFor and
// functionalias/list.go's filterByFunction: an alias created before the
// ownership label existed (or by hand) would be silently dropped by a
// selector-based List.
func aliasesByFunction(ctx context.Context, cl versioned.Interface, namespace, fnName string) []fv1.FunctionAlias {
	list, err := cl.CoreV1().FunctionAliases(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	out := make([]fv1.FunctionAlias, 0, len(list.Items))
	for _, a := range list.Items {
		if a.Spec.FunctionName == fnName {
			out = append(out, a)
		}
	}
	return out
}

// aliasedByColumn builds the `fn versions` ALIASED-BY table cell for every
// version aliases points at: a comma-joined, sorted list of the alias names
// whose effective target (aliasEffectiveTarget, versionref.go — Spec.Version
// when name-pinned, else Status.ResolvedVersion) is that version. A version
// absent from the returned map has no aliases pointing at it; callers render
// that as unaliasedCell.
func aliasedByColumn(aliases []fv1.FunctionAlias) map[string]string {
	names := make(map[string][]string)
	for _, a := range aliases {
		target := aliasEffectiveTarget(&a)
		if target == "" {
			continue
		}
		names[target] = append(names[target], a.Name)
	}
	out := make(map[string]string, len(names))
	for version, ns := range names {
		sort.Strings(ns)
		out[version] = strings.Join(ns, ",")
	}
	return out
}

// envDriftByVersion computes, for -o wide, each version's ENVDRIFT table
// cell against its recorded Snapshot.Environment's live Generation: "True"
// (EnvObservedGeneration has fallen behind), "False" (still matches), or
// util.NoneValue when it is not assessable at all (no Environment recorded
// on the snapshot, or the Environment can no longer be read) — mirroring
// the AliasReconciler's EnvDrift condition "absence means not assessable"
// contract, condensed to a table cell instead of a condition. A function's
// versions normally all reference the same Environment identity, so this
// Gets each distinct (namespace, name) it encounters only once, not once
// per version.
func envDriftByVersion(ctx context.Context, cl versioned.Interface, namespace string, versions []fv1.FunctionVersion) map[string]string {
	drift := make(map[string]string, len(versions))
	envCache := make(map[string]*fv1.Environment)

	for _, v := range versions {
		envName := v.Spec.Snapshot.Environment.Name
		if envName == "" {
			drift[v.Name] = util.NoneValue
			continue
		}

		// Mirrors publish.go:118's envNS fallback: an unset Snapshot
		// Environment namespace means "same namespace as the function".
		envNS := v.Spec.Snapshot.Environment.Namespace
		if envNS == "" {
			envNS = namespace
		}

		key := envNS + "/" + envName
		env, cached := envCache[key]
		if !cached {
			got, err := cl.CoreV1().Environments(envNS).Get(ctx, envName, metav1.GetOptions{})
			if err == nil {
				env = got
			}
			envCache[key] = env
		}

		if env == nil {
			drift[v.Name] = util.NoneValue
			continue
		}
		if v.Spec.EnvObservedGeneration != env.Generation {
			drift[v.Name] = "True"
		} else {
			drift[v.Name] = "False"
		}
	}
	return drift
}

// sortedBySequence returns items sorted ascending by Spec.Sequence (v1 first),
// the order versions were minted in.
func sortedBySequence(items []fv1.FunctionVersion) []fv1.FunctionVersion {
	out := make([]fv1.FunctionVersion, len(items))
	copy(out, items)
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.Sequence < out[j].Spec.Sequence })
	return out
}

// printVersionsList renders versions per format, in the shared shape
// util.PrintObjects gives every list command (see functionalias/list.go).
// json/yaml marshal the full slice (untruncated digests, and neither
// ALIASED-BY nor ENVDRIFT/DESCRIPTION -- those are table-only cells, not
// FunctionVersion fields); the table format truncates DIGEST to
// digestTableWidth characters so the row fits a terminal and includes
// ALIASED-BY (sourced from aliasedBy, keyed by version Name -- see
// aliasedByColumn -- unaliasedCell for a version no entry names), wide
// prints DIGEST in full and additionally appends ENVDRIFT (sourced from
// drift, keyed by version Name; envDriftByVersion builds it, empty/nil is
// treated as util.NoneValue for every row -- this happens for every non-wide
// call, which never sees the column at all) and DESCRIPTION (the
// fission.io/description publish annotation, via describe.go's
// versionDescription).
func printVersionsList(versions []fv1.FunctionVersion, format util.OutputFormat, drift map[string]string, aliasedBy map[string]string) error {
	truncate := format != util.OutputWide
	headers := []string{"NAME", "SEQUENCE", "DIGEST", "PUBLISHED", "ALIASED-BY", "AGE"}
	row := func(v fv1.FunctionVersion) []string {
		digest := v.Spec.PackageDigest
		if truncate {
			digest = truncateDigest(digest)
		}
		aliases, ok := aliasedBy[v.Name]
		if !ok || aliases == "" {
			aliases = unaliasedCell
		}
		return []string{
			v.Name,
			fmt.Sprintf("%d", v.Spec.Sequence),
			digest,
			v.Spec.PublishedAt.Format(time.RFC3339),
			aliases,
			util.AgeOf(v.CreationTimestamp),
		}
	}
	wideRow := func(v fv1.FunctionVersion) []string {
		d, ok := drift[v.Name]
		if !ok {
			d = util.NoneValue
		}
		return []string{d, versionDescription(&v)}
	}
	return util.PrintObjects(format, versions, headers, row, []string{"ENVDRIFT", "DESCRIPTION"}, wideRow)
}

// truncateDigest shortens d to at most digestTableWidth characters for the
// table DIGEST column.
func truncateDigest(d string) string {
	if len(d) <= digestTableWidth {
		return d
	}
	return d[:digestTableWidth]
}

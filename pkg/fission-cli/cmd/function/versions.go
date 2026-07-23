// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"fmt"
	"sort"
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

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	var drift map[string]string
	if format == util.OutputWide {
		drift = envDriftByVersion(input.Context(), opts.Client().FissionClientSet, namespace, versions)
	}

	return printVersionsList(versions, format, drift)
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
// json/yaml marshal the full slice (untruncated digests); the table format
// truncates DIGEST to digestTableWidth characters so the row fits a
// terminal, wide prints it in full and additionally appends an ENVDRIFT
// column sourced from drift (keyed by version Name; envDriftByVersion
// builds it, empty/nil is treated as util.NoneValue for every row — this
// happens for every non-wide call, which never sees the column at all, and
// callers that already know the answer, e.g. tests, may pass an explicit
// map).
func printVersionsList(versions []fv1.FunctionVersion, format util.OutputFormat, drift map[string]string) error {
	truncate := format != util.OutputWide
	headers := []string{"NAME", "SEQUENCE", "DIGEST", "PUBLISHED", "AGE"}
	row := func(v fv1.FunctionVersion) []string {
		digest := v.Spec.PackageDigest
		if truncate {
			digest = truncateDigest(digest)
		}
		return []string{
			v.Name,
			fmt.Sprintf("%d", v.Spec.Sequence),
			digest,
			v.Spec.PublishedAt.Format(time.RFC3339),
			util.AgeOf(v.CreationTimestamp),
		}
	}
	wideRow := func(v fv1.FunctionVersion) []string {
		d, ok := drift[v.Name]
		if !ok {
			d = util.NoneValue
		}
		return []string{d}
	}
	return util.PrintObjects(format, versions, headers, row, []string{"ENVDRIFT"}, wideRow)
}

// truncateDigest shortens d to at most digestTableWidth characters for the
// table DIGEST column.
func truncateDigest(d string) string {
	if len(d) <= digestTableWidth {
		return d
	}
	return d[:digestTableWidth]
}

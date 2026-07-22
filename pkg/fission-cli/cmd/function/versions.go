// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
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

	return printVersionsList(versions, format)
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
// terminal, wide prints it in full. There are no wide-only extra columns
// here, so row itself closes over truncate (fixed for the whole call, from
// format) rather than the headers/wideExtra split list.go uses for its
// NAMESPACE/AGE columns.
func printVersionsList(versions []fv1.FunctionVersion, format util.OutputFormat) error {
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
	wideRow := func(v fv1.FunctionVersion) []string { return nil }
	return util.PrintObjects(format, versions, headers, row, nil, wideRow)
}

// truncateDigest shortens d to at most digestTableWidth characters for the
// table DIGEST column.
func truncateDigest(d string) string {
	if len(d) <= digestTableWidth {
		return d
	}
	return d[:digestTableWidth]
}

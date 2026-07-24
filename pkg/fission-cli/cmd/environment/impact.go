// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package environment

import (
	"context"
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// driftOtherEnv is impactRowForAlias's Drift verdict when the alias's
// resolved FunctionVersion's snapshot references a DIFFERENT Environment
// than the one `env impact` was queried for — e.g. the Function has since
// been repointed at a new Environment, but this alias's resolved target was
// published back when the Function still referenced the old one. Comparing
// that version's EnvObservedGeneration (recorded against the old
// Environment) to the QUERIED Environment's live Generation would be a
// bogus comparison across two unrelated Environment identities, so this
// value replaces True/False rather than a misleading generation diff.
const driftOtherEnv = "OtherEnv"

// ImpactRow is one row of `fission env impact`'s output: a Function that
// references the named Environment, optionally paired with one of its
// FunctionAliases and that alias's resolved target's env-drift verdict
// (RFC-0025 "Environment & Package changes across the version boundary" —
// see AliasReconciler's EnvDrift condition, which this command surfaces
// on-demand for a whole Environment's blast radius instead of one alias at
// a time). A Function with no aliases yet still gets one row (Alias/
// TargetVersion/Drift all util.NoneValue) so its exposure is visible even
// before any version has been published.
type ImpactRow struct {
	Function              string `json:"function"`
	Alias                 string `json:"alias"`
	TargetVersion         string `json:"targetVersion"`
	EnvObservedGeneration int64  `json:"envObservedGeneration,omitempty"`
	LiveGeneration        int64  `json:"liveGeneration"`
	Drift                 string `json:"drift"`
}

type ImpactSubCommand struct {
	cmd.CommandActioner
}

// Impact lists every Function referencing an Environment, and — for each of
// their FunctionAliases — whether the alias's currently resolved
// FunctionVersion was published under a since-superseded Environment
// generation. It is the batch, ahead-of-an-update counterpart to the
// AliasReconciler's per-alias EnvDrift condition: "if I change this
// Environment right now, what/who is affected?"
func Impact(input cli.Input) error {
	return (&ImpactSubCommand{}).do(input)
}

func (opts *ImpactSubCommand) do(input cli.Input) error {
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error computing environment impact: %w", err)
	}
	envName := input.String(flagkey.EnvName)

	env, err := opts.Client().FissionClientSet.CoreV1().Environments(namespace).Get(input.Context(), envName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting environment '%v': %w", envName, err)
	}

	fns, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing functions: %w", err)
	}
	impacted := filterFunctionsByEnvironment(fns.Items, namespace, envName)

	aliases, err := opts.Client().FissionClientSet.CoreV1().FunctionAliases(namespace).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing function aliases: %w", err)
	}

	rows := buildImpactRows(input.Context(), opts.Client().FissionClientSet, namespace, env, impacted, aliases.Items)

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	headers := []string{"FUNCTION", "ALIAS", "TARGET-VERSION", "ENV-OBSERVED-GEN", "LIVE-GEN", "DRIFT"}
	row := func(r ImpactRow) []string {
		obsGen := util.NoneValue
		if r.Drift == "True" || r.Drift == "False" {
			obsGen = fmt.Sprintf("%d", r.EnvObservedGeneration)
		}
		return []string{
			r.Function, r.Alias, r.TargetVersion,
			obsGen,
			fmt.Sprintf("%d", r.LiveGeneration),
			r.Drift,
		}
	}
	wideRow := func(r ImpactRow) []string { return nil }
	return util.PrintObjects(format, rows, headers, row, nil, wideRow)
}

// filterFunctionsByEnvironment returns the Functions in fns whose
// Spec.Environment resolves to (namespace, envName) — mirroring publish.go's
// envNS fallback: an unset Spec.Environment.Namespace means "the function's
// own namespace", and every fn here already lives in namespace (this is a
// single-namespace List), so the fallback and explicit-match cases collapse
// to the same comparison. Sorted by name for deterministic output.
func filterFunctionsByEnvironment(fns []fv1.Function, namespace, envName string) []fv1.Function {
	var out []fv1.Function
	for _, fn := range fns {
		if fn.Spec.Environment.Name != envName {
			continue
		}
		envNS := fn.Spec.Environment.Namespace
		if envNS == "" {
			envNS = fn.Namespace
		}
		if envNS != namespace {
			continue
		}
		out = append(out, fn)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// buildImpactRows joins impacted functions against aliases (filtered
// client-side on Spec.FunctionName, matching functionalias/list.go's
// rationale: not every alias is guaranteed to carry
// VersionFunctionNameLabel), resolving each alias's target FunctionVersion
// to compare its EnvObservedGeneration against env.Generation.
func buildImpactRows(ctx context.Context, cl versioned.Interface, namespace string, env *fv1.Environment, fns []fv1.Function, aliases []fv1.FunctionAlias) []ImpactRow {
	byFunction := make(map[string][]fv1.FunctionAlias)
	for _, a := range aliases {
		byFunction[a.Spec.FunctionName] = append(byFunction[a.Spec.FunctionName], a)
	}

	var rows []ImpactRow
	for _, fn := range fns {
		fnAliases := byFunction[fn.Name]
		sort.Slice(fnAliases, func(i, j int) bool { return fnAliases[i].Name < fnAliases[j].Name })

		if len(fnAliases) == 0 {
			rows = append(rows, ImpactRow{
				Function:       fn.Name,
				Alias:          util.NoneValue,
				TargetVersion:  util.NoneValue,
				LiveGeneration: env.Generation,
				Drift:          util.NoneValue,
			})
			continue
		}

		for _, a := range fnAliases {
			rows = append(rows, impactRowForAlias(ctx, cl, namespace, env, fn.Name, a))
		}
	}
	return rows
}

// impactRowForAlias resolves one alias's drift verdict against env. An
// unresolved alias, or one whose resolved FunctionVersion can no longer be
// read, is not assessable (util.NoneValue) — mirroring
// AliasReconciler.applyEnvDrift's "absence means not assessable" contract.
// A resolved FunctionVersion whose snapshot references a DIFFERENT
// Environment than env gets driftOtherEnv instead of a generation
// comparison: see driftOtherEnv's doc comment for why (a Function can be
// repointed at a new Environment between publishes, leaving older,
// still-aliased versions snapshotted against the old one).
func impactRowForAlias(ctx context.Context, cl versioned.Interface, namespace string, env *fv1.Environment, fnName string, a fv1.FunctionAlias) ImpactRow {
	row := ImpactRow{
		Function:       fnName,
		Alias:          a.Name,
		TargetVersion:  util.NoneValue,
		LiveGeneration: env.Generation,
		Drift:          util.NoneValue,
	}

	if a.Status.ResolvedVersion == "" {
		return row
	}
	row.TargetVersion = a.Status.ResolvedVersion

	v, err := cl.CoreV1().FunctionVersions(namespace).Get(ctx, a.Status.ResolvedVersion, metav1.GetOptions{})
	if err != nil {
		return row
	}

	if !snapshotEnvMatches(v, namespace, env) {
		row.Drift = driftOtherEnv
		return row
	}

	row.EnvObservedGeneration = v.Spec.EnvObservedGeneration
	if v.Spec.EnvObservedGeneration != env.Generation {
		row.Drift = "True"
	} else {
		row.Drift = "False"
	}
	return row
}

// snapshotEnvMatches reports whether v's Snapshot.Environment actually
// resolves to env — the same publish.go:118 namespace-fallback logic
// AliasReconciler.applyEnvDrift and `fn versions -o wide`'s
// envDriftByVersion use (unset Snapshot Environment namespace means "the
// function's own namespace"; namespace here is that namespace, since fns
// was already scoped to it). A version's snapshot is the reference for what
// Environment it was actually published against — the Function's CURRENT
// Spec.Environment (what filterFunctionsByEnvironment matched on) can have
// moved on since.
func snapshotEnvMatches(v *fv1.FunctionVersion, namespace string, env *fv1.Environment) bool {
	if v.Spec.Snapshot.Environment.Name != env.Name {
		return false
	}
	envNS := v.Spec.Snapshot.Environment.Namespace
	if envNS == "" {
		envNS = namespace
	}
	return envNS == env.Namespace
}

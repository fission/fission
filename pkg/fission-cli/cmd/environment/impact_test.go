// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package environment

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	orig := os.Stdout
	t.Cleanup(func() { os.Stdout = orig })
	os.Stdout = w
	if err := fn(); err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	w.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func impactFn(name, envName, envNS string) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: envName, Namespace: envNS}},
	}
}

func TestFilterFunctionsByEnvironment(t *testing.T) {
	fns := []fv1.Function{
		*impactFn("same-ns-explicit", "nodejs", "default"),
		*impactFn("same-ns-fallback", "nodejs", ""),
		*impactFn("cross-ns", "nodejs", "other-ns"),
		*impactFn("different-env", "python", "default"),
	}

	got := filterFunctionsByEnvironment(fns, "default", "nodejs")

	names := make([]string, 0, len(got))
	for _, fn := range got {
		names = append(names, fn.Name)
	}
	assert.Equal(t, []string{"same-ns-explicit", "same-ns-fallback"}, names, "sorted by name; cross-ns and different-env excluded")
}

func TestBuildImpactRowsFunctionWithNoAliases(t *testing.T) {
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "nodejs", Namespace: "default", Generation: 3}}
	fns := []fv1.Function{*impactFn("hello", "nodejs", "")}

	rows := buildImpactRows(t.Context(), fissionfake.NewClientset(), "default", env, fns, nil)

	require.Len(t, rows, 1)
	assert.Equal(t, "hello", rows[0].Function)
	assert.Equal(t, util.NoneValue, rows[0].Alias)
	assert.Equal(t, util.NoneValue, rows[0].TargetVersion)
	assert.Equal(t, util.NoneValue, rows[0].Drift)
	assert.Equal(t, int64(3), rows[0].LiveGeneration)
}

func TestBuildImpactRowsUnresolvedAlias(t *testing.T) {
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "nodejs", Namespace: "default", Generation: 1}}
	fns := []fv1.Function{*impactFn("hello", "nodejs", "")}
	aliases := []fv1.FunctionAlias{{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v9"},
		// Status.ResolvedVersion left empty: never resolved.
	}}

	rows := buildImpactRows(t.Context(), fissionfake.NewClientset(), "default", env, fns, aliases)

	require.Len(t, rows, 1)
	assert.Equal(t, "prod", rows[0].Alias)
	assert.Equal(t, util.NoneValue, rows[0].TargetVersion)
	assert.Equal(t, util.NoneValue, rows[0].Drift, "unresolved alias is not assessable")
}

func TestBuildImpactRowsResolvedDriftedAndCurrent(t *testing.T) {
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "nodejs", Namespace: "default", Generation: 2}}
	fns := []fv1.Function{*impactFn("hello", "nodejs", "")}
	aliases := []fv1.FunctionAlias{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
			Status:     fv1.FunctionAliasStatus{ResolvedVersion: "hello-v1"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "canary", Namespace: "default"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v2"},
			Status:     fv1.FunctionAliasStatus{ResolvedVersion: "hello-v2"},
		},
	}
	v1 := &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1", Namespace: "default"},
		Spec: fv1.FunctionVersionSpec{
			FunctionName: "hello", Sequence: 1, EnvObservedGeneration: 1, // stale
			Snapshot: fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "nodejs"}},
		},
	}
	v2 := &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v2", Namespace: "default"},
		Spec: fv1.FunctionVersionSpec{
			FunctionName: "hello", Sequence: 2, EnvObservedGeneration: 2, // current
			Snapshot: fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "nodejs"}},
		},
	}

	rows := buildImpactRows(t.Context(), fissionfake.NewClientset(v1, v2), "default", env, fns, aliases)

	require.Len(t, rows, 2)
	byAlias := map[string]ImpactRow{}
	for _, r := range rows {
		byAlias[r.Alias] = r
	}
	assert.Equal(t, "True", byAlias["prod"].Drift)
	assert.Equal(t, int64(1), byAlias["prod"].EnvObservedGeneration)
	assert.Equal(t, "False", byAlias["canary"].Drift)
	assert.Equal(t, int64(2), byAlias["canary"].EnvObservedGeneration)
}

func TestSnapshotEnvMatches(t *testing.T) {
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "nodejs", Namespace: "default"}}

	cases := []struct {
		name    string
		envRef  fv1.EnvironmentReference
		fnNS    string
		matches bool
	}{
		{"same-ns explicit", fv1.EnvironmentReference{Name: "nodejs", Namespace: "default"}, "default", true},
		{"same-ns fallback (unset namespace)", fv1.EnvironmentReference{Name: "nodejs"}, "default", true},
		{"different name", fv1.EnvironmentReference{Name: "python"}, "default", false},
		{"different namespace, explicit", fv1.EnvironmentReference{Name: "nodejs", Namespace: "other-ns"}, "default", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &fv1.FunctionVersion{Spec: fv1.FunctionVersionSpec{Snapshot: fv1.FunctionSpec{Environment: tc.envRef}}}
			assert.Equal(t, tc.matches, snapshotEnvMatches(v, tc.fnNS, env))
		})
	}
}

// TestBuildImpactRowsAliasResolvedToVersionFromDifferentEnvironment is the
// review-flagged regression: hello has since been repointed at env-b
// (Spec.Environment, matched by filterFunctionsByEnvironment), but its
// "prod" alias is still resolved to hello-v1, which was published back when
// hello referenced env-a. Comparing hello-v1's EnvObservedGeneration
// (recorded against env-a) to env-b's live Generation would be a bogus
// cross-environment comparison; the row must report driftOtherEnv instead
// of a misleading True/False.
func TestBuildImpactRowsAliasResolvedToVersionFromDifferentEnvironment(t *testing.T) {
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "env-b", Namespace: "default", Generation: 5}}
	fn := impactFn("hello", "env-b", "") // hello's CURRENT environment reference
	aliases := []fv1.FunctionAlias{{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
		Status:     fv1.FunctionAliasStatus{ResolvedVersion: "hello-v1"},
	}}
	// hello-v1 predates the env-a -> env-b move: its snapshot still names env-a.
	v1 := &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1", Namespace: "default"},
		Spec: fv1.FunctionVersionSpec{
			FunctionName: "hello", Sequence: 1, EnvObservedGeneration: 9,
			Snapshot: fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "env-a"}},
		},
	}

	rows := buildImpactRows(t.Context(), fissionfake.NewClientset(v1), "default", env, []fv1.Function{*fn}, aliases)

	require.Len(t, rows, 1)
	assert.Equal(t, "hello-v1", rows[0].TargetVersion, "the resolved target name is still reported")
	assert.Equal(t, driftOtherEnv, rows[0].Drift, "the version was published against a different environment; no meaningful drift verdict against env-b")
	assert.Zero(t, rows[0].EnvObservedGeneration, "not populated for a cross-environment mismatch")
}

// TestBuildImpactRowsResolvedDriftedAndCurrentIsTheNormalCase re-affirms
// the ordinary same-environment path (already covered by
// TestBuildImpactRowsResolvedDriftedAndCurrent above) still classifies
// True/False rather than driftOtherEnv, now that snapshotEnvMatches gates
// the comparison.
func TestBuildImpactRowsResolvedDriftedAndCurrentIsTheNormalCase(t *testing.T) {
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "nodejs", Namespace: "default", Generation: 2}}
	fn := impactFn("hello", "nodejs", "")
	aliases := []fv1.FunctionAlias{{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
		Status:     fv1.FunctionAliasStatus{ResolvedVersion: "hello-v1"},
	}}
	v1 := &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1", Namespace: "default"},
		Spec: fv1.FunctionVersionSpec{
			FunctionName: "hello", Sequence: 1, EnvObservedGeneration: 1,
			Snapshot: fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "nodejs"}},
		},
	}

	rows := buildImpactRows(t.Context(), fissionfake.NewClientset(v1), "default", env, []fv1.Function{*fn}, aliases)

	require.Len(t, rows, 1)
	assert.Equal(t, "True", rows[0].Drift)
	assert.Equal(t, int64(1), rows[0].EnvObservedGeneration)
}

func TestBuildImpactRowsResolvedVersionMissingIsNotAssessable(t *testing.T) {
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "nodejs", Namespace: "default", Generation: 1}}
	fns := []fv1.Function{*impactFn("hello", "nodejs", "")}
	aliases := []fv1.FunctionAlias{{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
		Status:     fv1.FunctionAliasStatus{ResolvedVersion: "hello-v1"}, // no such FunctionVersion object
	}}

	rows := buildImpactRows(t.Context(), fissionfake.NewClientset(), "default", env, fns, aliases)

	require.Len(t, rows, 1)
	assert.Equal(t, "hello-v1", rows[0].TargetVersion, "the resolved name is still reported")
	assert.Equal(t, util.NoneValue, rows[0].Drift, "but the missing version makes drift unassessable")
}

func impactFlags(envName string) dummy.Cli {
	in := dummy.TestFlagSet()
	in.Set(flagkey.EnvName, envName)
	return in
}

func TestImpactCommandEndToEnd(t *testing.T) {
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "nodejs", Namespace: "default", Generation: 2}}
	fn := impactFn("hello", "nodejs", "")
	alias := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
		Status:     fv1.FunctionAliasStatus{ResolvedVersion: "hello-v1"},
	}
	v1 := &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1", Namespace: "default"},
		Spec: fv1.FunctionVersionSpec{
			FunctionName: "hello", Sequence: 1, EnvObservedGeneration: 1,
			Snapshot: fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "nodejs"}},
		},
	}
	otherFn := impactFn("unrelated", "python", "")

	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(env, fn, alias, v1, otherFn),
		Namespace:        "default",
	})

	out := captureStdout(t, func() error { return Impact(impactFlags("nodejs")) })

	assert.Contains(t, out, "hello")
	assert.Contains(t, out, "prod")
	assert.Contains(t, out, "hello-v1")
	assert.Contains(t, out, "True")
	assert.False(t, strings.Contains(out, "unrelated"), "a function referencing a different environment must not appear:\n%s", out)
}

func TestImpactCommandJSONOutput(t *testing.T) {
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "nodejs", Namespace: "default", Generation: 1}}
	fn := impactFn("hello", "nodejs", "")

	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(env, fn),
		Namespace:        "default",
	})

	in := impactFlags("nodejs")
	in.Set(flagkey.Output, "json")
	out := captureStdout(t, func() error { return Impact(in) })

	var got []ImpactRow
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "hello", got[0].Function)
	assert.Equal(t, util.NoneValue, got[0].Alias)
}

func TestImpactCommandUnknownEnvironmentErrors(t *testing.T) {
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(),
		Namespace:        "default",
	})

	err := Impact(impactFlags("does-not-exist"))
	require.Error(t, err)
}

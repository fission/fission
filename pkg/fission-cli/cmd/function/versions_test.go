// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func TestTruncateDigest(t *testing.T) {
	short := "sha256:abc"
	assert.Equal(t, short, truncateDigest(short), "shorter than the width should be unchanged")

	long := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef"
	got := truncateDigest(long)
	assert.Len(t, got, digestTableWidth)
	assert.Equal(t, long[:digestTableWidth], got)
}

func TestSortedBySequence(t *testing.T) {
	items := []fv1.FunctionVersion{
		{Name: "v3", Spec: fv1.FunctionVersionSpec{Sequence: 3}},
		{Name: "v1", Spec: fv1.FunctionVersionSpec{Sequence: 1}},
		{Name: "v2", Spec: fv1.FunctionVersionSpec{Sequence: 2}},
	}
	got := sortedBySequence(items)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"v1", "v2", "v3"}, []string{got[0].Name, got[1].Name, got[2].Name})

	// The input slice must not be mutated (callers may hold onto the original
	// List() result).
	assert.Equal(t, "v3", items[0].Name)
}

func TestPrintVersionsListTableTruncatesDigest(t *testing.T) {
	longDigest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef"
	versions := []fv1.FunctionVersion{{
		Name: "hello-v1",
		Spec: fv1.FunctionVersionSpec{Sequence: 1, PackageDigest: longDigest},
	}}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputTable, nil, nil) })
	assert.Contains(t, out, "hello-v1")
	assert.Contains(t, out, truncateDigest(longDigest))
	assert.False(t, strings.Contains(out, longDigest), "table output should not contain the full digest:\n%s", out)
}

func TestPrintVersionsListWideKeepsFullDigest(t *testing.T) {
	longDigest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef"
	versions := []fv1.FunctionVersion{{
		Name: "hello-v1",
		Spec: fv1.FunctionVersionSpec{Sequence: 1, PackageDigest: longDigest},
	}}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputWide, nil, nil) })
	assert.Contains(t, out, longDigest)
}

func TestPrintVersionsListJSON(t *testing.T) {
	longDigest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef"
	versions := []fv1.FunctionVersion{{
		Name: "hello-v1",
		Spec: fv1.FunctionVersionSpec{Sequence: 1, PackageDigest: longDigest},
	}}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputJSON, nil, nil) })
	var got []fv1.FunctionVersion
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got, 1)
	assert.Equal(t, longDigest, got[0].Spec.PackageDigest, "json must carry the full, untruncated digest")
}

func TestPrintVersionsListWideAddsEnvDriftColumn(t *testing.T) {
	versions := []fv1.FunctionVersion{{
		Name: "hello-v1",
		Spec: fv1.FunctionVersionSpec{Sequence: 1},
	}}
	drift := map[string]string{"hello-v1": "True"}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputWide, drift, nil) })
	assert.Contains(t, out, "ENVDRIFT")
	assert.Contains(t, out, "True")
}

func TestPrintVersionsListWideDriftMissingFallsBackToNone(t *testing.T) {
	versions := []fv1.FunctionVersion{{
		Name: "hello-v1",
		Spec: fv1.FunctionVersionSpec{Sequence: 1},
	}}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputWide, nil, nil) })
	assert.Contains(t, out, util.NoneValue)
}

func TestPrintVersionsListTableOmitsEnvDriftColumn(t *testing.T) {
	versions := []fv1.FunctionVersion{{
		Name: "hello-v1",
		Spec: fv1.FunctionVersionSpec{Sequence: 1},
	}}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputTable, nil, nil) })
	assert.False(t, strings.Contains(out, "ENVDRIFT"), "the plain table format must not gain the wide-only column:\n%s", out)
}

func TestEnvDriftByVersionDetectsDrift(t *testing.T) {
	cmd.ResetClientsetForTest()
	env := &fv1.Environment{
		Name: "nodejs", Namespace: "default", Generation: 2,
	}
	fc := fissionfake.NewClientset(env)
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

	versions := []fv1.FunctionVersion{
		{
			Name: "hello-v1",
			Spec: fv1.FunctionVersionSpec{
				Sequence:              1,
				EnvObservedGeneration: 1, // stale vs live generation 2
				Snapshot:              fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "nodejs"}},
			},
		},
		{
			Name: "hello-v2",
			Spec: fv1.FunctionVersionSpec{
				Sequence:              2,
				EnvObservedGeneration: 2, // current
				Snapshot:              fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "nodejs"}},
			},
		},
		{
			Name: "hello-v3",
			Spec: fv1.FunctionVersionSpec{
				Sequence: 3, // no Environment recorded on the snapshot
			},
		},
	}

	drift := envDriftByVersion(t.Context(), fc, "default", versions)
	assert.Equal(t, "True", drift["hello-v1"])
	assert.Equal(t, "False", drift["hello-v2"])
	assert.Equal(t, util.NoneValue, drift["hello-v3"])
}

func TestEnvDriftByVersionUnknownWhenEnvironmentMissing(t *testing.T) {
	cmd.ResetClientsetForTest()
	fc := fissionfake.NewClientset()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

	versions := []fv1.FunctionVersion{{
		Name: "hello-v1",
		Spec: fv1.FunctionVersionSpec{
			Sequence: 1,
			Snapshot: fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "does-not-exist"}},
		},
	}}

	drift := envDriftByVersion(t.Context(), fc, "default", versions)
	assert.Equal(t, util.NoneValue, drift["hello-v1"])
}

// TestVersionsCommandFiltersByFunctionLabel exercises the command-level
// wiring end to end: List() -> label-selector filter -> sort -> print,
// against a fake clientset with versions from two different functions.
func TestVersionsCommandFiltersByFunctionLabel(t *testing.T) {
	mkVersion := func(name, fn string, seq int64) *fv1.FunctionVersion {
		return &fv1.FunctionVersion{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{fv1.VersionFunctionNameLabel: fn},
			Spec:      fv1.FunctionVersionSpec{FunctionName: fn, Sequence: seq},
		}
	}

	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(
			mkVersion("hello-v2", "hello", 2),
			mkVersion("hello-v1", "hello", 1),
			mkVersion("other-v1", "other", 1),
		),
		Namespace: "default",
	})

	in := dummy.TestFlagSet()
	in.SetString(flagkey.FnName, "hello")
	out := captureStdout(t, func() error { return Versions(in) })

	assert.Contains(t, out, "hello-v1")
	assert.Contains(t, out, "hello-v2")
	assert.False(t, strings.Contains(out, "other-v1"), "versions of a different function must not be listed:\n%s", out)
	// v1 (older) should be printed before v2 (ascending Sequence).
	assert.Less(t, strings.Index(out, "hello-v1"), strings.Index(out, "hello-v2"))
}

// TestVersionsCommandWideShowsEnvDrift is the command-level end-to-end for
// the ENVDRIFT column: Versions() -> envDriftByVersion() -> printVersionsList().
func TestVersionsCommandWideShowsEnvDrift(t *testing.T) {
	env := &fv1.Environment{
		Name: "nodejs", Namespace: "default", Generation: 2,
	}
	v := &fv1.FunctionVersion{
		Name:      "hello-v1",
		Namespace: "default",
		Labels:    map[string]string{fv1.VersionFunctionNameLabel: "hello"},
		Spec: fv1.FunctionVersionSpec{
			FunctionName:          "hello",
			Sequence:              1,
			EnvObservedGeneration: 1,
			Snapshot:              fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "nodejs"}},
		},
	}

	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(env, v),
		Namespace:        "default",
	})

	in := dummy.TestFlagSet()
	in.SetString(flagkey.FnName, "hello")
	in.SetString(flagkey.Output, "wide")
	out := captureStdout(t, func() error { return Versions(in) })

	assert.Contains(t, out, "ENVDRIFT")
	assert.Contains(t, out, "hello-v1")
	assert.Contains(t, out, "True")
}

func TestAliasedByColumn(t *testing.T) {
	aliases := []fv1.FunctionAlias{
		{Name: "prod", Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v2"}},
		{Name: "canary", Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v2"}},
		{Name: "staging", Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"}},
		// A digest-pinned alias that has not resolved yet has no effective
		// target (fv1.FunctionAlias.EffectiveTarget returns "") and must not
		// appear in the column at all, for any version.
		{Name: "unresolved", Spec: fv1.FunctionAliasSpec{FunctionName: "hello", PackageDigest: "sha256:abc"}},
	}

	got := aliasedByColumn(aliases)

	// Multiple aliases on one version: sorted, comma-joined.
	assert.Equal(t, "canary,prod", got["hello-v2"])
	assert.Equal(t, "staging", got["hello-v1"])
	// A version with no aliases has no entry at all.
	_, ok := got["hello-v3"]
	assert.False(t, ok)
}

// TestPrintVersionsListAliasedByColumn is the DEFAULT-table-level test for
// the ALIASED-BY column: present (comma-joined, sorted) for an aliased
// version, "-" for an unaliased one, and present even outside -o wide.
func TestPrintVersionsListAliasedByColumn(t *testing.T) {
	versions := []fv1.FunctionVersion{
		{Name: "hello-v1", Spec: fv1.FunctionVersionSpec{Sequence: 1}},
		{Name: "hello-v2", Spec: fv1.FunctionVersionSpec{Sequence: 2}},
	}
	aliasedBy := map[string]string{"hello-v2": "canary,prod"}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputTable, nil, aliasedBy) })

	assert.Contains(t, out, "ALIASED-BY")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 3) // header + 2 rows

	v1Row := lines[1]
	v2Row := lines[2]
	assert.True(t, strings.Contains(v1Row, "hello-v1"))
	assert.True(t, strings.Contains(v2Row, "hello-v2") && strings.Contains(v2Row, "canary,prod"))
	// hello-v1 has no aliases: its ALIASED-BY cell is "-", not empty and not
	// util.NoneValue.
	fields := strings.Fields(v1Row)
	assert.Equal(t, unaliasedCell, fields[len(fields)-2], "ALIASED-BY is the column before AGE: %q", v1Row)
}

// TestPrintVersionsListWideAddsDescriptionColumn covers the -o wide
// DESCRIPTION column, sourced from the fission.io/description annotation --
// previously write-only from the CLI's point of view.
func TestPrintVersionsListWideAddsDescriptionColumn(t *testing.T) {
	versions := []fv1.FunctionVersion{
		{
			Name: "hello-v1", Annotations: map[string]string{"fission.io/description": "fix the widget bug"},
			Spec: fv1.FunctionVersionSpec{Sequence: 1},
		},
		{
			Name: "hello-v2",
			Spec: fv1.FunctionVersionSpec{Sequence: 2},
		},
	}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputWide, nil, nil) })

	assert.Contains(t, out, "DESCRIPTION")
	assert.Contains(t, out, "fix the widget bug")
	// hello-v2 has no description annotation: renders "-", same convention
	// as describe.go's versionDescription.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 3)
	fields := strings.Fields(lines[2])
	assert.Equal(t, "-", fields[len(fields)-1])
}

func TestPrintVersionNames(t *testing.T) {
	versions := []fv1.FunctionVersion{
		{Name: "hello-v1", Spec: fv1.FunctionVersionSpec{Sequence: 1}},
		{Name: "hello-v2", Spec: fv1.FunctionVersionSpec{Sequence: 2}},
	}
	var buf bytes.Buffer
	require.NoError(t, printVersionNames(&buf, versions))
	// Exact bytes: newest (last in the already-sorted slice) prints last, no
	// extra columns, no header.
	assert.Equal(t, "hello-v1\nhello-v2\n", buf.String())
}

// TestVersionsCommandOutputName is the command-level end-to-end for `fn
// versions -o name`: List() -> sortedBySequence() -> printVersionNames(),
// bypassing the table/ALIASED-BY/ENVDRIFT machinery entirely.
func TestVersionsCommandOutputName(t *testing.T) {
	mkVersion := func(name string, seq int64) *fv1.FunctionVersion {
		return &fv1.FunctionVersion{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{fv1.VersionFunctionNameLabel: "hello"},
			Spec:      fv1.FunctionVersionSpec{FunctionName: "hello", Sequence: seq},
		}
	}

	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(mkVersion("hello-v2", 2), mkVersion("hello-v1", 1)),
		Namespace:        "default",
	})

	in := dummy.TestFlagSet()
	in.SetString(flagkey.FnName, "hello")
	in.SetString(flagkey.Output, "name")
	out := captureStdout(t, func() error { return Versions(in) })

	assert.Equal(t, "hello-v1\nhello-v2\n", out)
}

// TestVersionsCommandAliasedByColumn is the command-level end-to-end for the
// ALIASED-BY column: Versions() lists FunctionAliases, joins them via
// aliasedByColumn, and printVersionsList renders the DEFAULT table with it.
func TestVersionsCommandAliasedByColumn(t *testing.T) {
	v1 := &fv1.FunctionVersion{
		Name: "hello-v1", Namespace: "default", Labels: map[string]string{fv1.VersionFunctionNameLabel: "hello"},
		Spec: fv1.FunctionVersionSpec{FunctionName: "hello", Sequence: 1},
	}
	v2 := &fv1.FunctionVersion{
		Name: "hello-v2", Namespace: "default", Labels: map[string]string{fv1.VersionFunctionNameLabel: "hello"},
		Spec: fv1.FunctionVersionSpec{FunctionName: "hello", Sequence: 2},
	}
	prod := &fv1.FunctionAlias{
		Name: "prod", Namespace: "default",
		Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v2"},
	}
	canary := &fv1.FunctionAlias{
		Name: "canary", Namespace: "default",
		Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v2"},
	}
	// An alias for a different function must not leak into hello's column.
	otherAlias := &fv1.FunctionAlias{
		Name: "other-prod", Namespace: "default",
		Spec: fv1.FunctionAliasSpec{FunctionName: "other", Version: "other-v1"},
	}

	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{
		FissionClientSet: fissionfake.NewClientset(v1, v2, prod, canary, otherAlias),
		Namespace:        "default",
	})

	in := dummy.TestFlagSet()
	in.SetString(flagkey.FnName, "hello")
	out := captureStdout(t, func() error { return Versions(in) })

	assert.Contains(t, out, "canary,prod")
	assert.False(t, strings.Contains(out, "other-prod"), "an alias belonging to a different function must not appear:\n%s", out)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 3)
	v1Fields := strings.Fields(lines[1])
	assert.Equal(t, unaliasedCell, v1Fields[len(v1Fields)-2], "hello-v1 has no aliases: %q", lines[1])
}

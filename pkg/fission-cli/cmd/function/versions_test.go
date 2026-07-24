// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"encoding/json"
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
		{ObjectMeta: metav1.ObjectMeta{Name: "v3"}, Spec: fv1.FunctionVersionSpec{Sequence: 3}},
		{ObjectMeta: metav1.ObjectMeta{Name: "v1"}, Spec: fv1.FunctionVersionSpec{Sequence: 1}},
		{ObjectMeta: metav1.ObjectMeta{Name: "v2"}, Spec: fv1.FunctionVersionSpec{Sequence: 2}},
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
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1"},
		Spec:       fv1.FunctionVersionSpec{Sequence: 1, PackageDigest: longDigest},
	}}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputTable, nil) })
	assert.Contains(t, out, "hello-v1")
	assert.Contains(t, out, truncateDigest(longDigest))
	assert.False(t, strings.Contains(out, longDigest), "table output should not contain the full digest:\n%s", out)
}

func TestPrintVersionsListWideKeepsFullDigest(t *testing.T) {
	longDigest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef"
	versions := []fv1.FunctionVersion{{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1"},
		Spec:       fv1.FunctionVersionSpec{Sequence: 1, PackageDigest: longDigest},
	}}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputWide, nil) })
	assert.Contains(t, out, longDigest)
}

func TestPrintVersionsListJSON(t *testing.T) {
	longDigest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef"
	versions := []fv1.FunctionVersion{{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1"},
		Spec:       fv1.FunctionVersionSpec{Sequence: 1, PackageDigest: longDigest},
	}}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputJSON, nil) })
	var got []fv1.FunctionVersion
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got, 1)
	assert.Equal(t, longDigest, got[0].Spec.PackageDigest, "json must carry the full, untruncated digest")
}

func TestPrintVersionsListWideAddsEnvDriftColumn(t *testing.T) {
	versions := []fv1.FunctionVersion{{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1"},
		Spec:       fv1.FunctionVersionSpec{Sequence: 1},
	}}
	drift := map[string]string{"hello-v1": "True"}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputWide, drift) })
	assert.Contains(t, out, "ENVDRIFT")
	assert.Contains(t, out, "True")
}

func TestPrintVersionsListWideDriftMissingFallsBackToNone(t *testing.T) {
	versions := []fv1.FunctionVersion{{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1"},
		Spec:       fv1.FunctionVersionSpec{Sequence: 1},
	}}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputWide, nil) })
	assert.Contains(t, out, util.NoneValue)
}

func TestPrintVersionsListTableOmitsEnvDriftColumn(t *testing.T) {
	versions := []fv1.FunctionVersion{{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1"},
		Spec:       fv1.FunctionVersionSpec{Sequence: 1},
	}}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputTable, nil) })
	assert.False(t, strings.Contains(out, "ENVDRIFT"), "the plain table format must not gain the wide-only column:\n%s", out)
}

func TestEnvDriftByVersionDetectsDrift(t *testing.T) {
	cmd.ResetClientsetForTest()
	env := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "nodejs", Namespace: "default", Generation: 2},
	}
	fc := fissionfake.NewClientset(env)
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

	versions := []fv1.FunctionVersion{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "hello-v1"},
			Spec: fv1.FunctionVersionSpec{
				Sequence:              1,
				EnvObservedGeneration: 1, // stale vs live generation 2
				Snapshot:              fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "nodejs"}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "hello-v2"},
			Spec: fv1.FunctionVersionSpec{
				Sequence:              2,
				EnvObservedGeneration: 2, // current
				Snapshot:              fv1.FunctionSpec{Environment: fv1.EnvironmentReference{Name: "nodejs"}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "hello-v3"},
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
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1"},
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
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels:    map[string]string{fv1.VersionFunctionNameLabel: fn},
			},
			Spec: fv1.FunctionVersionSpec{FunctionName: fn, Sequence: seq},
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
	in.Set(flagkey.FnName, "hello")
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
		ObjectMeta: metav1.ObjectMeta{Name: "nodejs", Namespace: "default", Generation: 2},
	}
	v := &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hello-v1",
			Namespace: "default",
			Labels:    map[string]string{fv1.VersionFunctionNameLabel: "hello"},
		},
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
	in.Set(flagkey.FnName, "hello")
	in.Set(flagkey.Output, "wide")
	out := captureStdout(t, func() error { return Versions(in) })

	assert.Contains(t, out, "ENVDRIFT")
	assert.Contains(t, out, "hello-v1")
	assert.Contains(t, out, "True")
}

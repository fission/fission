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

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputTable) })
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

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputWide) })
	assert.Contains(t, out, longDigest)
}

func TestPrintVersionsListJSON(t *testing.T) {
	longDigest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef"
	versions := []fv1.FunctionVersion{{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-v1"},
		Spec:       fv1.FunctionVersionSpec{Sequence: 1, PackageDigest: longDigest},
	}}

	out := captureStdout(t, func() error { return printVersionsList(versions, util.OutputJSON) })
	var got []fv1.FunctionVersion
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got, 1)
	assert.Equal(t, longDigest, got[0].Spec.PackageDigest, "json must carry the full, untruncated digest")
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

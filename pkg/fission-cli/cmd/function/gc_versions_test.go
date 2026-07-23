// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/versioning"
)

func gcVersion(fnName string, seq int64) *fv1.FunctionVersion {
	return &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-v%d", fnName, seq),
			Namespace: "default",
			Labels:    map[string]string{fv1.VersionFunctionNameLabel: fnName},
		},
		Spec: fv1.FunctionVersionSpec{FunctionName: fnName, Sequence: seq},
	}
}

func setGCVersionsClient(objs ...runtime.Object) *fissionfake.Clientset {
	fc := fissionfake.NewSimpleClientset(objs...) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})
	return fc
}

func gcVersionsFlags(fnName string, keep int, keepSet bool) dummy.Cli {
	in := dummy.TestFlagSet()
	in.Set(flagkey.FnName, fnName)
	if keepSet {
		in.Set(flagkey.GCVersionsKeep, keep)
	}
	return in
}

// TestGCVersionsCommand_KeepOverride drives the CLI end to end: an explicit
// --keep overrides the function's own Spec.Versioning.Retain (2 here), and
// the command's output reports the resulting counts.
func TestGCVersionsCommand_KeepOverride(t *testing.T) {
	retain := 2
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"},
		Spec: fv1.FunctionSpec{
			Versioning: &fv1.VersioningConfig{Retain: &retain},
		},
	}
	objs := []runtime.Object{fn}
	for i := int64(1); i <= 5; i++ {
		objs = append(objs, gcVersion("hello", i))
	}
	fc := setGCVersionsClient(objs...)

	// --keep 1 overrides the function's Retain: 2.
	in := gcVersionsFlags("hello", 1, true)
	out := captureStdout(t, func() error { return GCVersions(in) })

	assert.Contains(t, out, "deleted 4")
	assert.Contains(t, out, "retained 1")

	list, err := fc.CoreV1().FunctionVersions("default").List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "hello-v5", list.Items[0].Name)
}

// TestGCVersionsCommand_DefaultsToFunctionRetain: without --keep, the
// command reads the function's Spec.Versioning.Retain (here, 3).
func TestGCVersionsCommand_DefaultsToFunctionRetain(t *testing.T) {
	retain := 3
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"},
		Spec: fv1.FunctionSpec{
			Versioning: &fv1.VersioningConfig{Retain: &retain},
		},
	}
	objs := []runtime.Object{fn}
	for i := int64(1); i <= 5; i++ {
		objs = append(objs, gcVersion("hello", i))
	}
	fc := setGCVersionsClient(objs...)

	in := gcVersionsFlags("hello", 0, false)
	out := captureStdout(t, func() error { return GCVersions(in) })

	assert.Contains(t, out, "deleted 2")
	assert.Contains(t, out, "retained 3")

	list, err := fc.CoreV1().FunctionVersions("default").List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, list.Items, 3)
}

// TestGCVersionsCommand_NoVersioningConfigDefaultsToPackageDefault: a
// function with no Spec.Versioning at all still falls back to
// versioning.DefaultRetain for the on-demand CLI sweep (unlike the
// reconciler, which skips such functions entirely -- the CLI is an explicit
// operator action, not the automatic controller).
func TestGCVersionsCommand_NoVersioningConfigDefaultsToPackageDefault(t *testing.T) {
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"},
	}
	objs := []runtime.Object{fn}
	for i := int64(1); i <= int64(versioning.DefaultRetain)+2; i++ {
		objs = append(objs, gcVersion("hello", i))
	}
	fc := setGCVersionsClient(objs...)

	in := gcVersionsFlags("hello", 0, false)
	out := captureStdout(t, func() error { return GCVersions(in) })

	assert.Contains(t, out, "deleted 2")

	list, err := fc.CoreV1().FunctionVersions("default").List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, list.Items, versioning.DefaultRetain)
}

// TestGCVersionsCommand_InvalidKeepRejected: --keep 0 (or negative) is
// rejected before any sweep runs -- SweepVersions would silently floor it to
// 1, but the CLI should surface the mistake instead.
func TestGCVersionsCommand_InvalidKeepRejected(t *testing.T) {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"}}
	setGCVersionsClient(fn)

	in := gcVersionsFlags("hello", 0, true)
	err := GCVersions(in)
	assert.Error(t, err)
}

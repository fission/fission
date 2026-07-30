// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// TestListPodsVersionColumn guards the always-shown RFC-0025 VERSION column:
// a pod carrying the fission.io/function-version label renders it, and a pod
// without one (unversioned function, or specialized before --versioning was
// enabled) renders util.NoneValue -- not an empty cell.
func TestListPodsVersionColumn(t *testing.T) {
	fn := describeFunction()
	versioned := describeFunctionPod()
	versioned.Name = "poolmgr-nodejs-hello-v1"
	versioned.Labels[fv1.FUNCTION_VERSION] = "hello-v1"
	unversioned := describeFunctionPod()
	unversioned.Name = "poolmgr-nodejs-hello-live"

	setDescribeClients(t, []runtime.Object{fn}, versioned, unversioned)

	out := captureStdout(t, func() error { return ListPods(describeInput("hello", "default")) })

	assert.Contains(t, out, "VERSION", "pods table has a VERSION column")
	assert.Contains(t, out, "poolmgr-nodejs-hello-v1")
	assert.Contains(t, out, "hello-v1", "versioned pod shows its FunctionVersion name")
	assert.Contains(t, out, "poolmgr-nodejs-hello-live")
	assert.Contains(t, out, "-", "unversioned pod renders the none placeholder")
}

// TestListPodsVersionFilter guards --version: it must both preflight-check
// the named FunctionVersion (ownership) and narrow the pod list selector to
// that version's label.
func TestListPodsVersionFilter(t *testing.T) {
	t.Run("filters to the pinned version's pods", func(t *testing.T) {
		fn := describeFunction()
		version := &fv1.FunctionVersion{
			ObjectMeta: metav1.ObjectMeta{Name: "hello-v1", Namespace: "default"},
			Spec:       fv1.FunctionVersionSpec{FunctionName: "hello"},
		}
		matching := describeFunctionPod()
		matching.Name = "poolmgr-nodejs-hello-v1"
		matching.Labels[fv1.FUNCTION_VERSION] = "hello-v1"
		other := describeFunctionPod()
		other.Name = "poolmgr-nodejs-hello-v2"
		other.Labels[fv1.FUNCTION_VERSION] = "hello-v2"

		setDescribeClients(t, []runtime.Object{fn, version}, matching, other)

		in := describeInput("hello", "default")
		in.Set(flagkey.FnTestVersion, "hello-v1")

		out := captureStdout(t, func() error { return ListPods(in) })

		assert.Contains(t, out, "poolmgr-nodejs-hello-v1")
		assert.NotContains(t, out, "poolmgr-nodejs-hello-v2")
	})

	t.Run("a version owned by another function is rejected", func(t *testing.T) {
		fn := describeFunction()
		version := &fv1.FunctionVersion{
			ObjectMeta: metav1.ObjectMeta{Name: "other-v1", Namespace: "default"},
			Spec:       fv1.FunctionVersionSpec{FunctionName: "other-fn"},
		}
		setDescribeClients(t, []runtime.Object{fn, version})

		in := describeInput("hello", "default")
		in.Set(flagkey.FnTestVersion, "other-v1")

		err := ListPods(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `targets function "other-fn", not "hello"`)
	})
}

// TestListPodsAliasFilter guards --alias: it resolves the alias's *effective*
// target (Spec.Version when name-pinned, else Status.ResolvedVersion for a
// PackageDigest-pinned alias) rather than handing the alias name straight to
// the pod selector, since pods carry the FunctionVersion label, not an alias
// label.
func TestListPodsAliasFilter(t *testing.T) {
	t.Run("name-pinned alias filters by Spec.Version", func(t *testing.T) {
		fn := describeFunction()
		alias := &fv1.FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
		}
		matching := describeFunctionPod()
		matching.Name = "poolmgr-nodejs-hello-v1"
		matching.Labels[fv1.FUNCTION_VERSION] = "hello-v1"
		other := describeFunctionPod()
		other.Name = "poolmgr-nodejs-hello-v2"
		other.Labels[fv1.FUNCTION_VERSION] = "hello-v2"

		setDescribeClients(t, []runtime.Object{fn, alias}, matching, other)

		in := describeInput("hello", "default")
		in.Set(flagkey.FnTestAlias, "prod")

		out := captureStdout(t, func() error { return ListPods(in) })

		assert.Contains(t, out, "poolmgr-nodejs-hello-v1")
		assert.NotContains(t, out, "poolmgr-nodejs-hello-v2")
	})

	t.Run("digest-pinned alias falls back to Status.ResolvedVersion", func(t *testing.T) {
		fn := describeFunction()
		alias := &fv1.FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", PackageDigest: "sha256:" + repeat64("a")},
			Status:     fv1.FunctionAliasStatus{ResolvedVersion: "hello-v3"},
		}
		matching := describeFunctionPod()
		matching.Name = "poolmgr-nodejs-hello-v3"
		matching.Labels[fv1.FUNCTION_VERSION] = "hello-v3"

		setDescribeClients(t, []runtime.Object{fn, alias}, matching)

		in := describeInput("hello", "default")
		in.Set(flagkey.FnTestAlias, "prod")

		out := captureStdout(t, func() error { return ListPods(in) })

		assert.Contains(t, out, "poolmgr-nodejs-hello-v3")
	})

	t.Run("an unresolved alias is rejected", func(t *testing.T) {
		fn := describeFunction()
		alias := &fv1.FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", PackageDigest: "sha256:" + repeat64("a")},
		}
		setDescribeClients(t, []runtime.Object{fn, alias})

		in := describeInput("hello", "default")
		in.Set(flagkey.FnTestAlias, "prod")

		err := ListPods(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `alias "prod" has not resolved to a version yet`)
	})

	t.Run("an alias owned by another function is rejected", func(t *testing.T) {
		fn := describeFunction()
		alias := &fv1.FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "other-fn", Version: "other-v1"},
		}
		setDescribeClients(t, []runtime.Object{fn, alias})

		in := describeInput("hello", "default")
		in.Set(flagkey.FnTestAlias, "prod")

		err := ListPods(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `targets function "other-fn", not "hello"`)
	})
}

// TestListPodsAliasVersionMutuallyExclusive guards the --alias/--version
// mutual exclusion, checked before either object is looked up.
func TestListPodsAliasVersionMutuallyExclusive(t *testing.T) {
	fn := describeFunction()
	setDescribeClients(t, []runtime.Object{fn})

	in := describeInput("hello", "default")
	in.Set(flagkey.FnTestAlias, "prod")
	in.Set(flagkey.FnTestVersion, "hello-v1")

	err := ListPods(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// TestListPodsNeitherAliasNorVersion guards the common path: no filter flags
// set, no version-resolution API calls, existing behavior unchanged.
func TestListPodsNeitherAliasNorVersion(t *testing.T) {
	fn := describeFunction()
	setDescribeClients(t, []runtime.Object{fn}, describeFunctionPod())

	out := captureStdout(t, func() error { return ListPods(describeInput("hello", "default")) })
	assert.Contains(t, out, "poolmgr-nodejs-hello-abc")
}

func repeat64(s string) string {
	out := make([]byte, 0, 64)
	for len(out) < 64 {
		out = append(out, s...)
	}
	return string(out[:64])
}

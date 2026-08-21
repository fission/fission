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
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func setDeleteClient(objs ...runtime.Object) *fissionfake.Clientset {
	fc := fissionfake.NewSimpleClientset(objs...) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})
	return fc
}

func deleteFlags(fnName string) dummy.Cli {
	in := dummy.TestFlagSet()
	in.SetString(flagkey.FnName, fnName)
	return in
}

// TestDeleteCommand_Unversioned covers the common, unversioned function: no
// FunctionVersions, no FunctionAliases -- the preflight must produce no
// output at all, byte-identical to the command's behavior before the
// cascade warning existed.
func TestDeleteCommand_Unversioned(t *testing.T) {
	fn := &fv1.Function{Name: "hello", Namespace: "default"}
	setDeleteClient(fn)

	out := captureStdout(t, func() error { return Delete(deleteFlags("hello")) })

	assert.Equal(t, "function 'hello' deleted\n", out)
}

// TestDeleteCommand_VersionsOnly: a function with FunctionVersions but no
// aliases warns about the version count, singular grammar included, and
// mentions no aliases or triggers.
func TestDeleteCommand_VersionsOnly(t *testing.T) {
	fn := &fv1.Function{Name: "hello", Namespace: "default"}
	v1 := gcVersion("hello", 1)
	v2 := gcVersion("hello", 2)
	setDeleteClient(fn, v1, v2)

	out := captureStdout(t, func() error { return Delete(deleteFlags("hello")) })

	assert.Contains(t, out, "warning: deleting function 'hello' also deletes 2 versions")
	assert.NotContains(t, out, "alias")
	assert.Contains(t, out, "function 'hello' deleted")
}

// TestDeleteCommand_SingleVersionSingularGrammar exercises the "1 version"
// singular phrasing, not "1 versions".
func TestDeleteCommand_SingleVersionSingularGrammar(t *testing.T) {
	fn := &fv1.Function{Name: "hello", Namespace: "default"}
	setDeleteClient(fn, gcVersion("hello", 1))

	out := captureStdout(t, func() error { return Delete(deleteFlags("hello")) })

	assert.Contains(t, out, "warning: deleting function 'hello' also deletes 1 version")
	assert.NotContains(t, out, "1 versions")
}

// TestDeleteCommand_VersionsAndAliases covers versions + aliases together,
// with alias names sorted regardless of creation order, and no
// HTTPTriggers referencing them.
func TestDeleteCommand_VersionsAndAliases(t *testing.T) {
	fn := &fv1.Function{Name: "hello", Namespace: "default"}
	v1 := gcVersion("hello", 1)
	devAlias := &fv1.FunctionAlias{
		Name: "dev", Namespace: "default",
		Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
	}
	prodAlias := &fv1.FunctionAlias{
		Name: "prod", Namespace: "default",
		Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
	}
	// Unrelated alias for a different function must not be counted.
	other := &fv1.FunctionAlias{
		Name: "other", Namespace: "default",
		Spec: fv1.FunctionAliasSpec{FunctionName: "not-hello", Version: "not-hello-v1"},
	}
	setDeleteClient(fn, v1, devAlias, prodAlias, other)

	out := captureStdout(t, func() error { return Delete(deleteFlags("hello")) })

	assert.Contains(t, out, "warning: deleting function 'hello' also deletes 1 version and 2 aliases (dev, prod)")
	assert.NotContains(t, out, "HTTPTriggers")
}

// TestDeleteCommand_AliasesWithReferencingTriggers covers the full cascade:
// versions, aliases, and HTTPTriggers whose functionref.alias points at one
// of those aliases -- those triggers must be named in the warning. A
// trigger referencing an unrelated alias, and a trigger referencing the
// function by name (not alias), must not appear.
func TestDeleteCommand_AliasesWithReferencingTriggers(t *testing.T) {
	fn := &fv1.Function{Name: "hello", Namespace: "default"}
	prodAlias := &fv1.FunctionAlias{
		Name: "prod", Namespace: "default",
		Spec: fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
	}
	unrelatedAlias := &fv1.FunctionAlias{
		Name: "staging", Namespace: "default",
		Spec: fv1.FunctionAliasSpec{FunctionName: "other-fn", Version: "other-fn-v1"},
	}

	trigZ := &fv1.HTTPTrigger{
		Name: "trig-z", Namespace: "default",
		Spec: fv1.HTTPTriggerSpec{
			FunctionReference: fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Alias: "prod"},
		},
	}
	trigA := &fv1.HTTPTrigger{
		Name: "trig-a", Namespace: "default",
		Spec: fv1.HTTPTriggerSpec{
			FunctionReference: fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Alias: "prod"},
		},
	}
	trigOtherAlias := &fv1.HTTPTrigger{
		Name: "trig-other-alias", Namespace: "default",
		Spec: fv1.HTTPTriggerSpec{
			FunctionReference: fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Alias: "staging"},
		},
	}
	trigByName := &fv1.HTTPTrigger{
		Name: "trig-by-name", Namespace: "default",
		Spec: fv1.HTTPTriggerSpec{
			FunctionReference: fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello"},
		},
	}

	setDeleteClient(fn, prodAlias, unrelatedAlias, trigZ, trigA, trigOtherAlias, trigByName)

	out := captureStdout(t, func() error { return Delete(deleteFlags("hello")) })

	assert.Contains(t, out, "warning: deleting function 'hello' also deletes 1 alias (prod)")
	assert.Contains(t, out, "HTTPTriggers [trig-a, trig-z] reference these aliases and will stop resolving")
	assert.NotContains(t, out, "trig-other-alias")
	assert.NotContains(t, out, "trig-by-name")
}

// TestDeleteCommand_PreflightListErrorDoesNotBlockDelete: if the version
// preflight List errors (RBAC denial, API hiccup, etc.), the delete must
// still succeed with no warning and no failure -- the preflight is
// advisory only.
func TestDeleteCommand_PreflightListErrorDoesNotBlockDelete(t *testing.T) {
	fn := &fv1.Function{Name: "hello", Namespace: "default"}
	fc := setDeleteClient(fn)
	fc.PrependReactor("list", "functionversions", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, assert.AnError
	})
	fc.PrependReactor("list", "functionaliases", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, assert.AnError
	})
	fc.PrependReactor("list", "httptriggers", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, assert.AnError
	})

	out := captureStdout(t, func() error { return Delete(deleteFlags("hello")) })

	assert.Equal(t, "function 'hello' deleted\n", out)

	_, err := fc.CoreV1().Functions("default").Get(t.Context(), "hello", metav1.GetOptions{})
	require.Error(t, err, "function should have been deleted")
}

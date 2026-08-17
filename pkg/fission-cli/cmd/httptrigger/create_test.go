// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httptrigger

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// TestCreateCompleteFnRefTagFlags exercises the RFC-0025
// --function-alias/--function-version wiring through CreateSubCommand.complete:
// flag gating (mutual exclusion, weighted multi-function rejection), the
// ownership preflight, and that a valid tag lands on the built
// FunctionReference.
//
// Not run in parallel: cmd.SetClientset installs a package-level client
// shared by every test in this package.
func TestCreateCompleteFnRefTagFlags(t *testing.T) {
	t.Run("alias and version together errors before any API call", func(t *testing.T) {
		fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetStringSlice(flagkey.HtFnName, []string{"fn"})
		in.SetString(flagkey.HtUrl, "/test")
		in.SetString(flagkey.HtFnAlias, "prod")
		in.SetString(flagkey.HtFnVersion, "fn-v1")

		err := (&CreateSubCommand{}).complete(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})

	t.Run("alias with weighted multi-function reference errors", func(t *testing.T) {
		fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetStringSlice(flagkey.HtFnName, []string{"fn1", "fn2"})
		in.SetIntSlice(flagkey.HtFnWeight, []int{50, 50})
		in.SetString(flagkey.HtUrl, "/test")
		in.SetString(flagkey.HtFnAlias, "prod")

		err := (&CreateSubCommand{}).complete(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "require exactly one --function")
	})

	t.Run("alias preflight rejects a missing alias", func(t *testing.T) {
		fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetStringSlice(flagkey.HtFnName, []string{"fn"})
		in.SetString(flagkey.HtUrl, "/test")
		in.SetString(flagkey.HtFnAlias, "prod")

		err := (&CreateSubCommand{}).complete(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `alias "prod" not found for function "fn"`)
	})

	t.Run("version preflight rejects a version owned by another function", func(t *testing.T) {
		version := &fv1.FunctionVersion{
			ObjectMeta: metav1.ObjectMeta{Name: "fn-v1", Namespace: "default"},
			Spec:       fv1.FunctionVersionSpec{FunctionName: "other-fn"},
		}
		fc := fissionfake.NewSimpleClientset(version) //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetStringSlice(flagkey.HtFnName, []string{"fn"})
		in.SetString(flagkey.HtUrl, "/test")
		in.SetString(flagkey.HtFnVersion, "fn-v1")

		err := (&CreateSubCommand{}).complete(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `targets function "other-fn", not "fn"`)
	})

	t.Run("valid alias lands on the built FunctionReference", func(t *testing.T) {
		alias := &fv1.FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "fn", Version: "fn-v1"},
		}
		fc := fissionfake.NewSimpleClientset(alias) //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetStringSlice(flagkey.HtFnName, []string{"fn"})
		in.SetString(flagkey.HtUrl, "/test")
		in.SetString(flagkey.HtFnAlias, "prod")

		opts := &CreateSubCommand{}
		err := opts.complete(in)
		require.NoError(t, err)
		require.NotNil(t, opts.trigger)
		assert.EqualValues(t, fv1.FunctionReferenceTypeFunctionName, opts.trigger.Spec.FunctionReference.Type)
		assert.Equal(t, "fn", opts.trigger.Spec.FunctionReference.Name)
		assert.Equal(t, "prod", opts.trigger.Spec.FunctionReference.Alias)
		assert.Empty(t, opts.trigger.Spec.FunctionReference.Version)
	})

	t.Run("valid version lands on the built FunctionReference", func(t *testing.T) {
		version := &fv1.FunctionVersion{
			ObjectMeta: metav1.ObjectMeta{Name: "fn-v3", Namespace: "default"},
			Spec:       fv1.FunctionVersionSpec{FunctionName: "fn"},
		}
		fc := fissionfake.NewSimpleClientset(version) //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetStringSlice(flagkey.HtFnName, []string{"fn"})
		in.SetString(flagkey.HtUrl, "/test")
		in.SetString(flagkey.HtFnVersion, "fn-v3")

		opts := &CreateSubCommand{}
		err := opts.complete(in)
		require.NoError(t, err)
		require.NotNil(t, opts.trigger)
		assert.Equal(t, "fn-v3", opts.trigger.Spec.FunctionReference.Version)
		assert.Empty(t, opts.trigger.Spec.FunctionReference.Alias)
	})

	t.Run("no tag flags leaves the reference untouched", func(t *testing.T) {
		fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetStringSlice(flagkey.HtFnName, []string{"fn"})
		in.SetString(flagkey.HtUrl, "/test")

		opts := &CreateSubCommand{}
		err := opts.complete(in)
		require.NoError(t, err)
		require.NotNil(t, opts.trigger)
		assert.Empty(t, opts.trigger.Spec.FunctionReference.Alias)
		assert.Empty(t, opts.trigger.Spec.FunctionReference.Version)
	})
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httptrigger

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func existingRoute(name string) *fv1.HTTPTrigger {
	prefix := ""
	return &fv1.HTTPTrigger{
		Name: name, Namespace: "default",
		Spec: fv1.HTTPTriggerSpec{
			RelativeURL: "/test",
			Prefix:      &prefix,
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "fn",
			},
		},
	}
}

// TestUpdateCompleteFnRefTagFlags mirrors TestCreateCompleteFnRefTagFlags for
// UpdateSubCommand.complete: flag gating, the ownership preflight, and the
// update-specific rule that --function-alias/--function-version require
// --function in the SAME invocation (route update never infers the target
// function from the existing route).
//
// Not run in parallel: cmd.SetClientset installs a package-level client
// shared by every test in this package.
func TestUpdateCompleteFnRefTagFlags(t *testing.T) {
	t.Run("alias without --function errors", func(t *testing.T) {
		fc := fissionfake.NewSimpleClientset(existingRoute("r1")) //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetString(flagkey.HtName, "r1")
		in.SetString(flagkey.HtFnAlias, "prod")

		err := (&UpdateSubCommand{}).complete(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "require --function to be set")
	})

	t.Run("version without --function errors", func(t *testing.T) {
		fc := fissionfake.NewSimpleClientset(existingRoute("r1")) //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetString(flagkey.HtName, "r1")
		in.SetString(flagkey.HtFnVersion, "fn-v1")

		err := (&UpdateSubCommand{}).complete(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "require --function to be set")
	})

	t.Run("alias and version together errors", func(t *testing.T) {
		fc := fissionfake.NewSimpleClientset(existingRoute("r1")) //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetString(flagkey.HtName, "r1")
		in.SetStringSlice(flagkey.HtFnName, []string{"fn"})
		in.SetString(flagkey.HtFnAlias, "prod")
		in.SetString(flagkey.HtFnVersion, "fn-v1")

		err := (&UpdateSubCommand{}).complete(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})

	t.Run("alias with weighted multi-function reference errors", func(t *testing.T) {
		fc := fissionfake.NewSimpleClientset(existingRoute("r1")) //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetString(flagkey.HtName, "r1")
		in.SetStringSlice(flagkey.HtFnName, []string{"fn1", "fn2"})
		in.SetIntSlice(flagkey.HtFnWeight, []int{50, 50})
		in.SetString(flagkey.HtFnAlias, "prod")

		err := (&UpdateSubCommand{}).complete(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "require exactly one --function")
	})

	t.Run("alias preflight rejects a missing alias", func(t *testing.T) {
		fc := fissionfake.NewSimpleClientset(existingRoute("r1")) //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetString(flagkey.HtName, "r1")
		in.SetStringSlice(flagkey.HtFnName, []string{"fn"})
		in.SetString(flagkey.HtFnAlias, "prod")

		err := (&UpdateSubCommand{}).complete(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `alias "prod" not found for function "fn"`)
	})

	t.Run("valid alias lands on the updated FunctionReference", func(t *testing.T) {
		alias := &fv1.FunctionAlias{
			Name: "prod", Namespace: "default",
			Spec: fv1.FunctionAliasSpec{FunctionName: "fn", Version: "fn-v1"},
		}
		fc := fissionfake.NewSimpleClientset(existingRoute("r1"), alias) //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetString(flagkey.HtName, "r1")
		in.SetStringSlice(flagkey.HtFnName, []string{"fn"})
		in.SetString(flagkey.HtFnAlias, "prod")

		opts := &UpdateSubCommand{}
		err := opts.complete(in)
		require.NoError(t, err)
		require.NotNil(t, opts.trigger)
		assert.Equal(t, "fn", opts.trigger.Spec.FunctionReference.Name)
		assert.Equal(t, "prod", opts.trigger.Spec.FunctionReference.Alias)
		assert.Empty(t, opts.trigger.Spec.FunctionReference.Version)
	})

	t.Run("re-setting --function without a tag clears a previous alias pin", func(t *testing.T) {
		route := existingRoute("r1")
		route.Spec.FunctionReference.Alias = "prod"
		fc := fissionfake.NewSimpleClientset(route) //nolint:staticcheck
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		in := dummy.TestFlagSet()
		in.SetString(flagkey.HtName, "r1")
		in.SetStringSlice(flagkey.HtFnName, []string{"fn"})

		opts := &UpdateSubCommand{}
		err := opts.complete(in)
		require.NoError(t, err)
		require.NotNil(t, opts.trigger)
		assert.Empty(t, opts.trigger.Spec.FunctionReference.Alias)
	})
}

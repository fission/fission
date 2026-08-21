// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httptrigger

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// TestValidateFnRefTagFlags guards the RFC-0025 --function-alias/
// --function-version syntactic gate shared by `route create` and
// `route update`: mutually exclusive, and either one requires exactly one
// --function (no weighted multi-function reference).
func TestValidateFnRefTagFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		alias        string
		version      string
		functionList []string
		wantErr      string
	}{
		{
			name:         "neither set is fine with any function count",
			functionList: []string{"fn1", "fn2"},
		},
		{
			name:         "alias alone with one function is fine",
			alias:        "prod",
			functionList: []string{"fn1"},
		},
		{
			name:         "version alone with one function is fine",
			version:      "fn1-v1",
			functionList: []string{"fn1"},
		},
		{
			name:         "both alias and version set errors",
			alias:        "prod",
			version:      "fn1-v1",
			functionList: []string{"fn1"},
			wantErr:      "mutually exclusive",
		},
		{
			name:         "alias with two weighted functions errors",
			alias:        "prod",
			functionList: []string{"fn1", "fn2"},
			wantErr:      "require exactly one --function",
		},
		{
			name:         "version with two weighted functions errors",
			version:      "fn1-v1",
			functionList: []string{"fn1", "fn2"},
			wantErr:      "require exactly one --function",
		},
		{
			name:         "alias with zero functions errors",
			alias:        "prod",
			functionList: nil,
			wantErr:      "require exactly one --function",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateFnRefTagFlags(tt.alias, tt.version, tt.functionList)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestCheckFnRefTagOwnership guards the live preflight ownership check,
// mirroring `fn test`'s resolveTestRefSuffix: a missing alias/version, or one
// that belongs to a different function, must produce a clear error rather
// than silently accepting a mismatched tag.
func TestCheckFnRefTagOwnership(t *testing.T) {
	t.Parallel()

	t.Run("neither set is a no-op", func(t *testing.T) {
		t.Parallel()
		fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
		client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
		err := checkFnRefTagOwnership(t.Context(), client, "default", "fn", "", "")
		require.NoError(t, err)
	})

	t.Run("missing alias errors", func(t *testing.T) {
		t.Parallel()
		fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
		client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
		err := checkFnRefTagOwnership(t.Context(), client, "default", "fn", "prod", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `alias "prod" not found for function "fn"`)
	})

	t.Run("alias targeting a different function errors", func(t *testing.T) {
		t.Parallel()
		alias := &fv1.FunctionAlias{
			Name: "prod", Namespace: "default",
			Spec: fv1.FunctionAliasSpec{FunctionName: "other-fn", Version: "other-fn-v1"},
		}
		fc := fissionfake.NewSimpleClientset(alias) //nolint:staticcheck
		client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
		err := checkFnRefTagOwnership(t.Context(), client, "default", "fn", "prod", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `targets function "other-fn", not "fn"`)
	})

	t.Run("matching alias is accepted", func(t *testing.T) {
		t.Parallel()
		alias := &fv1.FunctionAlias{
			Name: "prod", Namespace: "default",
			Spec: fv1.FunctionAliasSpec{FunctionName: "fn", Version: "fn-v1"},
		}
		fc := fissionfake.NewSimpleClientset(alias) //nolint:staticcheck
		client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
		err := checkFnRefTagOwnership(t.Context(), client, "default", "fn", "prod", "")
		require.NoError(t, err)
	})

	t.Run("missing version errors", func(t *testing.T) {
		t.Parallel()
		fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
		client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
		err := checkFnRefTagOwnership(t.Context(), client, "default", "fn", "", "fn-v3")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `version "fn-v3" not found for function "fn"`)
	})

	t.Run("version targeting a different function errors", func(t *testing.T) {
		t.Parallel()
		version := &fv1.FunctionVersion{
			Name: "fn-v3", Namespace: "default",
			Spec: fv1.FunctionVersionSpec{FunctionName: "other-fn"},
		}
		fc := fissionfake.NewSimpleClientset(version) //nolint:staticcheck
		client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
		err := checkFnRefTagOwnership(t.Context(), client, "default", "fn", "", "fn-v3")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `targets function "other-fn", not "fn"`)
	})

	t.Run("matching version is accepted", func(t *testing.T) {
		t.Parallel()
		version := &fv1.FunctionVersion{
			Name: "fn-v3", Namespace: "default",
			Spec: fv1.FunctionVersionSpec{FunctionName: "fn"},
		}
		fc := fissionfake.NewSimpleClientset(version) //nolint:staticcheck
		client := cmd.Client{FissionClientSet: fc, Namespace: "default"}
		err := checkFnRefTagOwnership(t.Context(), client, "default", "fn", "", "fn-v3")
		require.NoError(t, err)
	})
}

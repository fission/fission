// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// versioningFlags builds a dummy.Cli with only the given keys Set (so
// input.IsSet(...) reports true only for those), mirroring the CLI's real
// flag-parsing behavior for the create/update mutation-gating tests below.
func versioningFlags(t *testing.T, values map[string]any) dummy.Cli {
	t.Helper()
	in := dummy.TestFlagSet()
	for k, v := range values {
		in.Set(k, v)
	}
	return in
}

func TestGetVersioningConfig(t *testing.T) {
	t.Parallel()

	t.Run("neither flag set, no existing (create, opt-in omitted)", func(t *testing.T) {
		t.Parallel()
		in := versioningFlags(t, nil)
		vc, err := getVersioningConfig(in, nil)
		require.NoError(t, err)
		assert.Nil(t, vc)
	})

	t.Run("neither flag set keeps existing untouched", func(t *testing.T) {
		t.Parallel()
		retain := 5
		existing := &fv1.VersioningConfig{Mode: fv1.VersioningModeAuto, Retain: &retain}
		in := versioningFlags(t, nil)
		vc, err := getVersioningConfig(in, existing)
		require.NoError(t, err)
		assert.Same(t, existing, vc)
	})

	t.Run("create with --versioning auto", func(t *testing.T) {
		t.Parallel()
		in := versioningFlags(t, map[string]any{flagkey.FnVersioning: "auto"})
		vc, err := getVersioningConfig(in, nil)
		require.NoError(t, err)
		require.NotNil(t, vc)
		assert.Equal(t, fv1.VersioningModeAuto, vc.Mode)
		assert.Nil(t, vc.Retain)
	})

	t.Run("create with --versioning manual", func(t *testing.T) {
		t.Parallel()
		in := versioningFlags(t, map[string]any{flagkey.FnVersioning: "manual"})
		vc, err := getVersioningConfig(in, nil)
		require.NoError(t, err)
		require.NotNil(t, vc)
		assert.Equal(t, fv1.VersioningModeManual, vc.Mode)
	})

	t.Run("--versioning off on create is a no-op (mirrors omitted)", func(t *testing.T) {
		t.Parallel()
		in := versioningFlags(t, map[string]any{flagkey.FnVersioning: "off"})
		vc, err := getVersioningConfig(in, nil)
		require.NoError(t, err)
		assert.Nil(t, vc)
	})

	t.Run("update flipping auto to manual preserves Retain", func(t *testing.T) {
		t.Parallel()
		retain := 7
		existing := &fv1.VersioningConfig{Mode: fv1.VersioningModeAuto, Retain: &retain}
		in := versioningFlags(t, map[string]any{flagkey.FnVersioning: "manual"})
		vc, err := getVersioningConfig(in, existing)
		require.NoError(t, err)
		require.NotNil(t, vc)
		assert.Equal(t, fv1.VersioningModeManual, vc.Mode)
		require.NotNil(t, vc.Retain)
		assert.Equal(t, 7, *vc.Retain)
		// original must not be mutated
		assert.Equal(t, fv1.VersioningModeAuto, existing.Mode)
	})

	t.Run("update --versioning off clears an existing config", func(t *testing.T) {
		t.Parallel()
		retain := 3
		existing := &fv1.VersioningConfig{Mode: fv1.VersioningModeAuto, Retain: &retain}
		in := versioningFlags(t, map[string]any{flagkey.FnVersioning: "off"})
		vc, err := getVersioningConfig(in, existing)
		require.NoError(t, err)
		assert.Nil(t, vc)
	})

	t.Run("--retain-versions alongside --versioning auto sets Retain on the new config", func(t *testing.T) {
		t.Parallel()
		in := versioningFlags(t, map[string]any{flagkey.FnVersioning: "auto", flagkey.FnRetainVersions: 4})
		vc, err := getVersioningConfig(in, nil)
		require.NoError(t, err)
		require.NotNil(t, vc)
		require.NotNil(t, vc.Retain)
		assert.Equal(t, 4, *vc.Retain)
	})

	t.Run("--retain-versions alone with an existing versioning config sets Retain, keeps Mode", func(t *testing.T) {
		t.Parallel()
		existing := &fv1.VersioningConfig{Mode: fv1.VersioningModeManual}
		in := versioningFlags(t, map[string]any{flagkey.FnRetainVersions: 9})
		vc, err := getVersioningConfig(in, existing)
		require.NoError(t, err)
		require.NotNil(t, vc)
		assert.Equal(t, fv1.VersioningModeManual, vc.Mode)
		require.NotNil(t, vc.Retain)
		assert.Equal(t, 9, *vc.Retain)
	})

	t.Run("--retain-versions alone with no existing versioning config errors", func(t *testing.T) {
		t.Parallel()
		in := versioningFlags(t, map[string]any{flagkey.FnRetainVersions: 4})
		_, err := getVersioningConfig(in, nil)
		require.Error(t, err)
		assert.ErrorContains(t, err, "use --versioning auto|manual with --retain-versions")
	})

	t.Run("--retain-versions with --versioning off errors", func(t *testing.T) {
		t.Parallel()
		in := versioningFlags(t, map[string]any{flagkey.FnVersioning: "off", flagkey.FnRetainVersions: 4})
		_, err := getVersioningConfig(in, nil)
		require.Error(t, err)
		assert.ErrorContains(t, err, "use --versioning auto|manual with --retain-versions")
	})

	t.Run("--retain-versions 0 rejected client-side", func(t *testing.T) {
		t.Parallel()
		in := versioningFlags(t, map[string]any{flagkey.FnVersioning: "auto", flagkey.FnRetainVersions: 0})
		_, err := getVersioningConfig(in, nil)
		require.Error(t, err)
		assert.ErrorContains(t, err, "must be >= 1")
	})

	t.Run("--retain-versions negative rejected client-side", func(t *testing.T) {
		t.Parallel()
		in := versioningFlags(t, map[string]any{flagkey.FnVersioning: "auto", flagkey.FnRetainVersions: -1})
		_, err := getVersioningConfig(in, nil)
		require.Error(t, err)
		assert.ErrorContains(t, err, "must be >= 1")
	})

	t.Run("invalid --versioning value rejected", func(t *testing.T) {
		t.Parallel()
		in := versioningFlags(t, map[string]any{flagkey.FnVersioning: "sometimes"})
		_, err := getVersioningConfig(in, nil)
		require.Error(t, err)
		assert.ErrorContains(t, err, "must be one of auto, manual, off")
	})
}

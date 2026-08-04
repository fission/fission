// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInternalAuthSecretName pins the resolution the fetcher pod-spec builder
// and the storagesvc client both depend on agreeing on — see
// InternalAuthSecretName's doc for why a disagreement fails silently.
func TestInternalAuthSecretName(t *testing.T) {
	t.Run("defaults to the chart-generated name", func(t *testing.T) {
		t.Setenv(InternalAuthSecretNameEnv, "")
		assert.Equal(t, DefaultInternalAuthSecret, InternalAuthSecretName(),
			"an unset or blank override must fall back, not resolve to empty")
	})

	t.Run("an override wins", func(t *testing.T) {
		t.Setenv(InternalAuthSecretNameEnv, "my-preexisting-secret")
		assert.Equal(t, "my-preexisting-secret", InternalAuthSecretName(),
			"internalAuth.existingSecret must reach every component that resolves the master")
	})
}

// TestValidateInternalAuthEnv pins the three-state contract: absent is
// disabled, non-empty is enabled, and present-but-empty must REFUSE — the
// misconfigured-Secret state that would otherwise fail open into
// unauthenticated pass-through.
func TestValidateInternalAuthEnv(t *testing.T) {
	t.Run("absent means disabled and passes", func(t *testing.T) {
		// t.Setenv forbids os.Unsetenv interplay in parallel tests; these
		// subtests run serially and scrub both variables explicitly.
		for _, env := range []string{InternalAuthSecretEnv, InternalAuthSecretOldEnv} {
			t.Setenv(env, "x") // register for restoration
			require.NoError(t, os.Unsetenv(env))
		}
		assert.NoError(t, ValidateInternalAuthEnv())
	})

	t.Run("non-empty master passes", func(t *testing.T) {
		t.Setenv(InternalAuthSecretEnv, "0123456789abcdef0123456789abcdef")
		assert.NoError(t, ValidateInternalAuthEnv())
	})

	t.Run("present-but-empty master is refused", func(t *testing.T) {
		t.Setenv(InternalAuthSecretEnv, "")
		err := ValidateInternalAuthEnv()
		require.Error(t, err, "an empty master must fail closed, never demote to pass-through")
		assert.Contains(t, err.Error(), InternalAuthSecretEnv)
	})

	t.Run("present-but-empty OLD master is refused too", func(t *testing.T) {
		t.Setenv(InternalAuthSecretEnv, "0123456789abcdef0123456789abcdef")
		t.Setenv(InternalAuthSecretOldEnv, "")
		err := ValidateInternalAuthEnv()
		require.Error(t, err, "a rotation misconfiguration must be as loud as a master one")
		assert.Contains(t, err.Error(), InternalAuthSecretOldEnv)
	})
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
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

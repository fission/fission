// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestInternalAuthSecretName pins the resolution the fetcher pod-spec builder
// and the storagesvc client both depend on.
//
// They must agree. A disagreement does not fail loudly: the fetcher's
// secretKeyRef is optional, so the pod starts with the env var simply absent,
// and every archive fetch and builder upload 401s with nothing naming the
// Secret as the cause.
func TestInternalAuthSecretName(t *testing.T) {
	t.Run("defaults to the chart-generated name", func(t *testing.T) {
		t.Setenv(InternalAuthSecretNameEnv, "")
		assert.Equal(t, DefaultInternalAuthSecret, InternalAuthSecretName())
	})

	t.Run("an override wins", func(t *testing.T) {
		t.Setenv(InternalAuthSecretNameEnv, "my-preexisting-secret")
		assert.Equal(t, "my-preexisting-secret", InternalAuthSecretName(),
			"internalAuth.existingSecret must reach every component that resolves the master")
	})

	t.Run("an empty override falls back rather than resolving to nothing", func(t *testing.T) {
		t.Setenv(InternalAuthSecretNameEnv, "")
		assert.NotEmpty(t, InternalAuthSecretName(),
			"an unset or blank override must not yield an empty Secret name")
	})
}

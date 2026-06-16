// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package tenant

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/auth/hmac"
)

func TestNamespaceAuthSecretDerivesNamespaceKeys(t *testing.T) {
	master := []byte("master-secret-bytes-for-test")
	s := NamespaceAuthSecret(master, "team-a")
	require.NotNil(t, s)

	assert.Equal(t, "fission-internal-auth", s.Name)
	assert.Equal(t, "team-a", s.Namespace)
	assert.Equal(t, managedByValue, s.Labels[managedByLabelKey])

	// Keys are the namespace-derived values…
	assert.Equal(t, hmac.DeriveServiceKeyNS(master, hmac.ServiceFetcher, "team-a"), s.Data["fetcherKey"])
	assert.Equal(t, hmac.DeriveServiceKeyNS(master, hmac.ServiceBuilder, "team-a"), s.Data["builderKey"])
	assert.Equal(t, hmac.DeriveServiceKeyNS(master, hmac.ServiceStoragesvc, "team-a"), s.Data["storageKey"])

	// …and the master itself is NEVER written into a tenant namespace.
	assert.NotContains(t, s.Data, "secret", "the master must never land in a tenant secret")
	for field, val := range s.Data {
		assert.NotEqual(t, master, val, "field %q must be a derived key, not the master", field)
	}
}

func TestNamespaceAuthSecretEmptyMaster(t *testing.T) {
	// No master (internalAuth disabled) → nil; the caller skips writing it.
	assert.Nil(t, NamespaceAuthSecret(nil, "team-a"))
	assert.Nil(t, NamespaceAuthSecret([]byte{}, "team-a"))
}

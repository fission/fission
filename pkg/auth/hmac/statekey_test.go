// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveStateKeyspaceKeyDeterministic(t *testing.T) {
	t.Parallel()
	master := []byte("test-master-secret")
	k1 := DeriveStateKeyspaceKey(master, "ns", "cart")
	k2 := DeriveStateKeyspaceKey(master, "ns", "cart")
	require.Len(t, k1, 32)
	assert.Equal(t, k1, k2)
}

// TestDeriveStateKeyspaceKeyIsolation is the S1 distinctness matrix: a token
// derived for one (namespace, keyspace) can never collide with another scope,
// with the plain/NS-scoped service channels, or across masters.
func TestDeriveStateKeyspaceKeyIsolation(t *testing.T) {
	t.Parallel()
	master := []byte("test-master-secret")
	base := DeriveStateKeyspaceKey(master, "ns-a", "cart")

	distinct := [][]byte{
		DeriveStateKeyspaceKey(master, "ns-b", "cart"),          // other namespace
		DeriveStateKeyspaceKey(master, "ns-a", "sessions"),      // other keyspace
		DeriveStateKeyspaceKey([]byte("other"), "ns-a", "cart"), // other master
		DeriveServiceKey(master, ServiceStateAPI),               // admin channel
		DeriveServiceKeyNS(master, ServiceStateAPI, "ns-a"),     // NS channel
		DeriveServiceKeyNS(master, ServiceStatestore, "ns-a"),   // other service NS channel
	}
	for i, k := range distinct {
		assert.NotEqual(t, base, k, "candidate %d must not collide", i)
	}
}

// The keyspace charset (webhook: ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$) and k8s
// namespace rules both exclude ':', so info-string splicing across the
// namespace/keyspace boundary cannot produce equal inputs. This guards the
// construction anyway: swapped fields must derive different keys.
func TestDeriveStateKeyspaceKeyNoFieldSplice(t *testing.T) {
	t.Parallel()
	master := []byte("m")
	assert.NotEqual(t,
		DeriveStateKeyspaceKey(master, "a", "b"),
		DeriveStateKeyspaceKey(master, "b", "a"))
}

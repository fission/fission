// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
	// Blank import registers the "memory" driver. This lives in the external
	// test package so the core statestore package never imports a driver.
	_ "github.com/fission/fission/pkg/statestore/memory"
)

func TestOpenDefaultDriverIsMemory(t *testing.T) {
	caps, err := statestore.Open(t.Context(), statestore.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })

	require.NoError(t, caps.Ping(t.Context()))
	kv, err := caps.KV()
	require.NoError(t, err)
	require.NotNil(t, kv)
}

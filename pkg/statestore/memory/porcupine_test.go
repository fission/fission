// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package memory_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/memory"
	"github.com/fission/fission/pkg/statestore/statestoretest"
)

// K1: the memory KV's concurrent CAS histories are linearizable. This also
// establishes the porcupine harness that Phase 2 runs against real Postgres.
func TestMemoryKV_K1_CASLinearizable_Porcupine(t *testing.T) {
	statestoretest.RunKVLinearizability(t, func(t *testing.T) statestore.Capabilities {
		caps, err := memory.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = caps.Close() })
		return caps
	})
}

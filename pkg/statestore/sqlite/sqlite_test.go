// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/sqlite"
	"github.com/fission/fission/pkg/statestore/statestoretest"
)

// The SQLite driver is a real durable backend, so it must pass the same
// conformance suite as the memory driver (the executable spec) — one suite
// across drivers is what makes "identical across modes" a tested claim. Being
// pure-Go and in-process, it also runs the virtual-time timing suite, which
// verifies the shared sqlstore timing code the Postgres driver reuses.
func TestConformance_SQLite(t *testing.T) {
	factory := func(t *testing.T) statestore.Capabilities {
		caps, err := sqlite.New(t.Context(), t.TempDir()+"/state.db")
		require.NoError(t, err)
		t.Cleanup(func() { _ = caps.Close() })
		return caps
	}
	statestoretest.RunConformance(t, factory)
	statestoretest.RunTimingConformance(t, factory)
}

func TestConformance_SQLite_Linearizability(t *testing.T) {
	statestoretest.RunKVLinearizability(t, func(t *testing.T) statestore.Capabilities {
		caps, err := sqlite.New(t.Context(), t.TempDir()+"/lin.db")
		require.NoError(t, err)
		t.Cleanup(func() { _ = caps.Close() })
		return caps
	})
}

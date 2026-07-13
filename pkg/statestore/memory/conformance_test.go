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

// The memory driver is the executable spec, so it must pass the shared
// conformance suite that every other driver is also held to.
func TestConformance_Memory(t *testing.T) {
	statestoretest.RunConformance(t, func(t *testing.T) statestore.Capabilities {
		caps, err := memory.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = caps.Close() })
		return caps
	})
}

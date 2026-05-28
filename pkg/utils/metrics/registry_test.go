// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeCollectorsRegistered(t *testing.T) {
	families, err := Registry.Gather()
	require.NoError(t, err)

	names := make(map[string]bool, len(families))
	for _, mf := range families {
		names[mf.GetName()] = true
	}

	for _, want := range []string{
		"go_goroutines",
		"go_memstats_alloc_bytes",
		"process_resident_memory_bytes",
	} {
		assert.True(t, names[want], "expected metric %q to be exposed by Registry", want)
	}
}

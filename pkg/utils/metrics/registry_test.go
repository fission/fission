// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
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

// TestRegistryComposesIntoControllerRuntime mirrors what ServeMetrics does:
// it registers our custom Registry into controller-runtime's metrics.Registry
// and serves the latter. This must not fail with a duplicate-collector error,
// and the runtime metrics must still surface through the merged registry. The
// test guards against a future controller-runtime version that begins
// registering Go/process collectors of its own (which would collide).
func TestRegistryComposesIntoControllerRuntime(t *testing.T) {
	require.NoError(t, crmetrics.Registry.Register(Registry),
		"registering our Registry into controller-runtime's must not collide")

	families, err := crmetrics.Registry.Gather()
	require.NoError(t, err)

	names := make(map[string]bool, len(families))
	for _, mf := range families {
		names[mf.GetName()] = true
	}
	assert.True(t, names["go_goroutines"],
		"runtime metrics must surface through the merged controller-runtime registry")
}

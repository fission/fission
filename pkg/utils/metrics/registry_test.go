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

// TestRegisterRuntimeCollectors verifies that runtime metrics are exposed on
// the served (controller-runtime) registry, that calling it twice tolerates a
// collector that is already registered, and — critically — that the custom
// Registry still composes into the served registry afterwards. The latter is
// the regression guard: previously the runtime collectors lived in the custom
// Registry, so a name collision in the atomic compose call silently dropped
// every Fission metric.
func TestRegisterRuntimeCollectors(t *testing.T) {
	RegisterRuntimeCollectors()
	// Idempotent: a second call must not panic on already-registered collectors.
	require.NotPanics(t, RegisterRuntimeCollectors)

	require.NoError(t, crmetrics.Registry.Register(Registry),
		"custom Registry must still compose into the served registry")

	families, err := crmetrics.Registry.Gather()
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
		assert.Truef(t, names[want], "expected metric %q on the served registry", want)
	}
}

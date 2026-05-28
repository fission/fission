// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/stretchr/testify/require"
)

// TestCustomRegistryComposesOverRuntimeCollectors guards the invariant that the
// custom Registry contains no Go/process collectors. ServeMetrics composes this
// Registry into controller-runtime's metrics.Registry, which already exposes
// Go/process collectors. That compose is atomic, so a single colliding
// descriptor makes registration fail and silently drops every Fission metric
// (this regressed once and stalled the canary controller, which reads function
// metrics from Prometheus). Reproduce that environment and assert our Registry
// still composes cleanly.
func TestCustomRegistryComposesOverRuntimeCollectors(t *testing.T) {
	base := prometheus.NewRegistry()
	base.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsAll)),
	)

	require.NoError(t, base.Register(Registry),
		"custom Registry must compose into a registry that already has Go/process collectors")
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Registry holds Fission's custom application metrics. ServeMetrics composes it
// into controller-runtime's metrics.Registry, which is what is served on
// /metrics. Go runtime and process collectors (go_memstats_*, go_goroutines,
// process_resident_memory_bytes, ...) are already registered into that
// controller-runtime registry by its internal controller metrics init, so they
// are exposed on the same endpoint without anything extra here. Do not register
// Go/process collectors into this Registry: composing it into the
// controller-runtime registry is atomic, so a collision silently drops every
// Fission metric.
var (
	Registry = prometheus.NewRegistry()
)

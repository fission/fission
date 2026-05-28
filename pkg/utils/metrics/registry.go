// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	Registry = prometheus.NewRegistry()
)

// RegisterRuntimeCollectors exposes Go runtime and process metrics
// (go_memstats_*, go_goroutines, process_resident_memory_bytes, ...) on the
// metrics endpoint so memory growth and goroutine leaks are observable per
// subsystem.
//
// They are registered directly into controller-runtime's registry (the one
// ServeMetrics actually serves) rather than into the custom Registry. The
// custom Registry is merged into controller-runtime's as a single collector,
// so a name collision there fails atomically and silently drops every Fission
// metric. Some subsystems already have these collectors registered (e.g. via
// controller-runtime's metrics server), so registration tolerates a collector
// that is already present.
func RegisterRuntimeCollectors() {
	registerTolerant(collectors.NewGoCollector())
	registerTolerant(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

func registerTolerant(c prometheus.Collector) {
	if err := crmetrics.Registry.Register(c); err != nil {
		are := prometheus.AlreadyRegisteredError{}
		if !errors.As(err, &are) {
			panic(err)
		}
	}
}

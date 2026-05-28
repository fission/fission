// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

var (
	Registry = prometheus.NewRegistry()
)

// register adds a collector to Registry, tolerating a collector that is
// already registered so subsystem startup never panics on a duplicate.
func register(c prometheus.Collector) {
	if err := Registry.Register(c); err != nil {
		are := prometheus.AlreadyRegisteredError{}
		if !errors.As(err, &are) {
			panic(err)
		}
	}
}

func init() {
	// Expose Go runtime memory/goroutine metrics (go_memstats_*, go_goroutines)
	// and process metrics (process_resident_memory_bytes) on the metrics endpoint
	// so memory growth and goroutine leaks are observable per subsystem.
	register(collectors.NewGoCollector())
	register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

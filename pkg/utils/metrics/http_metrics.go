// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Number of requests by path, method and status code.",
		},
		[]string{"path", "method", "code"},
	)
	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "http_requests_duration_seconds",
			Help: "Time taken to serve the request by path and method.",
			// Histogram instead of Summary: a summary allocates a per-series
			// quantile stream (the single largest router heap consumer), whereas
			// histogram buckets are fixed-size and aggregatable across replicas.
			Buckets: prometheus.DefBuckets,
		},
		[]string{"path", "method"},
	)
	httpRequestInFlight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Number of requests currently being served by path and method.",
		},
		[]string{"path", "method"},
	)
)

func init() {
	Registry.MustRegister(httpRequestsTotal)
	Registry.MustRegister(httpRequestDuration)
	Registry.MustRegister(httpRequestInFlight)
}

// HTTPRecorder records HTTP request metrics into this package's vecs, labelled
// by a low-cardinality path (the matched route pattern). It exists so routing
// packages can drive HTTP metrics without this package knowing anything about
// routing — it satisfies pkg/utils/httpmux.Recorder structurally, so httpmux
// invokes it per matched route without importing back into metrics. The zero
// value is ready to use.
type HTTPRecorder struct{}

// All three use WithLabelValues (positional, matching each vec's label order)
// rather than With(prometheus.Labels{...}): the map form allocates a map per
// call on every instrumented request, while WithLabelValues hits the vec's
// internal child cache without that per-request allocation.

func (HTTPRecorder) InFlightInc(path, method string) {
	httpRequestInFlight.WithLabelValues(path, method).Inc()
}

func (HTTPRecorder) InFlightDec(path, method string) {
	httpRequestInFlight.WithLabelValues(path, method).Dec()
}

func (HTTPRecorder) Observe(path, method string, statusCode int, duration time.Duration) {
	httpRequestDuration.WithLabelValues(path, method).Observe(duration.Seconds())
	httpRequestsTotal.WithLabelValues(path, method, strconv.Itoa(statusCode)).Inc()
}

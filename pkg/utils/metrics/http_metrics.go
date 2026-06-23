// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	httpRequestsTotal = Int64Counter(
		"http_requests_total",
		"Number of requests by path, method and status code.",
	)
	// Buckets stay prometheus.DefBuckets verbatim so the exposed _bucket le
	// series are identical to the pre-migration histogram; the exporter appends
	// the +Inf bucket. Histogram (not Summary) for the same reason as before: a
	// summary's per-series quantile stream was the largest router heap consumer.
	httpRequestDuration = Float64Histogram(
		"http_requests_duration_seconds",
		"Time taken to serve the request by path and method.",
		prometheus.DefBuckets,
	)
	// In-flight is increment/decrement, so an UpDownCounter (exposed as a
	// Prometheus gauge), not a set-to-value gauge.
	httpRequestInFlight = Int64UpDownCounter(
		"http_requests_in_flight",
		"Number of requests currently being served by path and method.",
	)
)

// HTTPRecorder records HTTP request metrics into this package's instruments,
// labelled by a low-cardinality path (the matched route pattern). It exists so
// routing packages can drive HTTP metrics without this package knowing anything
// about routing — it satisfies pkg/utils/httpmux.Recorder structurally, so
// httpmux invokes it per matched route without importing back into metrics. The
// zero value is ready to use.
type HTTPRecorder struct{}

func (HTTPRecorder) InFlightInc(path, method string) {
	httpRequestInFlight.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("path", path),
		attribute.String("method", method),
	))
}

func (HTTPRecorder) InFlightDec(path, method string) {
	httpRequestInFlight.Add(context.Background(), -1, metric.WithAttributes(
		attribute.String("path", path),
		attribute.String("method", method),
	))
}

func (HTTPRecorder) Observe(path, method string, statusCode int, duration time.Duration) {
	ctx := context.Background()
	httpRequestDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(
		attribute.String("path", path),
		attribute.String("method", method),
	))
	httpRequestsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("path", path),
		attribute.String("method", method),
		attribute.String("code", strconv.Itoa(statusCode)),
	))
}

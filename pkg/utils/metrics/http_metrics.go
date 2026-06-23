// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"strconv"
	"sync"
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

// Attribute sets are cached by their low-cardinality label tuple (the matched
// route pattern + method [+ status code]) so the per-request path does not
// rebuild — and re-sort/de-dupe — an attribute.Set on every call. This is the
// OTel equivalent of the child-cache the prior WithLabelValues path hit;
// without it the router's busiest path allocates ~4 sets per request. The keys
// are comparable arrays so the lookup itself allocates nothing.
var (
	pmAttrs  sync.Map // [2]string{path, method} -> metric.MeasurementOption
	pmcAttrs sync.Map // [3]string{path, method, code} -> metric.MeasurementOption
)

func pathMethodAttrs(path, method string) metric.MeasurementOption {
	key := [2]string{path, method}
	if v, ok := pmAttrs.Load(key); ok {
		return v.(metric.MeasurementOption)
	}
	opt := metric.WithAttributes(
		attribute.String("path", path),
		attribute.String("method", method),
	)
	pmAttrs.Store(key, opt)
	return opt
}

func pathMethodCodeAttrs(path, method, code string) metric.MeasurementOption {
	key := [3]string{path, method, code}
	if v, ok := pmcAttrs.Load(key); ok {
		return v.(metric.MeasurementOption)
	}
	opt := metric.WithAttributes(
		attribute.String("path", path),
		attribute.String("method", method),
		attribute.String("code", code),
	)
	pmcAttrs.Store(key, opt)
	return opt
}

// HTTPRecorder records HTTP request metrics into this package's instruments,
// labelled by a low-cardinality path (the matched route pattern). It exists so
// routing packages can drive HTTP metrics without this package knowing anything
// about routing — it satisfies pkg/utils/httpmux.Recorder structurally, so
// httpmux invokes it per matched route without importing back into metrics. The
// zero value is ready to use.
type HTTPRecorder struct{}

func (HTTPRecorder) InFlightInc(path, method string) {
	httpRequestInFlight.Add(context.Background(), 1, pathMethodAttrs(path, method))
}

func (HTTPRecorder) InFlightDec(path, method string) {
	httpRequestInFlight.Add(context.Background(), -1, pathMethodAttrs(path, method))
}

func (HTTPRecorder) Observe(path, method string, statusCode int, duration time.Duration) {
	ctx := context.Background()
	httpRequestDuration.Record(ctx, duration.Seconds(), pathMethodAttrs(path, method))
	httpRequestsTotal.Add(ctx, 1, pathMethodCodeAttrs(path, method, strconv.Itoa(statusCode)))
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// scope is the single instrumentation scope for all Fission control-plane
// metrics. One flat scope keeps the Prometheus exposition free of per-package
// noise; the Prometheus exporter is configured WithoutScopeInfo so the scope
// never leaks into a label or an otel_scope_* series.
const scope = "github.com/fission/fission"

// Meter returns the Fission meter from the global MeterProvider.
//
// Instruments may be created at package-init time exactly like the previous
// prometheus.NewCounterVec vars: the global MeterProvider delegates to the real
// provider once otel.InitProvider has installed it (the same delegation that
// makes otel.Tracer usable before SetTracerProvider), so an instrument created
// before InitProvider runs is wired to the real reader retroactively.
func Meter() metric.Meter {
	return otel.Meter(scope)
}

// The constructors below mirror the prometheus.MustRegister idiom: instrument
// creation only fails on an invalid or conflicting definition, which is a
// programmer error that must surface loudly at startup rather than silently
// dropping a metric. They panic on error for that reason.

// Int64Counter creates a monotonic counter. Pass the full metric name including
// any _total suffix; the exporter is configured WithoutCounterSuffixes so the
// name is exposed verbatim.
func Int64Counter(name, help string) metric.Int64Counter {
	c, err := Meter().Int64Counter(name, metric.WithDescription(help))
	if err != nil {
		panic(fmt.Sprintf("metrics: create counter %q: %v", name, err))
	}
	return c
}

// Float64Histogram creates a histogram with explicit bucket boundaries. The
// boundaries are reproduced exactly (e.g. prometheus.DefBuckets) so the exposed
// _bucket le series match the pre-migration layout; the exporter appends the
// +Inf bucket itself.
func Float64Histogram(name, help string, buckets []float64) metric.Float64Histogram {
	h, err := Meter().Float64Histogram(name,
		metric.WithDescription(help),
		metric.WithExplicitBucketBoundaries(buckets...),
	)
	if err != nil {
		panic(fmt.Sprintf("metrics: create histogram %q: %v", name, err))
	}
	return h
}

// Int64UpDownCounter creates a non-monotonic sum, exposed by the Prometheus
// exporter as a gauge. Use this for gauges driven by increment/decrement (e.g.
// an in-flight counter), not by setting an absolute value.
func Int64UpDownCounter(name, help string) metric.Int64UpDownCounter {
	g, err := Meter().Int64UpDownCounter(name, metric.WithDescription(help))
	if err != nil {
		panic(fmt.Sprintf("metrics: create updowncounter %q: %v", name, err))
	}
	return g
}

// Int64Gauge creates a synchronous gauge, exposed as a Prometheus gauge. Use
// this for gauges set to an absolute value (Record), the OTel analogue of
// prometheus Gauge.Set.
func Int64Gauge(name, help string) metric.Int64Gauge {
	g, err := Meter().Int64Gauge(name, metric.WithDescription(help))
	if err != nil {
		panic(fmt.Sprintf("metrics: create gauge %q: %v", name, err))
	}
	return g
}

// Int64ObservableGauge creates an asynchronous gauge whose value is read from
// the callback on each collection, the OTel analogue of prometheus.NewGaugeFunc.
func Int64ObservableGauge(name, help string, cb metric.Int64Callback) metric.Int64ObservableGauge {
	g, err := Meter().Int64ObservableGauge(name,
		metric.WithDescription(help),
		metric.WithInt64Callback(cb),
	)
	if err != nil {
		panic(fmt.Sprintf("metrics: create observable gauge %q: %v", name, err))
	}
	return g
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
)

// setupMeter installs a MeterProvider whose Prometheus bridge reader writes into
// a fresh registry, and returns that registry. It validates the core migration
// assumption: instruments created at package-init time (the httpRequest* vars)
// against the global delegate wire up to the real reader once SetMeterProvider
// runs, exactly as the bootstrap installs it in production.
func setupMeter(t *testing.T) *prometheus.Registry {
	t.Helper()
	reg := prometheus.NewRegistry()
	mp, err := NewMeterProvider(resource.Default(), reg)
	require.NoError(t, err)
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { _ = mp.Shutdown(t.Context()) })
	return reg
}

// findMetric returns the first metric in family fam whose label set is a
// superset of want.
func findMetric(fam *dto.MetricFamily, want map[string]string) *dto.Metric {
	for _, m := range fam.GetMetric() {
		labels := map[string]string{}
		for _, l := range m.GetLabel() {
			labels[l.GetName()] = l.GetValue()
		}
		match := true
		for k, v := range want {
			if labels[k] != v {
				match = false
				break
			}
		}
		if match {
			return m
		}
	}
	return nil
}

// TestHTTPRecorderScrapeParity drives the recorder through the OTel meter and
// asserts the Prometheus exposition is unchanged by the migration: the metric
// names, types, label sets, histogram bucket boundaries (DefBuckets), and
// inc/dec gauge semantics all match the pre-migration client_golang surface.
func TestHTTPRecorderScrapeParity(t *testing.T) {
	reg := setupMeter(t)

	const path, method, code = "/metrics-recorder-test", "POST", "201"
	rec := HTTPRecorder{}

	rec.InFlightInc(path, method)
	rec.Observe(path, method, 201, 5*time.Millisecond)
	rec.InFlightDec(path, method)

	families, err := reg.Gather()
	require.NoError(t, err)
	byName := map[string]*dto.MetricFamily{}
	for _, f := range families {
		byName[f.GetName()] = f
	}

	// Counter: exact name, COUNTER type, {path,method,code} labels, value 1.
	calls := byName["http_requests_total"]
	require.NotNil(t, calls, "http_requests_total exposed (name not mangled by the exporter)")
	assert.Equal(t, dto.MetricType_COUNTER, calls.GetType())
	m := findMetric(calls, map[string]string{"path": path, "method": method, "code": code})
	require.NotNil(t, m, "counter carries {path,method,code}")
	assert.Equal(t, float64(1), m.GetCounter().GetValue())

	// Histogram: exact name, HISTOGRAM type, bucket boundaries == DefBuckets.
	dur := byName["http_requests_duration_seconds"]
	require.NotNil(t, dur, "http_requests_duration_seconds exposed")
	assert.Equal(t, dto.MetricType_HISTOGRAM, dur.GetType())
	hm := findMetric(dur, map[string]string{"path": path, "method": method})
	require.NotNil(t, hm, "histogram carries {path,method}")
	assert.Equal(t, uint64(1), hm.GetHistogram().GetSampleCount())
	gotBounds := []float64{}
	for _, b := range hm.GetHistogram().GetBucket() {
		gotBounds = append(gotBounds, b.GetUpperBound())
	}
	assert.Equal(t, prometheus.DefBuckets, gotBounds,
		"histogram bucket boundaries reproduce prometheus.DefBuckets exactly")

	// In-flight: exact name, GAUGE type (UpDownCounter), balanced to 0.
	inflight := byName["http_requests_in_flight"]
	require.NotNil(t, inflight, "http_requests_in_flight exposed")
	assert.Equal(t, dto.MetricType_GAUGE, inflight.GetType())
	gm := findMetric(inflight, map[string]string{"path": path, "method": method})
	require.NotNil(t, gm, "gauge carries {path,method}")
	assert.Zero(t, gm.GetGauge().GetValue(), "in-flight balanced after inc/dec")
}

// BenchmarkHTTPRecorder measures the per-request metrics cost (inc + observe +
// dec) an instrumented public-listener request pays.
func BenchmarkHTTPRecorder(b *testing.B) {
	var rec HTTPRecorder
	b.ReportAllocs()
	for b.Loop() {
		rec.InFlightInc("/bench", "GET")
		rec.Observe("/bench", "GET", 200, time.Millisecond)
		rec.InFlightDec("/bench", "GET")
	}
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// TestExporterInstrumentKinds locks how the OTel->Prometheus bridge exposes the
// instrument kinds the migration relies on: a synchronous gauge (Set), an
// up-down counter (inc/dec gauge), and an observable gauge (GaugeFunc) must all
// surface as Prometheus GAUGE with the recorded value. It builds the provider
// directly (no global state) so it is deterministic and parallel-safe.
func TestExporterInstrumentKinds(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	mp, err := NewMeterProvider(resource.Default(), reg, true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mp.Shutdown(t.Context()) })
	m := mp.Meter("test")

	gauge, err := m.Int64Gauge("test_set_gauge")
	require.NoError(t, err)
	gauge.Record(t.Context(), 7)

	updown, err := m.Int64UpDownCounter("test_inflight")
	require.NoError(t, err)
	updown.Add(t.Context(), 5)
	updown.Add(t.Context(), -2)

	_, err = m.Int64ObservableGauge("test_observed",
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(42)
			return nil
		}))
	require.NoError(t, err)

	families, err := reg.Gather()
	require.NoError(t, err)
	byName := map[string]*dto.MetricFamily{}
	for _, f := range families {
		byName[f.GetName()] = f
	}

	for name, want := range map[string]float64{
		"test_set_gauge": 7, // synchronous gauge: last Record wins
		"test_inflight":  3, // up-down counter: net of +5, -2
		"test_observed":  42,
	} {
		fam := byName[name]
		require.NotNil(t, fam, "%s exposed", name)
		assert.Equal(t, dto.MetricType_GAUGE, fam.GetType(), "%s is a Prometheus gauge", name)
		require.Len(t, fam.GetMetric(), 1)
		assert.Equal(t, want, fam.GetMetric()[0].GetGauge().GetValue(), "%s value", name)
	}
}

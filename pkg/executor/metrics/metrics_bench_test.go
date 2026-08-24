// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/metrics"
)

var allExecutorTypes = []fv1.ExecutorType{
	fv1.ExecutorTypePoolmgr, fv1.ExecutorTypeNewdeploy, fv1.ExecutorTypeContainer,
}

var allReapStages = []ReapStage{ReapStagePrepare, ReapStageList, ReapStageReap}

// installMeter wires the package-level instruments to a fresh registry. Only the
// first install in a test binary takes effect (see metricstest.SetupMeter), which
// is fine here: every case in this file wants the same provider.
func installMeter(tb testing.TB) *prometheus.Registry {
	tb.Helper()
	reg := prometheus.NewRegistry()
	mp, err := metrics.NewMeterProvider(resource.Default(), reg, false)
	require.NoError(tb, err)
	otel.SetMeterProvider(mp)
	return reg
}

// populate records every executor_type x stage combination the reaper can
// produce, so the registry holds the instruments at their maximum cardinality.
func populate(ctx context.Context) {
	for _, et := range allExecutorTypes {
		RecordIdleReap(ctx, et, 1.25)
		for _, st := range allReapStages {
			RecordIdleReapError(ctx, et, st)
		}
	}
}

// TestReaperMetricCardinalityIsBounded locks the cardinality contract these
// instruments were designed around: both dimensions are closed sets, so the
// series count is a constant that cannot grow with functions, pods or traffic.
// Adding a label keyed on anything unbounded — a function name, a pod, a reason
// string — would fail here rather than in someone's Prometheus.
func TestReaperMetricCardinalityIsBounded(t *testing.T) {
	reg := installMeter(t)
	populate(t.Context())

	families, err := reg.Gather()
	require.NoError(t, err)

	series := map[string]int{}
	for _, fam := range families {
		switch fam.GetName() {
		case "fission_executor_idle_reap_lag_seconds", "fission_executor_idle_reap_errors_total":
			series[fam.GetName()] = len(fam.GetMetric())
		}
	}
	// 3 executor types; 3 x 3 executor/stage pairs.
	require.Equal(t, 3, series["fission_executor_idle_reap_lag_seconds"], "lag is keyed by executor_type alone")
	require.Equal(t, 9, series["fission_executor_idle_reap_errors_total"], "errors are keyed by executor_type x stage")
}

// BenchmarkRecordIdleReap and BenchmarkRecordIdleReapError measure the per-call
// cost of the reaper's instrumentation. Both attribute dimensions are closed, so
// their option values are precomputed at init; without that the error path built
// and merged two attribute sets per call and cost 528ns/640B/11 allocs against
// the ~104ns/16B/1 alloc measured now.
func BenchmarkRecordIdleReap(b *testing.B) {
	installMeter(b)
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		RecordIdleReap(ctx, fv1.ExecutorTypePoolmgr, 3.5)
	}
}

func BenchmarkRecordIdleReapError(b *testing.B) {
	installMeter(b)
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		RecordIdleReapError(ctx, fv1.ExecutorTypePoolmgr, ReapStageReap)
	}
}

// BenchmarkGather measures scrape-side cost with the instruments at full
// cardinality — the per-scrape price an operator pays for having them.
func BenchmarkGather(b *testing.B) {
	reg := installMeter(b)
	populate(b.Context())
	b.ReportAllocs()
	for b.Loop() {
		if _, err := reg.Gather(); err != nil {
			b.Fatal(err)
		}
	}
}

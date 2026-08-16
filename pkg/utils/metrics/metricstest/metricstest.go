// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package metricstest is shared test support for asserting package-level OTel
// instruments through the Prometheus bridge. It exists because the
// install-provider + scan-families dance was being copy-pasted per test
// package (pkg/utils/metrics, pkg/router, pkg/router/endpointcache); import
// this instead of pasting a fourth copy.
package metricstest

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/fission/fission/pkg/utils/metrics"
)

// SetupMeter installs a MeterProvider backed by a fresh Prometheus registry
// and returns the registry. Package-level instruments bind to the global
// MeterProvider delegate at package init, so a test-installed provider
// retroactively wires them to this registry. Only ONE test per test binary
// may install a provider: a second install silently keeps recording into the
// FIRST registry, so tests of one binary needing metrics must share a single
// SetupMeter call.
func SetupMeter(t *testing.T) *prometheus.Registry {
	t.Helper()
	reg := prometheus.NewRegistry()
	mp, err := metrics.NewMeterProvider(resource.Default(), reg, true)
	require.NoError(t, err)
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { _ = mp.Shutdown(t.Context()) })
	return reg
}

// FindMetric returns the first series in fam carrying every label in want,
// or nil when none matches.
func FindMetric(fam *dto.MetricFamily, want map[string]string) *dto.Metric {
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

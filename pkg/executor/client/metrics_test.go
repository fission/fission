// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"os"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/fission/fission/pkg/utils/metrics"
)

// testMetricsReg backs the meter provider installed for this package's tests, so
// the OTel tap-flush counters can be read back via the Prometheus exposition.
var testMetricsReg *prometheus.Registry

func TestMain(m *testing.M) {
	testMetricsReg = prometheus.NewRegistry()
	mp, err := metrics.NewMeterProvider(resource.Default(), testMetricsReg)
	if err != nil {
		panic(err)
	}
	otel.SetMeterProvider(mp)
	os.Exit(m.Run())
}

// counterValue returns the current value of the no-label counter named name,
// gathered from the test registry (0 if the series is absent).
func counterValue(t *testing.T, name string) float64 {
	t.Helper()
	fams, err := testMetricsReg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, f := range fams {
		if f.GetName() == name {
			for _, metric := range f.GetMetric() {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

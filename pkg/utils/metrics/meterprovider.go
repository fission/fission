// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/otlptranslator"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// PromExporterOptions configures the OTel->Prometheus bridge to reproduce the
// pre-migration /metrics exposition exactly. UnderscoreEscapingWithoutSuffixes
// keeps Fission's already-valid metric names verbatim — no appended unit suffix
// and no doubled _total on counters whose name already carries it — while
// WithoutScopeInfo/WithoutTargetInfo drop the otel_scope_* labels and the
// target_info series. Without these the exporter would rename metrics and add
// series that break existing dashboards and alerts.
func PromExporterOptions(reg prometheus.Registerer) []otelprom.Option {
	return []otelprom.Option{
		otelprom.WithRegisterer(reg),
		otelprom.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithoutSuffixes),
		otelprom.WithoutScopeInfo(),
		otelprom.WithoutTargetInfo(),
	}
}

// NewMeterProvider builds the MeterProvider that serves Fission's application
// metrics. The Prometheus bridge reader is always registered against reg (the
// same registry ServeMetrics composes into controller-runtime's and serves on
// /metrics), so the scrape contract holds whether or not OTLP push is
// configured. extraReaders (e.g. an OTLP periodic reader) are appended by the
// caller.
func NewMeterProvider(res *resource.Resource, reg prometheus.Registerer, extraReaders ...sdkmetric.Reader) (*sdkmetric.MeterProvider, error) {
	promExp, err := otelprom.New(PromExporterOptions(reg)...)
	if err != nil {
		return nil, err
	}
	opts := []sdkmetric.Option{
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(promExp),
	}
	for _, r := range extraReaders {
		if r != nil {
			opts = append(opts, sdkmetric.WithReader(r))
		}
	}
	return sdkmetric.NewMeterProvider(opts...), nil
}

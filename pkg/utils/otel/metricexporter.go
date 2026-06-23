// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package otel

import (
	"context"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"google.golang.org/grpc/credentials"
)

// getMetricReader builds the OTLP periodic reader that pushes metrics natively
// over OTLP (RFC-0019 phase 4). It is opt-in: returns nil unless an OTLP
// endpoint is configured AND OTEL_METRICS_EXPORTER selects otlp, so the default
// install keeps Prometheus-scrape-only behavior. The Prometheus bridge reader
// is always present regardless (see metrics.NewMeterProvider), so enabling OTLP
// push adds a parallel pipeline rather than replacing the scrape.
func getMetricReader(ctx context.Context, cfg OtelConfig) (sdkmetric.Reader, error) {
	if cfg.endpoint == "" || !cfg.metricsOTLP {
		return nil, nil
	}
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(cfg.endpoint)}
	if cfg.insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	} else {
		opts = append(opts, otlpmetricgrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, "")))
	}
	exporter, err := otlpmetricgrpc.New(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return sdkmetric.NewPeriodicReader(exporter), nil
}

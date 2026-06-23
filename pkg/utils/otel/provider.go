// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package otel

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/contrib/propagators/autoprop"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otellogglobal "go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"google.golang.org/grpc/credentials"
	apiv1 "k8s.io/api/core/v1"

	"github.com/fission/fission/pkg/utils/metrics"
)

const (
	OtelEnvPrefix         = "OTEL_"
	OtelEndpointEnvVar    = "OTEL_EXPORTER_OTLP_ENDPOINT"
	OtelInsecureEnvVar    = "OTEL_EXPORTER_OTLP_INSECURE"
	OtelPropagaters       = "OTEL_PROPAGATORS"
	OtelLogsEnabledEnvVar = "OTEL_LOGS_ENABLED"
)

type OtelConfig struct {
	endpoint string
	insecure bool
	// logsEnabled opts control-plane logs into the OTLP push (RFC-0016 ph4);
	// off by default so enabling traces alone does not change log behavior.
	logsEnabled bool
}

// parseOtelConfig parses the environment variables OTEL_EXPORTER_OTLP_ENDPOINT and
func parseOtelConfig() OtelConfig {
	config := OtelConfig{}
	config.endpoint = os.Getenv(OtelEndpointEnvVar)
	insecure, err := strconv.ParseBool(os.Getenv(OtelInsecureEnvVar))
	if err != nil {
		insecure = true
	}
	config.insecure = insecure
	config.logsEnabled, _ = strconv.ParseBool(os.Getenv(OtelLogsEnabledEnvVar))
	return config
}

func getTraceExporter(ctx context.Context, logger logr.Logger) (*otlptrace.Exporter, error) {
	otelConfig := parseOtelConfig()
	if otelConfig.endpoint == "" {
		logger.Info("OTEL_EXPORTER_OTLP_ENDPOINT not set, skipping Opentelemtry tracing")
		return nil, nil
	}

	grpcOpts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(otelConfig.endpoint),
	}
	if otelConfig.insecure {
		grpcOpts = append(grpcOpts, otlptracegrpc.WithInsecure())
	} else {
		grpcOpts = append(grpcOpts, otlptracegrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, "")))
	}

	exporter, err := otlptracegrpc.New(ctx, grpcOpts...)
	if err != nil {
		return nil, err
	}
	return exporter, nil
}

// getLogExporter builds the OTLP gRPC log exporter (RFC-0016 phase 4), mirroring
// the trace exporter's endpoint/insecure handling. Returns nil when no endpoint
// is configured, so log push stays inert by default.
func getLogExporter(ctx context.Context, cfg OtelConfig) (*otlploggrpc.Exporter, error) {
	if cfg.endpoint == "" || !cfg.logsEnabled {
		return nil, nil
	}
	opts := []otlploggrpc.Option{otlploggrpc.WithEndpoint(cfg.endpoint)}
	if cfg.insecure {
		opts = append(opts, otlploggrpc.WithInsecure())
	} else {
		opts = append(opts, otlploggrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, "")))
	}
	return otlploggrpc.New(ctx, opts...)
}

// Initializes an OTLP exporter, and configures the corresponding trace and metric providers.
func InitProvider(ctx context.Context, logger logr.Logger, serviceName string) (func(context.Context), error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		return nil, err
	}
	traceExporter, err := getTraceExporter(ctx, logger)
	if err != nil {
		return nil, err
	}

	tpOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if traceExporter != nil {
		// Pin the head sampler explicitly from OTEL_TRACES_SAMPLER (previously
		// ignored) and wrap it so failed invocations are always recorded
		// (RFC-0015): the base decides export volume for successful traces, and
		// errorExportProcessor force-exports error spans the base dropped. Only
		// applied when an exporter exists, so tracing stays fully inert (no span
		// recording) when OTEL_EXPORTER_OTLP_ENDPOINT is unset.
		tpOpts = append(tpOpts, sdktrace.WithSampler(errorBiasedSampler{base: baseSamplerFromEnv()}))
	}
	tracerProvider := sdktrace.NewTracerProvider(tpOpts...)

	if traceExporter != nil {
		tracerProvider.RegisterSpanProcessor(sdktrace.NewBatchSpanProcessor(traceExporter))
		tracerProvider.RegisterSpanProcessor(newErrorExportProcessor(traceExporter, logger))
	}

	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(autoprop.NewTextMapPropagator())

	// Metrics: always register the OTel->Prometheus bridge reader against the
	// shared registry, so the /metrics scrape is byte-for-byte unchanged whether
	// or not OTLP push is configured. SetMeterProvider wires every metric
	// instrument created at package-init time via the global provider's
	// delegation (the same mechanism as SetTracerProvider above).
	meterProvider, err := metrics.NewMeterProvider(res, metrics.Registry)
	if err != nil {
		return nil, err
	}
	otel.SetMeterProvider(meterProvider)

	// Control-plane OTLP log push (RFC-0016 phase 4): when an OTLP endpoint is
	// configured, stand up a LoggerProvider and register it globally so the zap
	// bridge in loggerfactory pushes control-plane logs (carrying trace_id) as
	// OTLP records. Inert when the endpoint is unset (the global stays no-op).
	var loggerProvider *sdklog.LoggerProvider
	logExporter, err := getLogExporter(ctx, parseOtelConfig())
	if err != nil {
		return nil, err
	}
	if logExporter != nil {
		loggerProvider = sdklog.NewLoggerProvider(
			sdklog.WithResource(res),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		)
		otellogglobal.SetLoggerProvider(loggerProvider)
	}

	// Shutdown will flush any remaining spans/logs and shut down the exporters.
	return func(ctx context.Context) {
		if ctx.Err() != nil {
			// if the context is already cancelled, create a new one with a timeout of 30 seconds
			ctxwithTimeout, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ctx = ctxwithTimeout
		}
		err := tracerProvider.Shutdown(ctx)
		if err != nil {
			logger.Error(err, "error shutting down trace provider")
		}
		if err = meterProvider.Shutdown(ctx); err != nil {
			logger.Error(err, "error shutting down meter provider")
		}
		if traceExporter != nil {
			if err = traceExporter.Shutdown(ctx); err != nil {
				logger.Error(err, "error shutting down trace exporter")
			}
		}
		if loggerProvider != nil {
			if err = loggerProvider.Shutdown(ctx); err != nil {
				logger.Error(err, "error shutting down logger provider")
			}
		}
	}, nil
}

// OtelEnvForContainer returns a list of environment variables
// for the container, which start with prefix OTEL_
func OtelEnvForContainer() []apiv1.EnvVar {
	otelEnvs := []apiv1.EnvVar{}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, OtelEnvPrefix) {
			pair := strings.SplitN(e, "=", 2)
			otelEnvs = append(otelEnvs, apiv1.EnvVar{
				Name:  pair[0],
				Value: pair[1],
			})

		}
	}
	return otelEnvs
}

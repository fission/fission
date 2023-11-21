package otel

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/propagators/autoprop"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	apiv1 "k8s.io/api/core/v1"
)

const (
	OtelEnvPrefix      = "OTEL_"
	OtelEndpointEnvVar = "OTEL_EXPORTER_OTLP_ENDPOINT"
	OtelInsecureEnvVar = "OTEL_EXPORTER_OTLP_INSECURE"
	OtelPropagaters    = "OTEL_PROPAGATORS"
)

type OtelConfig struct {
	endpoint string
	insecure bool
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
	return config
}

func getTraceExporter(ctx context.Context, logger *zap.Logger) (*otlptrace.Exporter, error) {
	otelConfig := parseOtelConfig()
	if otelConfig.endpoint == "" {
		if logger != nil {
			logger.Info("OTEL_EXPORTER_OTLP_ENDPOINT not set, skipping Opentelemtry tracing")
		}
		return nil, nil
	}

	grpcOpts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(otelConfig.endpoint),
		otlptracegrpc.WithDialOption(grpc.WithBlock()),
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

// Initializes an OTLP exporter, and configures the corresponding trace and metric providers.
func InitProvider(ctx context.Context, logger *zap.Logger, serviceName string) (func(context.Context), error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		return nil, err
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
	)
	traceExporter, err := getTraceExporter(ctx, logger)
	if err != nil {
		return nil, err
	}

	if traceExporter != nil {
		bsp := sdktrace.NewBatchSpanProcessor(traceExporter)
		tracerProvider.RegisterSpanProcessor(bsp)
	}

	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(autoprop.NewTextMapPropagator())
	// Shutdown will flush any remaining spans and shut down the exporter.
	return func(ctx context.Context) {
		if ctx.Err() != nil {
			// if the context is already cancelled, create a new one with a timeout of 30 seconds
			ctxwithTimeout, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ctx = ctxwithTimeout
		}
		err := tracerProvider.Shutdown(ctx)
		if err != nil && logger != nil {
			logger.Error("error shutting down trace provider", zap.Error(err))
		}
		if traceExporter != nil {
			if err = traceExporter.Shutdown(ctx); err != nil && logger != nil {
				logger.Error("error shutting down trace exporter", zap.Error(err))
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

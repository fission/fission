package otel

import (
	"context"
	"os"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	OtelEndpointEnvVar = "OTEL_EXPORTER_OTLP_ENDPOINT"
	OtelInsecureEnvVar = "OTEL_EXPORTER_OTLP_INSECURE"
)

type OtelConfig struct {
	endpoint string
	insecure bool
}

func parseOtelConfig() OtelConfig {
	config := OtelConfig{}
	config.endpoint = os.Getenv(OtelEndpointEnvVar)
	insecure, err := strconv.ParseBool(os.Getenv(OtelInsecureEnvVar))
	if err != nil {
		insecure = false
	}
	config.insecure = insecure
	return config
}

func getTraceExporter(ctx context.Context, logger *zap.Logger) (*otlptrace.Exporter, error) {
	otelConfig := parseOtelConfig()
	if otelConfig.endpoint == "" {
		logger.Info("OTEL_EXPORTER_OTLP_ENDPOINT not set, skipping Opentelemtry tracing")
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
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	// Shutdown will flush any remaining spans and shut down the exporter.
	return func(ctx context.Context) {
		err := tracerProvider.Shutdown(ctx)
		if err != nil {
			logger.Fatal("error shutting down trace provider", zap.Error(err))
		}
		if traceExporter != nil {
			if err = traceExporter.Shutdown(ctx); err != nil {
				logger.Fatal("error shutting down trace exporter", zap.Error(err))
			}
		}
	}, nil
}

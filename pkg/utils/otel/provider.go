package otel

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

func getSpanProcessor(ctx context.Context, logger *zap.Logger) (*sdktrace.SpanProcessor, error) {
	collectorEndpoint := os.Getenv("OTEL_COLLECTOR_ENDPOINT")
	if collectorEndpoint == "" {
		logger.Info("skipping trace exporter registration")
		return nil, nil
	}

	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(collectorEndpoint),
		otlptracegrpc.WithDialOption(grpc.WithBlock()),
	)
	if err != nil {
		return nil, err
	}

	bsp := sdktrace.NewBatchSpanProcessor(traceExporter)
	return &bsp, nil
}

// Initializes an OTLP exporter, and configures the corresponding trace and metric providers.
func InitProvider(logger *zap.Logger, serviceName string) (func(), error) {
	ctx := context.Background()
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		return nil, err
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(res),
	)

	bsp, err := getSpanProcessor(ctx, logger)
	if err != nil {
		return nil, err
	}
	if bsp != nil {
		tracerProvider.RegisterSpanProcessor(*bsp)
	}

	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Shutdown will flush any remaining spans and shut down the exporter.
	return func() {
		err := tracerProvider.Shutdown(ctx)
		if err != nil {
			logger.Fatal("error shutting down trace provider", zap.Error(err))
		}
	}, nil
}

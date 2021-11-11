package otel

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/contrib/propagators/aws/xray"
	"go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/contrib/propagators/jaeger"
	"go.opentelemetry.io/contrib/propagators/ot"
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
	apiv1 "k8s.io/api/core/v1"
)

const (
	OtelEnvPrefix        = "OTEL_"
	OtelEndpointEnvVar   = "OTEL_EXPORTER_OTLP_ENDPOINT"
	OtelInsecureEnvVar   = "OTEL_EXPORTER_OTLP_INSECURE"
	OtelTracesSampler    = "OTEL_TRACES_SAMPLER"
	OtelTracesSamplerArg = "OTEL_TRACES_SAMPLER_ARG"
	OtelPropogaters      = "OTEL_PROPOGATORS"
)

type OtelConfig struct {
	endpoint string
	insecure bool
}

/*
Each Sampler type defines its own expected input, if any.
Currently we get trace ratio for the case of,
1. traceidratio
2. parentbased_traceidratio
*/
func getSamplerArg() (float64, error) {
	arg := os.Getenv(OtelTracesSamplerArg)
	return strconv.ParseFloat(arg, 64)
}

/* GetPropogater returns a slice of propagators to be used by the OpenTelemetry
provider.

Supported providers:
tracecontext - W3C Trace Context
baggage - W3C Baggage
b3 - B3 Single
b3multi - B3 Multi
jaeger - Jaeger uber-trace-id header
xray - AWS X-Ray (third party)
ottrace - OpenTracing Trace (third party)
*/
func GetPropogater(logger *zap.Logger) []propagation.TextMapPropagator {
	propogatersEnv := os.Getenv(OtelPropogaters)
	if propogatersEnv == "" {
		return []propagation.TextMapPropagator{
			propagation.TraceContext{}, propagation.Baggage{},
		}
	}
	propogators := []propagation.TextMapPropagator{}
	for _, prop := range strings.Split(propogatersEnv, ",") {
		switch prop {
		case "tracecontext":
			propogators = append(propogators, propagation.TraceContext{})
		case "baggage":
			propogators = append(propogators, propagation.Baggage{})
		case "b3multi":
			propogators = append(propogators, b3.New(b3.WithInjectEncoding(b3.B3MultipleHeader)))
		case "b3":
			propogators = append(propogators, b3.New(b3.WithInjectEncoding(b3.B3SingleHeader)))
		case "jaeger":
			propogators = append(propogators, jaeger.Jaeger{})
		case "xray":
			propogators = append(propogators, xray.Propagator{})
		case "ottrace":
			propogators = append(propogators, ot.OT{})
		default:
			logger.Error("Unsupported propagation type", zap.String("propagation", prop))
		}
	}
	if len(propogators) == 0 {
		return []propagation.TextMapPropagator{
			propagation.TraceContext{}, propagation.Baggage{},
		}
	}
	return propogators
}

/*
GetSampler returns a sampler that can be used to sample traces.
This is based on https://github.com/open-telemetry/opentelemetry-specification/blob/main/specification/sdk-environment-variables.md#general-sdk-configuration
We have to implement as open-telemetry Go sdk doesn't support configuration of different samplers.
Once its added we may remove this code.

Supported samplers:
always_on - Sampler that always samples spans, regardless of the parent span's sampling decision.
always_off - Sampler that never samples spans, regardless of the parent span's sampling decision.
traceidratio - Sampler that samples probabalistically based on rate.
parentbased_always_on - (default) Sampler that respects its parent span's sampling decision, but otherwise always samples.
parentbased_always_off - Sampler that respects its parent span's sampling decision, but otherwise never samples.
parentbased_traceidratio - Sampler that respects its parent span's sampling decision, but otherwise samples probabalistically based on rate.

Environment variables:
OTEL_TRACES_SAMPLER - Sampler to use(one of the above samplers)
OTEL_TRACES_SAMPLER_ARG - Argument to pass to the sampler(float value)
*/
func GetSampler() (sdktrace.Sampler, error) {
	samplerType := os.Getenv(OtelTracesSampler)
	switch samplerType {
	case "always_on":
		return sdktrace.AlwaysSample(), nil
	case "always_off":
		return sdktrace.NeverSample(), nil
	case "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample()), nil
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample()), nil
	case "traceidratio":
		arg, err := getSamplerArg()
		if err != nil {
			return nil, fmt.Errorf("invalid sampler arg: %w", err)
		}
		return sdktrace.TraceIDRatioBased(arg), nil
	case "parentbased_traceidratio":
		arg, err := getSamplerArg()
		if err != nil {
			return nil, fmt.Errorf("invalid sampler arg: %w", err)
		}
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(arg)), nil
	default:
		return sdktrace.ParentBased(sdktrace.AlwaysSample()), nil
	}
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
	sampler, err := GetSampler()
	if err != nil {
		return nil, err
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
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
	propogaters := GetPropogater(logger)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propogaters...))
	// Shutdown will flush any remaining spans and shut down the exporter.
	return func(ctx context.Context) {
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

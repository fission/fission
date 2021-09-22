package otel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"go.opentelemetry.io/contrib/propagators/jaeger"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestGetPropogater(t *testing.T) {
	if OtelPropogaters != "OTEL_PROPOGATORS" {
		t.Errorf("Expected OTEL_PROPOGATORS to be set, got %s", OtelPropogaters)
	}
	tests := []struct {
		propogaterEnv string
		propogaters   []propagation.TextMapPropagator
	}{
		{
			"",
			[]propagation.TextMapPropagator{propagation.TraceContext{}, propagation.Baggage{}},
		},
		{
			"tracecontext,baggage",
			[]propagation.TextMapPropagator{propagation.TraceContext{}, propagation.Baggage{}},
		},
		{
			"jaeger",
			[]propagation.TextMapPropagator{jaeger.Jaeger{}},
		},
		{
			"baggage,tracecontext",
			[]propagation.TextMapPropagator{propagation.Baggage{}, propagation.TraceContext{}},
		},
		{
			"jaeger,baggage",
			[]propagation.TextMapPropagator{jaeger.Jaeger{}, propagation.Baggage{}},
		},
	}
	logger := loggerfactory.GetLogger()
	for _, tt := range tests {
		os.Setenv(OtelPropogaters, tt.propogaterEnv)
		prop := GetPropogater(logger)
		if prop == nil {
			t.Errorf("GetPropogater() = %#v, want %#v", prop, tt.propogaters)
		}
		if len(prop) != len(tt.propogaters) {
			t.Errorf("GetPropogater() = %#v, want %#v", prop, tt.propogaters)
		}
		if !reflect.DeepEqual(prop, tt.propogaters) {
			t.Errorf("GetPropogater() = %#v, want %#v", prop, tt.propogaters)
		}
	}
}

func TestGetSampler(t *testing.T) {
	if OtelTracesSampler != "OTEL_TRACES_SAMPLER" {
		t.Errorf("Expected OTEL_TRACES_SAMPLER to be set, got %s", OtelTracesSampler)
	}
	if OtelTracesSamplerArg != "OTEL_TRACES_SAMPLER_ARG" {
		t.Errorf("Expected OTEL_TRACES_SAMPLER_ARG to be set, got %s", OtelTracesSamplerArg)
	}
	if OtelPropogaters != "OTEL_PROPOGATORS" {
		t.Errorf("Expected OTEL_PROPOGATORS to be set, got %s", OtelPropogaters)
	}
	tests := []struct {
		sampler     string
		samplerArg  string
		wantSampler sdktrace.Sampler
		wantError   error
	}{
		{
			"",
			"",
			sdktrace.ParentBased(sdktrace.AlwaysSample()),
			nil,
		},
		{
			"always_on",
			"",
			sdktrace.AlwaysSample(),
			nil,
		},
		{
			"always_off",
			"",
			sdktrace.NeverSample(),
			nil,
		},
		{
			"parentbased_always_on",
			"",
			sdktrace.ParentBased(sdktrace.AlwaysSample()),
			nil,
		},
		{
			"parentbased_always_off",
			"",
			sdktrace.ParentBased(sdktrace.NeverSample()),
			nil,
		},
		{
			"traceidratio",
			"0.5",
			sdktrace.TraceIDRatioBased(0.5),
			nil,
		},
		{
			"traceidratio",
			"",
			nil,
			errors.New("invalid sampler arg: strconv.ParseFloat: parsing \"\": invalid syntax"),
		},
		{
			"parentbased_traceidratio",
			"",
			nil,
			errors.New("invalid sampler arg: strconv.ParseFloat: parsing \"\": invalid syntax"),
		},
		{
			"parentbased_traceidratio",
			"0.01",
			sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.01)),
			nil,
		},
	}
	for _, tt := range tests {
		os.Setenv(OtelTracesSampler, tt.sampler)
		os.Setenv(OtelTracesSamplerArg, tt.samplerArg)
		gotSampler, gotError := GetSampler()
		if !reflect.DeepEqual(gotSampler, tt.wantSampler) {
			t.Errorf("GetSampler() gotSampler = %#v, want %#v", gotSampler, tt.wantSampler)
		}
		if fmt.Sprintf("%s", gotError) != fmt.Sprintf("%s", tt.wantError) {
			t.Errorf("GetSampler() gotError = %#v, want %#v", gotError, tt.wantError)
		}
	}
}

func TestGetTraceExporter(t *testing.T) {
	if OtelEndpointEnvVar != "OTEL_EXPORTER_OTLP_ENDPOINT" {
		t.Errorf("Expected OTEL_EXPORTER_OTLP_ENDPOINT to be set, got %s", OtelEndpointEnvVar)
	}
	if OtelInsecureEnvVar != "OTEL_EXPORTER_OTLP_INSECURE" {
		t.Errorf("Expected OTEL_EXPORTER_OTLP_INSECURE to be set, got %s", OtelInsecureEnvVar)
	}
	logger := loggerfactory.GetLogger()
	ctx := context.Background()
	tests := []struct {
		oltpEndpoint string
		oltpInsecure string
		wantExporter *otlptrace.Exporter
		wantError    error
	}{
		{
			"",
			"",
			nil,
			nil,
		},
	}
	for _, tt := range tests {
		os.Setenv(OtelEndpointEnvVar, tt.oltpEndpoint)
		os.Setenv(OtelInsecureEnvVar, tt.oltpInsecure)
		exporter, err := getTraceExporter(ctx, logger)
		if !reflect.DeepEqual(exporter, tt.wantExporter) {
			t.Errorf("getTraceExporter() exporter = %#v, want %#v", exporter, tt.wantExporter)
		}
		if fmt.Sprintf("%s", err) != fmt.Sprintf("%s", tt.wantError) {
			t.Errorf("getTraceExporter() err = %#v, want %#v", err, tt.wantError)
		}
	}
}

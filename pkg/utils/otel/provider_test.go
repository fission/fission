// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package otel

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"

	"go.opentelemetry.io/contrib/propagators/autoprop"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestGetPropogater(t *testing.T) {
	if OtelPropagaters != "OTEL_PROPAGATORS" {
		t.Errorf("Expected OTEL_PROPAGATORS to be set, got %s", OtelPropagaters)
	}
	// tracecontext, baggage, b3, b3multi, jaeger, xray, ottrace, and none
	tests := []struct {
		propogaterEnv string
		propogaters   []string
	}{
		{
			"none",
			[]string{},
		},
		{
			"tracecontext,baggage",
			[]string{"baggage", "traceparent", "tracestate"},
		},
		{
			"jaeger",
			[]string{"uber-trace-id"},
		},
		{
			"baggage,tracecontext",
			[]string{"baggage", "traceparent", "tracestate"},
		},
		{
			"jaeger,baggage",
			[]string{"baggage", "uber-trace-id"},
		},
	}
	for _, tt := range tests {
		os.Setenv(OtelPropagaters, tt.propogaterEnv)
		propFields := autoprop.NewTextMapPropagator().Fields()
		sort.Strings(propFields)
		if !reflect.DeepEqual(propFields, tt.propogaters) {
			t.Errorf("Expected %s, got %s", tt.propogaters, propFields)
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
	ctx := t.Context()
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

func TestGetLogExporter(t *testing.T) {
	ctx := t.Context()
	// No endpoint -> nil exporter, no error (control-plane log push stays inert).
	exp, err := getLogExporter(ctx, OtelConfig{})
	if err != nil {
		t.Fatalf("getLogExporter() with no endpoint err = %v, want nil", err)
	}
	if exp != nil {
		t.Errorf("getLogExporter() with no endpoint = %#v, want nil", exp)
	}
	// Endpoint set but logs not opted in -> nil (traces alone do not push logs).
	exp, err = getLogExporter(ctx, OtelConfig{endpoint: "localhost:4317", insecure: true})
	if err != nil {
		t.Fatalf("getLogExporter() without logsEnabled err = %v, want nil", err)
	}
	if exp != nil {
		t.Errorf("getLogExporter() without logsEnabled = %#v, want nil (opt-in gate)", exp)
	}
	// Endpoint set AND logs enabled -> a non-nil exporter (creation is lazy).
	exp, err = getLogExporter(ctx, OtelConfig{endpoint: "localhost:4317", insecure: true, logsEnabled: true})
	if err != nil {
		t.Fatalf("getLogExporter() with endpoint+logsEnabled err = %v, want nil", err)
	}
	if exp == nil {
		t.Error("getLogExporter() with endpoint+logsEnabled = nil, want non-nil")
	} else {
		_ = exp.Shutdown(ctx)
	}
}

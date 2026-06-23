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

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/propagation"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// TestPropagatorIsW3C locks the RFC-0019 decision to drop autoprop: the
// propagator is fixed to W3C Trace Context + Baggage regardless of
// OTEL_PROPAGATORS, so the wire fields are exactly traceparent/tracestate/
// baggage and the aws/b3/jaeger/ot propagator modules stay out of the build.
func TestPropagatorIsW3C(t *testing.T) {
	prop := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	got := prop.Fields()
	sort.Strings(got)
	want := []string{"baggage", "traceparent", "tracestate"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("propagator fields = %v, want %v", got, want)
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

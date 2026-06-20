// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package otel

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestErrorBiasedSampler(t *testing.T) {
	t.Parallel()
	params := sdktrace.SamplingParameters{TraceID: trace.TraceID{0x1}, Name: "x"}

	t.Run("upgrades a base Drop to RecordOnly so errors can still be recorded", func(t *testing.T) {
		t.Parallel()
		s := errorBiasedSampler{base: sdktrace.NeverSample()}
		assert.Equal(t, sdktrace.RecordOnly, s.ShouldSample(params).Decision)
	})

	t.Run("leaves a base RecordAndSample untouched", func(t *testing.T) {
		t.Parallel()
		s := errorBiasedSampler{base: sdktrace.AlwaysSample()}
		assert.Equal(t, sdktrace.RecordAndSample, s.ShouldSample(params).Decision)
	})
}

func unsampledSpan(code codes.Code) sdktrace.ReadOnlySpan {
	return tracetest.SpanStub{
		SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: trace.TraceID{0x1}, SpanID: trace.SpanID{0x1}, TraceFlags: 0, // not sampled
		}),
		Status: sdktrace.Status{Code: code},
	}.Snapshot()
}

func sampledSpan(code codes.Code) sdktrace.ReadOnlySpan {
	return tracetest.SpanStub{
		SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: trace.TraceID{0x2}, SpanID: trace.SpanID{0x2}, TraceFlags: trace.FlagsSampled,
		}),
		Status: sdktrace.Status{Code: code},
	}.Snapshot()
}

func TestErrorExportProcessor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		span       sdktrace.ReadOnlySpan
		wantExport bool
	}{
		{name: "unsampled error span is force-exported", span: unsampledSpan(codes.Error), wantExport: true},
		{name: "unsampled ok span is dropped", span: unsampledSpan(codes.Ok), wantExport: false},
		{name: "sampled error span is left to the batch processor", span: sampledSpan(codes.Error), wantExport: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			exp := tracetest.NewInMemoryExporter()
			proc := newErrorExportProcessor(exp, logr.Discard())
			proc.OnEnd(tc.span)
			if tc.wantExport {
				// Export runs off the span-ending goroutine; wait for it.
				require.Eventually(t, func() bool { return len(exp.GetSpans()) == 1 },
					2*time.Second, 5*time.Millisecond)
			} else {
				assert.Empty(t, exp.GetSpans())
			}
		})
	}
}

func TestBaseSamplerFromEnv(t *testing.T) {
	root := sdktrace.SamplingParameters{TraceID: trace.TraceID{0x1}, Name: "x"}

	t.Run("defaults to always-sample when unset", func(t *testing.T) {
		t.Setenv("OTEL_TRACES_SAMPLER", "")
		assert.Equal(t, sdktrace.RecordAndSample, baseSamplerFromEnv().ShouldSample(root).Decision)
	})

	t.Run("honors always_off", func(t *testing.T) {
		t.Setenv("OTEL_TRACES_SAMPLER", "always_off")
		assert.Equal(t, sdktrace.Drop, baseSamplerFromEnv().ShouldSample(root).Decision)
	})

	t.Run("honors traceidratio rate 0 as drop", func(t *testing.T) {
		t.Setenv("OTEL_TRACES_SAMPLER", "traceidratio")
		t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0")
		assert.Equal(t, sdktrace.Drop, baseSamplerFromEnv().ShouldSample(root).Decision)
	})

	t.Run("honors traceidratio rate 1 as sample", func(t *testing.T) {
		t.Setenv("OTEL_TRACES_SAMPLER", "traceidratio")
		t.Setenv("OTEL_TRACES_SAMPLER_ARG", "1")
		assert.Equal(t, sdktrace.RecordAndSample, baseSamplerFromEnv().ShouldSample(root).Decision)
	})
}

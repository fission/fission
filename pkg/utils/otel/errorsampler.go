// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package otel

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/fission/fission/pkg/utils/metrics"
)

const (
	// errorExportConcurrency bounds in-flight force-exports so a mass-failure
	// storm cannot spawn unbounded goroutines or hammer the OTLP endpoint.
	errorExportConcurrency = 8
	// errorExportTimeout bounds a single force-export so a stuck endpoint
	// cannot pin an export goroutine indefinitely.
	errorExportTimeout = 2 * time.Second
)

var (
	// errorSpanExportFailures counts error spans the error-biased exporter
	// failed to send: the RFC-0015 "errors are always traced" guarantee was
	// not met for these, so a nonzero rate means the safety net has a hole.
	errorSpanExportFailures = metrics.Int64Counter(
		"fission_error_span_export_failures_total",
		"Error spans the error-biased exporter failed to send (RFC-0015).",
	)
	// errorSpanExportDrops counts error spans dropped without export because
	// the error-biased exporter was saturated (e.g. a mass-failure storm) —
	// distinct from failures so backpressure is visible separately.
	errorSpanExportDrops = metrics.Int64Counter(
		"fission_error_span_export_drops_total",
		"Error spans dropped without export because the error-biased exporter was saturated (RFC-0015).",
	)
)

// errorBiasedSampler wraps a base head sampler so that failed invocations are
// never lost to sampling (RFC-0015). It keeps the base sampler's sampled/drop
// decision for export, but upgrades a Drop to RecordOnly: the span is still
// built (and OnEnd fires) without being marked sampled, so errorExportProcessor
// can force-export it if it ends in error. Spans the base samples are
// unchanged, so export volume for successful traces stays exactly what the base
// sampler decided.
type errorBiasedSampler struct {
	base sdktrace.Sampler
}

func (s errorBiasedSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	r := s.base.ShouldSample(p)
	if r.Decision == sdktrace.Drop {
		r.Decision = sdktrace.RecordOnly
	}
	return r
}

func (s errorBiasedSampler) Description() string {
	return "ErrorBiased{" + s.base.Description() + "}"
}

// errorExportProcessor force-exports spans that ended in error but were not
// head-sampled, so a failed invocation always has a recorded trace even under
// aggressive head sampling. Head-sampled spans are left to the batch processor.
// Exports run off the span-ending goroutine (bounded concurrency, drop-on-
// saturation) so an error storm cannot serialize a blocking export onto the
// request path; failures and drops are counted. It does NOT own the exporter
// lifecycle (InitProvider shuts the exporter down).
type errorExportProcessor struct {
	exporter sdktrace.SpanExporter
	logger   logr.Logger
	sem      chan struct{}
}

func newErrorExportProcessor(exporter sdktrace.SpanExporter, logger logr.Logger) *errorExportProcessor {
	return &errorExportProcessor{
		exporter: exporter,
		logger:   logger,
		sem:      make(chan struct{}, errorExportConcurrency),
	}
}

func (p *errorExportProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (p *errorExportProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	// Head-sampled spans flow through the BatchSpanProcessor already.
	if s.SpanContext().IsSampled() {
		return
	}
	if s.Status().Code != codes.Error {
		return
	}
	select {
	case p.sem <- struct{}{}:
	default:
		// Saturated (storm): drop rather than block the span-ending goroutine.
		errorSpanExportDrops.Add(context.Background(), 1)
		return
	}
	go func() {
		defer func() { <-p.sem }()
		ctx, cancel := context.WithTimeout(context.Background(), errorExportTimeout)
		defer cancel()
		if err := p.exporter.ExportSpans(ctx, []sdktrace.ReadOnlySpan{s}); err != nil {
			errorSpanExportFailures.Add(context.Background(), 1)
			p.logger.V(1).Info("error-biased span export failed", "error", err.Error())
		}
	}()
}

func (p *errorExportProcessor) Shutdown(context.Context) error   { return nil }
func (p *errorExportProcessor) ForceFlush(context.Context) error { return nil }

// baseSamplerFromEnv builds the head sampler from OTEL_TRACES_SAMPLER /
// OTEL_TRACES_SAMPLER_ARG, honoring the standard values the Helm chart already
// documents (previously these env vars were ignored, so traces were always
// sampled). An unset or unrecognized value defaults to ParentBased(AlwaysSample)
// — the historical behavior — so installs that do not set the env are
// unaffected.
func baseSamplerFromEnv() sdktrace.Sampler {
	ratio := 1.0
	if f, err := strconv.ParseFloat(os.Getenv("OTEL_TRACES_SAMPLER_ARG"), 64); err == nil {
		ratio = f
	}
	switch os.Getenv("OTEL_TRACES_SAMPLER") {
	case "always_on", "parentbased_always_on", "":
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "always_off", "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	case "traceidratio":
		return sdktrace.TraceIDRatioBased(ratio)
	case "parentbased_traceidratio":
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	default:
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package otel

import (
	"context"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/trace"
)

func LoggerWithTraceID(ctx context.Context, logger logr.Logger) logr.Logger {
	if span := trace.SpanContextFromContext(ctx); span.TraceID().IsValid() {
		return logger.WithValues("trace_id", span.TraceID().String())
	}
	return logger
}

// TraceIDFromContext returns the hex trace id from ctx's span context, or ""
// when there is no valid span (e.g. tracing disabled). Used to surface the
// trace id alongside the request id in structured error responses (RFC-0015).
func TraceIDFromContext(ctx context.Context) string {
	if span := trace.SpanContextFromContext(ctx); span.TraceID().IsValid() {
		return span.TraceID().String()
	}
	return ""
}

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

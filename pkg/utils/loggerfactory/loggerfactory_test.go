// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package loggerfactory

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	otellogglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/log/logtest"
	lognoop "go.opentelemetry.io/otel/log/noop"
)

// TestGetLoggerMemoized asserts that GetLogger() returns a logger backed by the
// same underlying sink across calls, proving the base zap core/sampler is
// constructed once and shared rather than re-allocated per call.
func TestGetLoggerMemoized(t *testing.T) {
	a := GetLogger()
	b := GetLogger()

	sinkA := a.GetSink()
	sinkB := b.GetSink()
	require.NotNil(t, sinkA)
	require.NotNil(t, sinkB)

	assert.Equal(t,
		reflect.ValueOf(sinkA).Pointer(),
		reflect.ValueOf(sinkB).Pointer(),
		"GetLogger() should return a logger backed by the same memoized sink",
	)
}

// TestGetLoggerLogsWithoutPanic is a basic smoke test ensuring the memoized
// logger is usable.
func TestGetLoggerLogsWithoutPanic(t *testing.T) {
	logger := GetLogger().WithName("smoke").WithValues("k", "v")
	assert.NotPanics(t, func() {
		logger.Info("smoke test message", "field", "value")
		logger.V(1).Info("debug-ish message")
	})
}

// TestOTLPLogBridge asserts that when OTEL_EXPORTER_OTLP_ENDPOINT is configured,
// the zap core is teed to the OpenTelemetry bridge so each record also reaches
// the global LoggerProvider (RFC-0016 phase 4 control-plane log push).
func TestOTLPLogBridge(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	t.Setenv("OTEL_LOGS_ENABLED", "true")
	rec := logtest.NewRecorder()
	otellogglobal.SetLoggerProvider(rec)
	t.Cleanup(func() { otellogglobal.SetLoggerProvider(lognoop.NewLoggerProvider()) })

	// buildLogger (not the memoized GetLogger) so the bridge is wired with the
	// env set above.
	logger := buildLogger()
	logger.Info("otlp-bridge-marker")

	found := false
	for _, records := range rec.Result() {
		for _, r := range records {
			if strings.Contains(r.Body.AsString(), "otlp-bridge-marker") {
				found = true
			}
		}
	}
	require.True(t, found, "a log emitted via the zap bridge must reach the OTLP LoggerProvider")
}

// TestOTLPLogBridgeDisabled asserts the opt-in gate: with an OTLP endpoint set
// but OTEL_LOGS_ENABLED unset, no bridge is wired — so a trace-only operator's
// logs are not silently pushed on upgrade (backward compatibility).
func TestOTLPLogBridgeDisabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	t.Setenv("OTEL_LOGS_ENABLED", "")
	rec := logtest.NewRecorder()
	otellogglobal.SetLoggerProvider(rec)
	t.Cleanup(func() { otellogglobal.SetLoggerProvider(lognoop.NewLoggerProvider()) })

	logger := buildLogger()
	logger.Info("should-not-bridge")

	for _, records := range rec.Result() {
		assert.Empty(t, records, "without OTEL_LOGS_ENABLED there is no bridge, so the LoggerProvider receives nothing")
	}
}

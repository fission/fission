// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package loggerfactory

import (
	"os"
	"strconv"
	"sync"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/contrib/bridges/otelzap"
	otellogglobal "go.opentelemetry.io/otel/log/global"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	kzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	baseLogger     logr.Logger
	baseLoggerOnce sync.Once
)

// buildLogger constructs the controller-runtime zap logger. DEBUG_ENV is read
// here, at first construction time, and decides Debug vs Info level.
func buildLogger() logr.Logger {
	isDebugEnv, _ := strconv.ParseBool(os.Getenv("DEBUG_ENV"))
	zapLevel := zapcore.InfoLevel
	if isDebugEnv {
		zapLevel = zapcore.DebugLevel
	}

	encConfOpt := func(o *kzap.Options) {
		encTimeFunc := func(encConfig *zapcore.EncoderConfig) {
			encConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		}
		o.EncoderConfigOptions = append(o.EncoderConfigOptions, encTimeFunc)
		o.ZapOpts = append(o.ZapOpts, zap.AddCaller())

		// Control-plane OTLP log push (RFC-0016 phase 4): when an OTLP endpoint
		// is configured AND OTEL_LOGS_ENABLED is set (opt-in, so enabling traces
		// alone does not change log behavior), tee the zap core to an
		// OpenTelemetry bridge so each record is ALSO emitted to the global
		// LoggerProvider — which pkg/utils/otel.InitProvider points at the OTLP
		// exporter (carrying the trace_id). The bridge is gated to the same level
		// as the console so OTLP receives exactly the records stdout does. The
		// env-var names match pkg/utils/otel's constants (hardcoded to avoid an
		// import cycle: that package's tests import this one).
		logsEnabled, _ := strconv.ParseBool(os.Getenv("OTEL_LOGS_ENABLED"))
		if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" && logsEnabled {
			o.ZapOpts = append(o.ZapOpts, zap.WrapCore(func(c zapcore.Core) zapcore.Core {
				bridge := otelzap.NewCore("fission", otelzap.WithLoggerProvider(otellogglobal.GetLoggerProvider()))
				leveled, err := zapcore.NewIncreaseLevelCore(bridge, zapLevel)
				if err != nil {
					leveled = bridge
				}
				return zapcore.NewTee(c, leveled)
			}))
		}
	}

	return kzap.New(kzap.Level(zapLevel), encConfOpt)
}

// GetLogger returns a process-wide shared base logger. The underlying zap core
// (including its sampler) is constructed exactly once and memoized, so every
// caller shares a single sampler instead of allocating a fresh one per call.
// Callers may chain .WithName()/.WithValues(); those derive cheaply without
// allocating a new sampler.
func GetLogger() logr.Logger {
	baseLoggerOnce.Do(func() {
		baseLogger = buildLogger()
	})
	return baseLogger
}

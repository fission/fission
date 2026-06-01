// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package loggerfactory

import (
	"os"
	"strconv"
	"sync"

	"github.com/go-logr/logr"
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
	encConfOpt := func(o *kzap.Options) {
		encTimeFunc := func(encConfig *zapcore.EncoderConfig) {
			encConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		}
		o.EncoderConfigOptions = append(o.EncoderConfigOptions, encTimeFunc)
		o.ZapOpts = append(o.ZapOpts, zap.AddCaller())
	}
	isDebugEnv, _ := strconv.ParseBool(os.Getenv("DEBUG_ENV"))
	level := kzap.Level(zap.InfoLevel)
	if isDebugEnv {
		level = kzap.Level(zap.DebugLevel)
	}
	return kzap.New(level, encConfOpt)
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

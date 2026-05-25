// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package loggerfactory

import (
	"os"
	"strconv"

	"github.com/go-logr/logr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	kzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func GetLogger() logr.Logger {
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

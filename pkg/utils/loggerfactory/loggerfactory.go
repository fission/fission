/*
Copyright 2021 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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

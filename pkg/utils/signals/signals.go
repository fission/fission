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
package signals

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
)

var onlyOneSignalHandler = make(chan struct{})

func SetupSignalHandlerWithContext(logger *zap.Logger) context.Context {
	var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

	close(onlyOneSignalHandler) // panics when called twice

	ctx, cancel := context.WithCancel(context.Background())
	c := make(chan os.Signal, 2)
	signal.Notify(c, shutdownSignals...)
	go func() {
		signal := <-c
		logger.Info("Received signal", zap.String("signal", signal.String()))
		cancel()
		<-c
		panic("multiple signals received")
	}()

	return ctx
}

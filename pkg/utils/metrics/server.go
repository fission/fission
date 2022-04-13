/*
Copyright 2022 The Fission Authors.

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

package metrics

import (
	"context"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

func ServeMetrics(ctx context.Context, logger *zap.Logger) {
	metricsAddr := os.Getenv("METRICS_ADDR")
	if metricsAddr == "" {
		metricsAddr = ":8080"
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	s := &http.Server{
		Addr:    metricsAddr,
		Handler: mux,
	}
	logger.Info("Starting metrics server", zap.String("address", metricsAddr))
	go func() {
		if err := s.ListenAndServe(); err != nil {
			if err != http.ErrServerClosed {
				logger.Error("Metrics server error", zap.Error(err))
			}
		}
	}()
	<-ctx.Done()
	logger.Info("Shutting down metrics server")
	err := s.Shutdown(ctx)
	if err == context.DeadlineExceeded || err == context.Canceled {
		return
	}
	if err != nil {
		logger.Error("Failed to shutdown metrics server", zap.Error(err))
	}
}

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
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/fission/fission/pkg/utils/httpserver"
)

func ServeMetrics(ctx context.Context, parent string, logger *zap.Logger) {
	metricsAddr := os.Getenv("METRICS_ADDR")
	if metricsAddr == "" {
		metricsAddr = "8080"
	}
	err := metrics.Registry.Register(Registry)
	if err != nil {
		logger.Error("failed to register metrics", zap.Error(err))
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(
		metrics.Registry,
		promhttp.HandlerOpts{
			// Opt into OpenMetrics to support exemplars.
			EnableOpenMetrics: true,
		},
	))
	httpserver.StartServer(ctx, logger, parent+"/metrics", metricsAddr, mux)
}

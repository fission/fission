// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"net/http"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/fission/fission/pkg/svcinfo"
	"github.com/fission/fission/pkg/utils/httpserver"
)

func ServeMetrics(ctx context.Context, parent string, logger logr.Logger, mgr *errgroup.Group) {
	metricsAddr := httpserver.BindAddrFromEnv("METRICS_ADDR", svcinfo.PortMetrics)
	err := metrics.Registry.Register(Registry)
	if err != nil {
		logger.Error(err, "failed to register metrics")
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(
		metrics.Registry,
		promhttp.HandlerOpts{
			// Opt into OpenMetrics to support exemplars.
			EnableOpenMetrics: true,
		},
	))
	httpserver.StartServer(ctx, logger, mgr, parent+"/metrics", metricsAddr, mux)
}

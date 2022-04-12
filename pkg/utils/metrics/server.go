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

package metrics

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

type ResponseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
}

var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Number of requests by path, method and status code.",
		},
		[]string{"path", "method", "code"},
	)
	httpRequestDuration = promauto.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "http_requests_duration_seconds",
			Help:       "Time taken to serve the request by path and method.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		[]string{"path", "method"},
	)
	httpRequestInFlight = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Number of requests currently being served by path and method.",
		},
		[]string{"path", "method"},
	)
)

func (rw *ResponseWriterWrapper) WriteHeader(statuscode int) {
	rw.statusCode = statuscode
	rw.ResponseWriter.WriteHeader(statuscode)
}

func HTTPMetricMiddleware() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			labels := make(prometheus.Labels, 0)
			labels["path"] = r.URL.Path
			if route := mux.CurrentRoute(r); route != nil {
				if routePath, err := route.GetPathTemplate(); err == nil {
					labels["path"] = routePath
				}
			}
			labels["method"] = r.Method
			rw := ResponseWriterWrapper{w, http.StatusOK}
			httpRequestInFlight.With(labels).Inc()
			httpRequestDuration := prometheus.NewTimer(httpRequestDuration.With(labels))
			defer func() {
				httpRequestDuration.ObserveDuration()
				httpRequestInFlight.With(labels).Dec()
				labels["code"] = fmt.Sprintf("%d", rw.statusCode)
				httpRequestsTotal.With(labels).Inc()
			}()
			next.ServeHTTP(&rw, r)
		})
	}
}

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

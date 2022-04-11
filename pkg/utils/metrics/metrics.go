package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

var (
	functionLabels = []string{"path", "code"}
	requestsTotal  = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_requests_total",
			Help: "Number of requests",
		},
		functionLabels,
	)
	requestsError = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_requests_error_total",
			Help: "Number of requests failed due to errors",
		},
		functionLabels,
	)
	requestsLatency = promauto.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_requests_seconds",
			Help:       "Time taken to serve the request",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		functionLabels,
	)
)

func IncreaseRequests() {
	requestsTotal.WithLabelValues().Inc()
}

func IncreaseRequestsError() {
	requestsError.WithLabelValues().Inc()
}

func ObserveLatency(time float64) {
	requestsLatency.WithLabelValues().Observe(time)
}

func MonitoringMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()
		h.ServeHTTP(w, r)
		ObserveLatency(time.Since(startTime).Seconds())
		IncreaseRequests()
	})
}

func ServeMetrics(logger *zap.Logger) {
	metricsAddr := ":8080"
	http.Handle("/metrics", promhttp.Handler())
	err := http.ListenAndServe(metricsAddr, nil)

	logger.Fatal("done listening on metrics endpoint", zap.Error(err))
}

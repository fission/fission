package metrics

import (
	"net/http"

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

func IncreaseRequests(path, code string) {
	requestsTotal.WithLabelValues(path, code).Inc()
}

func IncreaseRequestsError(path, code string) {
	requestsError.WithLabelValues(path, code).Inc()
}

func observeLatency(time float64) {
	requestsLatency.WithLabelValues().Observe(time)
}

func ServeMetrics(logger *zap.Logger) {
	metricsAddr := ":8080"
	http.Handle("/metrics", promhttp.Handler())
	err := http.ListenAndServe(metricsAddr, nil)

	logger.Fatal("done listening on metrics endpoint", zap.Error(err))
}

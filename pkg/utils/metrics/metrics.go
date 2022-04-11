package metrics

import (
	"net/http"
	"time"

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

func IncreaseRequests(path string, code int) {
	requestsTotal.WithLabelValues().Inc()
}

func IncreaseRequestsError(path string, code int) {
	requestsError.WithLabelValues().Inc()
}

func ObserveLatency(path string, code int, time float64) {
	requestsLatency.WithLabelValues().Observe(time)
}

func (rw *ResponseWriterWrapper) WriteHeader(statuscode int) {
	rw.statusCode = statuscode
	rw.ResponseWriter.WriteHeader(statuscode)
}

func MonitoringMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := mux.CurrentRoute(r)
		path, _ := route.GetPathTemplate()
		rw := ResponseWriterWrapper{w, http.StatusOK}
		startTime := time.Now()
		h.ServeHTTP(&rw, r)
		ObserveLatency(path, rw.statusCode, time.Since(startTime).Seconds())
		IncreaseRequests(path, rw.statusCode)
		if rw.statusCode >= 400 {
			IncreaseRequestsError(path, rw.statusCode)
		}
	})
}

func ServeMetrics(logger *zap.Logger) {
	metricsAddr := ":8080"
	http.Handle("/metrics", promhttp.Handler())
	err := http.ListenAndServe(metricsAddr, nil)

	logger.Fatal("done listening on metrics endpoint", zap.Error(err))
}

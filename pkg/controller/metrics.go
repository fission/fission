package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	functionLabels = []string{}
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
			Name:       "fission_requests_milliseconds",
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

func observeLatency(time float64) {
	requestsLatency.WithLabelValues().Observe(time)
}

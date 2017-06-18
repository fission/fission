package router

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	metricAddr = ":8080"

	// function http calls
	// cached: true | false, is this function service address cached locally
	// funcname: the function's name
	// funcuid: the function's version id
	// path: the client call the function on which http path
	// code: the http status code
	// method: the function's http method
	httpCalls = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_http_calls_total",
			Help: "How many fission HTTP calls by cached or not, funcname, funcuid, url, HTTP code and method.",
		},
		[]string{"cached", "funcname", "funcuid", "path", "code", "method"},
	)
	httpCallErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_http_callerrors_total",
			Help: "How many fission error during HTTP call labelled by reason.",
		},
		[]string{"reason"},
	)
	httpCallLatencySummary = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_http_call_latency_seconds_summary",
			Help:       "The latency of the http call to target function.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		[]string{"cached", "funcname", "funcuid", "path", "code", "method"},
	)
	httpCallDelaySummary = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_http_call_delay_seconds_summary",
			Help:       "The function call delay caused by fission.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		[]string{"cached", "funcname", "funcuid", "path", "code", "method"},
	)
	httpCallResponseSizeSummary = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_http_call_response_size_bytes_summary",
			Help:       "The response size of the http call to target function.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		[]string{"cached", "funcname", "funcuid", "path", "code", "method"},
	)
)

func init() {
	// Register the function calls counter with Prometheus's default registry.
	prometheus.MustRegister(httpCalls)
	prometheus.MustRegister(httpCallErrors)
	prometheus.MustRegister(httpCallLatencySummary)
	prometheus.MustRegister(httpCallDelaySummary)
	prometheus.MustRegister(httpCallResponseSizeSummary)
}

func increaseHttpCalls(cached, funcname, funcuid, path, code, method string) {
	httpCalls.WithLabelValues(cached, funcname, funcuid, path, code, method).Inc()
}

func increaseHttpCallErrors(reason string) {
	httpCallErrors.WithLabelValues(reason).Inc()
}

func observeHttpCallLatency(cached, funcname, funcuid, path, code, method string, latency float64) {
	httpCallLatencySummary.WithLabelValues(cached, funcname, funcuid, path, code, method).Observe(latency)
}

func observeHttpCallDelay(cached, funcname, funcuid, path, code, method string, delay float64) {
	httpCallDelaySummary.WithLabelValues(cached, funcname, funcuid, path, code, method).Observe(delay)
}

func observeHttpCallResponseSize(cached, funcname, funcuid, path, code, method string, size float64) {
	httpCallResponseSizeSummary.WithLabelValues(cached, funcname, funcuid, path, code, method).Observe(size)
}

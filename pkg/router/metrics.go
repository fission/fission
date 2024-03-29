package router

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/fission/fission/pkg/utils/metrics"
)

var (
	// function + http labels as strings
	labelsStrings = []string{"function_namespace", "function_name", "path", "method", "code"}

	// Function http calls count
	// function_namespace: function namespace
	// function_name: function name
	// code: http status code
	// path: the client call the function on which http path
	// method: the function's http method
	functionCalls = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_function_calls_total",
			Help: "Count of Fission function calls",
		},
		labelsStrings,
	)
	functionCallErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_function_errors_total",
			Help: "Count of Fission function errors",
		},
		labelsStrings,
	)
	functionCallOverhead = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_function_overhead_seconds",
			Help:       "The function call delay caused by fission.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		labelsStrings,
	)
)

func init() {
	registry := metrics.Registry
	registry.MustRegister(functionCalls)
	registry.MustRegister(functionCallErrors)
	registry.MustRegister(functionCallOverhead)
}

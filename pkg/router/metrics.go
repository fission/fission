package router

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
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
	functionCallOverhead = promauto.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_function_overhead_seconds",
			Help:       "The function call delay caused by fission.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		labelsStrings,
	)
)

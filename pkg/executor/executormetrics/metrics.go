package executormetrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// function_name: the function's name
	// function_uid: the function's version id
	// function_address: the address of the pod from which the function was called
	functionLabels = []string{"function_name", "function_namespace"}
	ColdStarts     = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_function_cold_starts_total",
			Help: "How many cold starts are made by function_name, function_uid.",
		},
		functionLabels,
	)
	FuncRunningSummary = promauto.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_function_running_seconds",
			Help:       "The running time (last access - create) in seconds of the function.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		functionLabels,
	)
	FuncError = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_function_cold_start_errors_total",
			Help: "Count of fission cold start errors",
		},
		functionLabels,
	)
)

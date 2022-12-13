/*
Copyright 2022 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/fission/fission/pkg/utils/metrics"
)

var (
	// function_name: the function's name
	// function_uid: the function's version id
	// function_address: the address of the pod from which the function was called
	functionLabels = []string{"function_name", "function_namespace"}
	ColdStarts     = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_function_cold_starts_total",
			Help: "How many cold starts are made by function_name, function_namespace.",
		},
		functionLabels,
	)
	FuncRunningSummary = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_function_running_seconds",
			Help:       "The running time (last access - create) in seconds of the function.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		functionLabels,
	)
	FuncError = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_function_cold_start_errors_total",
			Help: "Count of fission cold start errors",
		},
		functionLabels,
	)
)

func init() {
	registry := metrics.Registry
	registry.MustRegister(ColdStarts)
	registry.MustRegister(FuncRunningSummary)
	registry.MustRegister(FuncError)
}

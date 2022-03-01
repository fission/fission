package fscache

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// function_name: the function's name
	// function_uid: the function's version id
	// function_address: the address of the pod from which the function was called
	functionLabels    = []string{"function_name", "function_uid"}
	functionPodLabels = []string{"function_name", "function_address"}
	coldStarts        = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_function_cold_starts_total",
			Help: "How many cold starts are made by function_name, function_uid.",
		},
		functionLabels,
	)
	funcRunningSummary = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_function_running_seconds",
			Help:       "The running time (last access - create) in seconds of the function.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		functionLabels,
	)
	funcAliveSummary = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_function_alive_seconds",
			Help:       "The alive time in seconds of the function.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		functionLabels,
	)
	funcIsAlive = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_function_is_alive",
			Help: "A binary value indicating is the function_name, function_uid alive",
		},
		functionLabels,
	)
	funcReapTime = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_function_pod_reaptime_seconds",
			Help:       "Amount of seconds to reap a pod",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		functionPodLabels,
	)
	idleTime = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_function_idle_pod_time",
			Help:       "Number of seconds it took for Reaper to detect the pod was idle",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		functionPodLabels,
	)
)

func init() {
	// Register the function calls counter with Prometheus's default registry.
	prometheus.MustRegister(coldStarts)
	prometheus.MustRegister(funcRunningSummary)
	prometheus.MustRegister(funcAliveSummary)
	prometheus.MustRegister(funcIsAlive)
	prometheus.MustRegister(funcReapTime)
	prometheus.MustRegister(idleTime)
}

// IncreaseColdStarts increments the counter by 1.
func (fsc *FunctionServiceCache) IncreaseColdStarts(funcname, funcuid string) {
	coldStarts.WithLabelValues(funcname, funcuid).Inc()
}

func (fsc *FunctionServiceCache) observeFuncRunningTime(funcname, funcuid string, running float64) {
	funcRunningSummary.WithLabelValues(funcname, funcuid).Observe(running)
}

func (fsc *FunctionServiceCache) observeFuncAliveTime(funcname, funcuid string, alive float64) {
	funcAliveSummary.WithLabelValues(funcname, funcuid).Observe(alive)
}

func (fsc *FunctionServiceCache) setFuncAlive(funcname, funcuid string, isAlive bool) {
	count := 0
	if isAlive {
		count = 1
	}
	funcIsAlive.WithLabelValues(funcname, funcuid).Set(float64(count))
}

// ReapTime is the amount of time taken to reap a pod
func (fsc *FunctionServiceCache) ReapTime(funcName, funcAddress string, time float64) {
	funcReapTime.WithLabelValues(funcName, funcAddress).Observe(time)
}

// IdleTime is the amount of time it took Reaper to find out the pod was idle
func (fsc *FunctionServiceCache) IdleTime(funcName, funcAddress string, time float64) {
	idleTime.WithLabelValues(funcName, funcAddress).Observe(time)
}

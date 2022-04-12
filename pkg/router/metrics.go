package router

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var globalFunctionCallCount uint64

type (
	// functionLabels is the set of metrics labels that relate to
	// functions.
	//
	// cached indicates whether or not the function call hit the
	// cache in this service.
	//
	// namespace and name are the metadata of the function.
	functionLabels struct {
		cached    bool
		namespace string
		name      string
	}

	// httpLabels is the set of metrics labels that relate to HTTP
	// requests.
	//
	// host is the host that the HTTP request was made to
	// path is the relative URL of the request
	// method is the HTTP method ("GET", "POST", ...)
	// code is the HTTP status code
	httpLabels struct {
		host   string
		path   string
		method string
		code   int
	}
)

var (
	// function + http labels as strings
	labelsStrings = []string{"function_namespace", "function_name", "path", "method", "code"}

	// Function http calls count
	// cached: true | false, is this function service address cached locally
	// function_namespace: function namespace
	// function_name: function name
	// code: http status code
	// path: the client call the function on which http path
	// method: the function's http method
	functionCalls = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_function_calls_total",
			Help: "Count of Fission function calls",
		},
		labelsStrings,
	)
	functionCallErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_function_errors_total",
			Help: "Count of Fission function errors",
		},
		labelsStrings,
	)
	functionCallOverhead = promauto.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "fission_function_overhead_seconds",
			Help:       "The function call delay caused by fission.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		labelsStrings,
	)
)

func labelsToStrings(f *functionLabels, h *httpLabels) []string {
	return []string{
		f.namespace,
		f.name,
		h.path,
		h.method,
		fmt.Sprint(h.code),
	}
}

func functionCallCompleted(f *functionLabels, h *httpLabels, overhead time.Duration) {
	atomic.AddUint64(&globalFunctionCallCount, 1)

	l := labelsToStrings(f, h)

	// overhead: time from request ingress into router up to proxing into function pod
	functionCallOverhead.WithLabelValues(l...).Observe(float64(overhead.Nanoseconds()) / 1e9)

	// total function call counter
	functionCalls.WithLabelValues(l...).Inc()

	// error counter
	if h.code >= 400 {
		functionCallErrors.WithLabelValues(l...).Inc()
	}
}

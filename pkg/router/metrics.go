// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"time"

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
	functionCallOverhead = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "fission_function_overhead_seconds",
			Help: "The function call delay caused by fission.",
			// Histogram instead of Summary: a summary keeps a per-series quantile
			// stream, and with these high-cardinality labels that was the largest
			// router heap consumer. Buckets are fixed-size and aggregatable across
			// replicas. Quantiles are derived with histogram_quantile().
			Buckets: prometheus.DefBuckets,
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

// collectFunctionMetric records the per-call counters and the
// Fission-attributed overhead histogram. Pure observation: the cached-address
// tap that historically hid in here now fires from the ModifyResponse hook and
// the proxy error handler, with identical ordering.
func (fh functionHandler) collectFunctionMetric(start time.Time, rrt *RetryingRoundTripper, req *http.Request, resp *http.Response) {
	duration := time.Since(start)
	var path string

	if fh.httpTrigger != nil {
		if fh.httpTrigger.Spec.Prefix != nil && *fh.httpTrigger.Spec.Prefix != "" {
			path = *fh.httpTrigger.Spec.Prefix
		} else {
			path = fh.httpTrigger.Spec.RelativeURL
		}
	}

	functionCalls.WithLabelValues(fh.function.ObjectMeta.Namespace,
		fh.function.ObjectMeta.Name, path, req.Method,
		fmt.Sprint(resp.StatusCode)).Inc()

	if resp.StatusCode >= 400 {
		functionCallErrors.WithLabelValues(fh.function.ObjectMeta.Namespace,
			fh.function.ObjectMeta.Name, path, req.Method,
			fmt.Sprint(resp.StatusCode)).Inc()
	}

	functionCallOverhead.WithLabelValues(fh.function.ObjectMeta.Namespace,
		fh.function.ObjectMeta.Name, path, req.Method,
		fmt.Sprint(resp.StatusCode)).
		Observe(float64(duration.Nanoseconds()) / 1e9)

	fh.logger.V(1).Info("Request complete", "function", fh.function.Name,
		"retry", rrt.totalRetry, "total-time", duration,
		"content-length", resp.ContentLength)
}

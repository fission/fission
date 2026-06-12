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

	// Route-update observability (RFC-0013). muxRebuilds is the headline:
	// in incremental mode it must NOT move under canary-weight / function
	// churn — only shape changes (trigger create/delete/path edits) and the
	// legacy mode's full rebuilds increment it.
	routeTableApplies = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_router_route_table_applies_total",
			Help: "Route table applications by result (no_change, handler_swapped, shape_changed, rejected).",
		},
		[]string{"result"},
	)
	muxRebuilds = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_router_mux_rebuilds_total",
			Help: "Full mux rebuilds by listener and reason (shape_change for the incremental materializer, legacy for the full-rebuild mode).",
		},
		[]string{"listener", "reason"},
	)
	routesTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_router_routes",
			Help: "Routes currently in the route table (public = HTTP triggers, internal = functions).",
		},
		[]string{"listener"},
	)
	resyncDrift = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "fission_router_route_resync_drift_total",
			Help: "Routes the periodic resync had to correct — a nonzero value means a watch event was missed (the table self-healed).",
		},
	)
	// The drift guard's own failure modes must be observable: the safety
	// story of the incremental path rests on the resync, so a resync that
	// cannot run (or a materialize that cannot build) needs an alertable
	// signal beyond a log line.
	resyncFailures = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "fission_router_route_resync_failures_total",
			Help: "Resync passes that failed (list error or per-trigger apply errors); the drift guard was unable to verify the table this tick.",
		},
	)
	materializeFailures = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "fission_router_mux_materialize_failures_total",
			Help: "Mux materializations that failed before the swap; the served mux is stale until a retry succeeds (the resync loop re-arms it).",
		},
	)
)

func init() {
	registry := metrics.Registry
	registry.MustRegister(functionCalls)
	registry.MustRegister(functionCallErrors)
	registry.MustRegister(functionCallOverhead)
	registry.MustRegister(routeTableApplies)
	registry.MustRegister(muxRebuilds)
	registry.MustRegister(routesTotal)
	registry.MustRegister(resyncDrift)
	registry.MustRegister(resyncFailures)
	registry.MustRegister(materializeFailures)
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

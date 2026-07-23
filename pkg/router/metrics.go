// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/correlation"
	"github.com/fission/fission/pkg/utils/metrics"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

var (
	// Function http call counters and the Fission-attributed overhead histogram,
	// labelled by function_namespace, function_name, function_version
	// (RFC-0025: the resolved fv1.FUNCTION_VERSION label, empty for an
	// unversioned invocation), path (the client-called http path), method,
	// and code (http status). Recorded on the request path with the request
	// context, so the overhead histogram carries trace exemplars when the
	// invocation is sampled.
	functionCalls = metrics.Int64Counter(
		"fission_function_calls_total",
		"Count of Fission function calls",
	)
	functionCallErrors = metrics.Int64Counter(
		"fission_function_errors_total",
		"Count of Fission function errors",
	)
	// Histogram instead of Summary: a summary keeps a per-series quantile
	// stream, and with these high-cardinality labels that was the largest
	// router heap consumer. Buckets stay prometheus.DefBuckets verbatim so the
	// exposed series are unchanged; quantiles are derived with
	// histogram_quantile().
	functionCallOverhead = metrics.Float64Histogram(
		"fission_function_overhead_seconds",
		"The function call delay caused by fission.",
		prometheus.DefBuckets,
	)

	// Sticky routing observability (RFC-0023 phase 3), labelled by
	// function_namespace/function_name. A "hit" is a request that carried a
	// sticky key into the resolver; "fallback" counts requests whose sticky
	// declaration matched nothing in the request (missing header/param), which
	// silently take the default pick — the metric is how an operator notices a
	// misdeclared key source. Per-key ownership moves are deliberately NOT
	// counted: that would need per-key memory in the router; reshuffle is
	// churn-driven and observable from pod events.
	stickyKeyRequests = metrics.Int64Counter(
		"fission_router_sticky_requests_total",
		"Requests to sticky-routed functions that carried their sticky key",
	)
	stickyKeyMissing = metrics.Int64Counter(
		"fission_router_sticky_key_missing_total",
		"Requests to sticky-routed functions missing their sticky key (default pick used)",
	)

	// Route-update observability (RFC-0013). muxRebuilds is the headline:
	// it must NOT move under canary-weight / function churn — only route
	// shape changes (trigger create/delete/path edits) increment it.
	routeTableApplies = metrics.Int64Counter(
		"fission_router_route_table_applies_total",
		"Route table applications by result (no_change, handler_swapped, shape_changed, rejected).",
	)
	muxRebuilds = metrics.Int64Counter(
		"fission_router_mux_rebuilds_total",
		"Full mux rebuilds by listener and reason (shape_change when the route-table materializer rebuilds a listener mux).",
	)
	routesTotal = metrics.Int64Gauge(
		"fission_router_routes",
		"Routes currently in the route table (public = HTTP triggers, internal = functions).",
	)
	resyncDrift = metrics.Int64Counter(
		"fission_router_route_resync_drift_total",
		"Routes the periodic resync had to correct — a nonzero value means a watch event was missed (the table self-healed).",
	)
	// The drift guard's own failure modes must be observable: the safety
	// story of the incremental path rests on the resync, so a resync that
	// cannot run (or a materialize that cannot build) needs an alertable
	// signal beyond a log line.
	resyncFailures = metrics.Int64Counter(
		"fission_router_route_resync_failures_total",
		"Resync passes that failed (list error or per-trigger apply errors); the drift guard was unable to verify the table this tick.",
	)
	materializeFailures = metrics.Int64Counter(
		"fission_router_mux_materialize_failures_total",
		"Mux materializations that failed before the swap; the served mux is stale until a retry succeeds (the resync loop re-arms it).",
	)
	// Failure attribution (RFC-0015). component: router|executor|fetcher|
	// function|timeout; reason: a stable taxonomy value. A spike in executor/*
	// is an alertable platform problem, vs function/* (user code). Client
	// disconnects (499) are excluded as benign churn.
	invocationFailures = metrics.Int64Counter(
		"fission_invocation_failures_total",
		"Count of failed function invocations attributed by component and reason (RFC-0015).",
	)
)

// functionCallAttrsCache memoizes the metric.MeasurementOption (which wraps a
// sorted, deduped attribute.Set) per (namespace,name,version,path,method,code)
// so the warm path does a comparable-array map lookup — allocating nothing —
// instead of building and sorting a 6-attribute Set on every request. Mirrors
// pmcAttrs in pkg/utils/metrics. `path` is the trigger pattern (not the raw
// request URL), so the cache's cardinality tracks the metric series this
// option already feeds. `version` is fv1.FUNCTION_VERSION off the resolved
// function (RFC-0025) — empty for an unversioned invocation, which the
// Prometheus bridge exposes as an absent label (empty == absent in TSDB
// series identity), so unversioned series are unaffected by this addition.
var functionCallAttrsCache sync.Map // [6]string{namespace, name, version, path, method, code} -> metric.MeasurementOption

func functionCallAttrs(namespace, name, version, path, method string, code int) metric.MeasurementOption {
	codeStr := strconv.Itoa(code)
	key := [6]string{namespace, name, version, path, method, codeStr}
	if v, ok := functionCallAttrsCache.Load(key); ok {
		return v.(metric.MeasurementOption)
	}
	opt := metric.WithAttributes(
		attribute.String("function_namespace", namespace),
		attribute.String("function_name", name),
		attribute.String("function_version", version),
		attribute.String("path", path),
		attribute.String("method", method),
		attribute.String("code", codeStr),
	)
	functionCallAttrsCache.Store(key, opt)
	return opt
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

	// Same label set for all three; recorded with the request context so the
	// overhead histogram attaches a trace exemplar on sampled invocations.
	ctx := req.Context()
	version := fh.function.Labels[fv1.FUNCTION_VERSION]
	attrs := functionCallAttrs(fh.function.Namespace, fh.function.Name, version, path, req.Method, resp.StatusCode)

	functionCalls.Add(ctx, 1, attrs)
	if resp.StatusCode >= 400 {
		functionCallErrors.Add(ctx, 1, attrs)
	}
	functionCallOverhead.Record(ctx, float64(duration.Nanoseconds())/1e9, attrs)

	fh.logger.V(1).Info("Request complete", "function", fh.function.Name,
		"retry", rrt.totalRetry, "total-time", duration,
		"content-length", resp.ContentLength)

	fh.logAccessRecord(rrt, req, resp, path, duration)
}

// logAccessRecord emits one structured per-invocation record (RFC-0016) — the
// request id, trace id, function identity, chosen backend, status, and latency
// — to stdout, where an external log collector ingests it. It is the
// correlation key that lets `fission function logs --request-id <id>` resolve
// an invocation to its function and time window. Off by default
// (DISPLAY_ACCESS_LOG / router.displayAccessLog) so it adds no per-request log
// volume unless an operator opts into log-based correlation.
func (fh functionHandler) logAccessRecord(rrt *RetryingRoundTripper, req *http.Request, resp *http.Response, path string, duration time.Duration) {
	if !fh.accessLog {
		return
	}
	var backend string
	if rrt.serviceURL != nil {
		backend = rrt.serviceURL.Host
	}
	ctx := req.Context()
	fh.logger.Info("function access",
		"fission.request.id", correlation.FromContext(ctx),
		"trace_id", otelUtils.TraceIDFromContext(ctx),
		"fission.function.name", fh.function.Name,
		"fission.function.uid", string(fh.function.UID),
		"fission.function.namespace", fh.function.Namespace,
		"http.method", req.Method,
		"http.path", path,
		"http.status_code", resp.StatusCode,
		"backend", backend,
		"retry", rrt.totalRetry,
		"duration_ms", duration.Milliseconds(),
	)
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/fission/fission/pkg/utils/metrics"
)

var (
	functionLabels = func(name, namespace string) metric.MeasurementOption {
		return metric.WithAttributes(
			attribute.String("function_name", name),
			attribute.String("function_namespace", namespace),
		)
	}

	coldStarts = metrics.Int64Counter(
		"fission_function_cold_starts_total",
		"How many cold starts are made by function_name, function_namespace.",
	)
	// Histogram instead of Summary: avoids the per-series quantile stream
	// memory; quantiles are derived with histogram_quantile(). This is a
	// function lifetime (seconds to hours), so the buckets stay exponential
	// from 1s to ~9h verbatim rather than DefBuckets (which top out at 10s).
	funcRunningSeconds = metrics.Float64Histogram(
		"fission_function_running_seconds",
		"The running time (last access - create) in seconds of the function.",
		prometheus.ExponentialBuckets(1, 2, 16),
	)
	coldStartErrors = metrics.Int64Counter(
		"fission_function_cold_start_errors_total",
		"Count of fission cold start errors",
	)
	// functionServiceEnsures counts per-function Service ensure outcomes
	// (RFC-0002 EndpointSlice data plane). No function-name labels by design —
	// same cardinality discipline as the router metrics.
	functionServiceEnsures = metrics.Int64Counter(
		"fission_executor_function_service_ensures_total",
		"Count of per-function Service ensure operations by result (created|updated|exists|error).",
	)
	// ociPoolsReaped counts per-image pool deployments destroyed by the idle
	// pool reaper (RFC-0012). The Gate C signal: at many-package scale this
	// moving (and warm-pod count staying bounded) is the design working.
	ociPoolsReaped = metrics.Int64Counter(
		"fission_executor_oci_pools_reaped_total",
		"Per-image (OCI) warm pools destroyed by the idle pool reaper.",
	)
	// ociPoolReapFailures counts reap passes whose deployment delete failed
	// (the pool entry is dropped and the deployment orphaned until adoption
	// or restart cleanup) — kept separate so ociPoolsReaped never lies.
	ociPoolReapFailures = metrics.Int64Counter(
		"fission_executor_oci_pool_reap_failures_total",
		"Idle-pool reap attempts whose deployment delete failed (deployment orphaned until adoption or restart cleanup).",
	)
)

// RecordColdStart counts one cold start for the function.
func RecordColdStart(ctx context.Context, fnName, fnNamespace string) {
	coldStarts.Add(ctx, 1, functionLabels(fnName, fnNamespace))
}

// RecordColdStartError counts one cold-start failure for the function.
func RecordColdStartError(ctx context.Context, fnName, fnNamespace string) {
	coldStartErrors.Add(ctx, 1, functionLabels(fnName, fnNamespace))
}

// ObserveFunctionRunningSeconds records a function's lifetime (last access -
// create) in seconds.
func ObserveFunctionRunningSeconds(ctx context.Context, fnName, fnNamespace string, seconds float64) {
	funcRunningSeconds.Record(ctx, seconds, functionLabels(fnName, fnNamespace))
}

// RecordFunctionServiceEnsure counts one Service-ensure outcome by result
// (created|updated|exists|error).
func RecordFunctionServiceEnsure(ctx context.Context, result string) {
	functionServiceEnsures.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

// RecordOCIPoolReaped counts one idle per-image pool destroyed by the reaper.
func RecordOCIPoolReaped(ctx context.Context) {
	ociPoolsReaped.Add(ctx, 1)
}

// RecordOCIPoolReapFailure counts one reap pass whose deployment delete failed.
func RecordOCIPoolReapFailure(ctx context.Context) {
	ociPoolReapFailures.Add(ctx, 1)
}

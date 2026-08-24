// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/metrics"
)

func functionLabels(name, namespace string) metric.MeasurementOption {
	return metric.WithAttributes(
		attribute.String("function_name", name),
		attribute.String("function_namespace", namespace),
	)
}

var (
	coldStarts = metrics.Int64Counter(
		"fission_function_cold_starts_total",
		"How many cold starts are made by function_name, function_namespace.",
	)
	specializationsRejected = metrics.Int64Counter(
		"fission_executor_specializations_rejected_total",
		"Specialization requests rejected at a capacity bound (concurrency cap or in-flight limit), by function_name, function_namespace.",
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

	// idleReapLagSeconds measures reaper RESPONSIVENESS, not idleness: how long a
	// function service sat past its idle deadline before the reaper acted. A
	// function idle for an hour under a one-hour timeout was reaped on time and
	// records ~0. Its _count is the reap volume, so no companion counter exists.
	// Buckets reach ~34m; a healthy reaper lands inside its tick interval (5s).
	// Labelled by executor_type only — the router metrics' cardinality discipline.
	idleReapLagSeconds = metrics.Float64Histogram(
		"fission_executor_idle_reap_lag_seconds",
		"Delay between a function service becoming eligible for idle reaping (last access + idle timeout) and the reaper acting on it, by executor_type.",
		prometheus.ExponentialBuckets(0.25, 2, 14),
	)
	// idleReapErrors counts idle-reaping failures by stage. The stage attribute is
	// load-bearing: a reaper whose Prepare List calls fail aborts the whole pass
	// before any candidate is considered, which is the likeliest way for reaping to
	// stop entirely. Counting only per-candidate errors would leave that case
	// indistinguishable from a quiet cluster — every series flat at zero.
	idleReapErrors = metrics.Int64Counter(
		"fission_executor_idle_reap_errors_total",
		"Idle reaping failures by executor_type and stage (prepare|list|reap).",
	)

	fissionProvisionedTarget = metrics.Int64Gauge(
		"fission_provisioned_target",
		"Desired provisioned-concurrency warm-pod count, by function_name, function_namespace.",
	)

	fissionProvisionedReady = metrics.Int64Gauge(
		"fission_provisioned_ready",
		"Current provisioned-concurrency warm-pod count, by function_name, function_namespace.",
	)

	fissionEagerSpecializations = metrics.Int64Counter(
		"fission_provisioned_eager_specializations_total",
		"The number of provisioned eager specializations attempts by outcome (success|error) for each function, function_namespace.",
	)
	fissionProvisionedWindowTransitions = metrics.Int64Counter(
		"fission_provisioned_window_transitions_total",
		"The number of provisioned window transitions for each function, function_namespace.",
	)
)

func RecordProvisionedWindowTransition(ctx context.Context, fnName, fnNamespace string) {
	fissionProvisionedWindowTransitions.Add(ctx, 1, functionLabels(fnName, fnNamespace))
}

func RecordEagerSpecialization(ctx context.Context, fnName, fnNamespace string, outcome string) {
	fissionEagerSpecializations.Add(ctx, 1, functionLabels(fnName, fnNamespace), metric.WithAttributes(
		attribute.String("outcome", outcome),
	))

}

func RecordProvisionedTarget(ctx context.Context, fnName, fnNamespace string, target int64) {
	fissionProvisionedTarget.Record(ctx, target, functionLabels(fnName, fnNamespace))
}

func RecordProvisionedReady(ctx context.Context, fnName, fnNamespace string, ready int64) {
	fissionProvisionedReady.Record(ctx, ready, functionLabels(fnName, fnNamespace))
}

// RecordSpecializationRejected counts one capacity-gated specialization
// rejection (concurrency cap or in-flight-specialization bound); the router
// relays these to clients as 429s.
func RecordSpecializationRejected(ctx context.Context, fnName, fnNamespace string) {
	specializationsRejected.Add(ctx, 1, functionLabels(fnName, fnNamespace))
}

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

// ReapStage is where in a reaping pass a failure happened.
type ReapStage string

const (
	// ReapStagePrepare is the per-tick setup (the cross-namespace List calls).
	// A failure here skips the entire pass.
	ReapStagePrepare ReapStage = "prepare"
	// ReapStageList is listing the idle candidates; also skips the pass.
	ReapStageList ReapStage = "list"
	// ReapStageReap is one candidate's own reap.
	ReapStageReap ReapStage = "reap"
)

// Reaper attribute sets are precomputed. Both dimensions are closed — three
// executor types, three stages — so the twelve possible option values are built
// once at init instead of allocating a fresh attribute set on every record.
// Measured on the error path, which combines both: 528ns/640B/11 allocs before,
// and the reaper records under a shared semaphore where allocation churn is the
// part worth not paying.
var (
	reapExecutorAttrs = func() map[fv1.ExecutorType]metric.MeasurementOption {
		m := map[fv1.ExecutorType]metric.MeasurementOption{}
		for _, et := range []fv1.ExecutorType{fv1.ExecutorTypePoolmgr, fv1.ExecutorTypeNewdeploy, fv1.ExecutorTypeContainer} {
			m[et] = metric.WithAttributes(attribute.String("executor_type", string(et)))
		}
		return m
	}()
	reapErrorAttrs = func() map[fv1.ExecutorType]map[ReapStage]metric.MeasurementOption {
		m := map[fv1.ExecutorType]map[ReapStage]metric.MeasurementOption{}
		for _, et := range []fv1.ExecutorType{fv1.ExecutorTypePoolmgr, fv1.ExecutorTypeNewdeploy, fv1.ExecutorTypeContainer} {
			m[et] = map[ReapStage]metric.MeasurementOption{}
			for _, st := range []ReapStage{ReapStagePrepare, ReapStageList, ReapStageReap} {
				m[et][st] = metric.WithAttributes(
					attribute.String("executor_type", string(et)),
					attribute.String("stage", string(st)),
				)
			}
		}
		return m
	}()
)

// executorTypeAttrs returns the precomputed option, falling back to an allocated
// one for an executor type added later without updating the table above.
func executorTypeAttrs(executorType fv1.ExecutorType) metric.MeasurementOption {
	if opt, ok := reapExecutorAttrs[executorType]; ok {
		return opt
	}
	return metric.WithAttributes(attribute.String("executor_type", string(executorType)))
}

// RecordIdleReap records one completed idle reap, as how far past its idle
// deadline the function service was when the reaper acted. The histogram's
// _count carries the reap volume, so there is no separate counter to drift.
func RecordIdleReap(ctx context.Context, executorType fv1.ExecutorType, lagSeconds float64) {
	idleReapLagSeconds.Record(ctx, lagSeconds, executorTypeAttrs(executorType))
}

// RecordIdleReapError counts one idle-reaping failure at the given stage.
func RecordIdleReapError(ctx context.Context, executorType fv1.ExecutorType, stage ReapStage) {
	if byStage, ok := reapErrorAttrs[executorType]; ok {
		if opt, ok := byStage[stage]; ok {
			idleReapErrors.Add(ctx, 1, opt)
			return
		}
	}
	idleReapErrors.Add(ctx, 1, metric.WithAttributes(
		attribute.String("executor_type", string(executorType)),
		attribute.String("stage", string(stage)),
	))
}

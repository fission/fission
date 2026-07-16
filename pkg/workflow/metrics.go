// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/fission/fission/pkg/utils/metrics"
)

// Metrics are labeled by workflow and state name ONLY — never by run UID or
// any per-run value: unbounded label values mint unbounded series (the
// RFC-0027 lesson).
var (
	runsTotal = metrics.Int64Counter(
		"fission_workflow_runs_total",
		"Workflow runs reaching a terminal phase, by workflow and phase.",
	)
	stepDuration = metrics.Float64Histogram(
		"fission_workflow_step_duration_seconds",
		"Task step attempt duration (invocation round trip), by workflow state and outcome.",
		[]float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300},
	)
	activeRuns = metrics.Int64UpDownCounter(
		"fission_workflow_active_runs",
		"Runs currently executing (started, not yet terminal).",
	)
)

func recordRunStarted(ctx context.Context) {
	activeRuns.Add(ctx, 1)
}

func recordRunTerminal(ctx context.Context, workflow string, phase string) {
	activeRuns.Add(ctx, -1)
	runsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("workflow", workflow),
		attribute.String("phase", phase),
	))
}

func recordStepDuration(ctx context.Context, state string, outcome string, d time.Duration) {
	stepDuration.Record(ctx, d.Seconds(), metric.WithAttributes(
		attribute.String("state", state),
		attribute.String("outcome", outcome),
	))
}

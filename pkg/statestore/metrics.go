// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/fission/fission/pkg/utils/metrics"
)

// Statestore metrics (RFC-0019 OTel meters). Instruments are package-level vars
// bound to the global meter; they work before the provider is installed because
// the OTel global delegates retroactively.
var (
	opsTotal = metrics.Int64Counter(
		"fission_statestore_ops_total",
		"Statestore operations, by capability and op.",
	)
	errorsTotal = metrics.Int64Counter(
		"fission_statestore_errors_total",
		"Statestore operations that returned an error, by capability and op.",
	)
	quotaRejectionsTotal = metrics.Int64Counter(
		"fission_statestore_quota_rejections_total",
		"Writes rejected by a scope quota, by reason.",
	)
)

// conservationReporters is the set of live Queue drivers whose message
// accounting feeds the conservation drift gauge. A single observable callback
// sums over it, so N scoped wrappers do not register N double-observing
// callbacks.
var (
	reportersMu sync.Mutex
	reporters   []ConservationReporter
)

func registerConservationReporter(r ConservationReporter) {
	if r == nil {
		return
	}
	reportersMu.Lock()
	defer reportersMu.Unlock()
	reporters = append(reporters, r)
}

func sumConservationDrift() int64 {
	reportersMu.Lock()
	defer reportersMu.Unlock()
	var drift int64
	for _, r := range reporters {
		drift += r.ConservationStats().Drift()
	}
	return drift
}

// conservationDriftGauge is the T1 runtime gate: it must read zero. It is
// registered once at package init and reads every live reporter.
var _ = metrics.Int64ObservableGauge(
	"fission_statestore_conservation_drift",
	"Queue conservation drift (enqueued - inflight - acked - dead); must be zero.",
	func(_ context.Context, o metric.Int64Observer) error {
		o.Observe(sumConservationDrift())
		return nil
	},
)

func recordOp(ctx context.Context, capability, op string) {
	opsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("capability", capability),
		attribute.String("op", op),
	))
}

func recordError(ctx context.Context, capability, op string) {
	errorsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("capability", capability),
		attribute.String("op", op),
	))
}

func recordQuotaRejection(ctx context.Context, reason string) {
	quotaRejectionsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("reason", reason),
	))
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/utils/metrics"
)

// Async invocation counters (RFC-0024 observability, RFC-0019 OTel meters). The
// conservation gauge (invariant A5) is emitted server-side by the statestore
// service over its own store, so it is not duplicated here.
var (
	asyncDeliveries = metrics.Int64Counter("fission_async_deliveries_total",
		"Count of async invocation delivery attempts, labeled by response condition")
	asyncRetries = metrics.Int64Counter("fission_async_retries_total",
		"Count of async invocation deliveries requeued for a retry")
	asyncDLQ = metrics.Int64Counter("fission_async_dlq_total",
		"Count of async invocations dead-lettered, labeled by reason")
	asyncDestinations = metrics.Int64Counter("fission_async_destinations_total",
		"Count of async destination fires, labeled by outcome (enqueued/dropped/depth_capped/...)")
	asyncDepthCap = metrics.Int64Counter("fission_async_depth_cap_total",
		"Count of async destination invocations dropped for exceeding the chain depth cap (A6)")
)

func recordDelivery(ctx context.Context, condition string) {
	asyncDeliveries.Add(ctx, 1, metric.WithAttributes(attribute.String("condition", condition)))
}

func recordRetry(ctx context.Context) {
	asyncRetries.Add(ctx, 1)
}

func recordDLQ(ctx context.Context, reason string) {
	asyncDLQ.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

func recordDestination(ctx context.Context, outcome string) {
	asyncDestinations.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

func recordDepthCap(ctx context.Context) {
	asyncDepthCap.Add(ctx, 1)
}

// deliveryCondition classifies a DeliveryResult for the deliveries_total label:
// the raw response class of one delivery attempt (distinct from the settle
// action, which classify() decides).
func deliveryCondition(res DeliveryResult) string {
	switch {
	case res.Err != nil:
		return "transport_error"
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return "2xx"
	case res.StatusCode >= 400 && res.StatusCode < 500:
		return "4xx"
	case res.StatusCode >= 500:
		return "5xx"
	default:
		return "other"
	}
}

// RegisterQueueGauges registers the async depth and oldest-age observable gauges,
// read from q.Stats(queueName) on each metrics collection. Call it once at router
// start (only when async invocation is enabled).
func RegisterQueueGauges(q statestore.Queue, queueName string) {
	metrics.Int64ObservableGauge("fission_async_queue_depth",
		"Async invocation queue depth: visible messages awaiting delivery",
		observeStat(q, queueName, func(st statestore.QueueStats) int64 { return st.Visible }))
	metrics.Int64ObservableGauge("fission_async_oldest_age_seconds",
		"Age in seconds of the oldest visible async invocation (0 when none)",
		observeStat(q, queueName, func(st statestore.QueueStats) int64 {
			return int64(st.OldestVisibleAge.Seconds())
		}))
}

// observeStat builds an observable-gauge callback that reads q.Stats(queueName)
// and observes one field. A Stats read failure is RETURNED (routed to
// otel.Handle) rather than swallowed: the OTel SDK skips only this instrument's
// observation and continues the collection, so the scrape still succeeds AND the
// failure produces a signal instead of the gauge silently freezing at zero.
func observeStat(q statestore.Queue, queueName string, pick func(statestore.QueueStats) int64) metric.Int64Callback {
	return func(ctx context.Context, o metric.Int64Observer) error {
		st, err := q.Stats(ctx, queueName)
		if err != nil {
			return err
		}
		o.Observe(pick(st))
		return nil
	}
}

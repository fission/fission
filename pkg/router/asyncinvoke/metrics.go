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
// start (only when async invocation is enabled). A per-scrape Stats read failure
// is swallowed so a transient statestore blip does not fail the whole scrape.
func RegisterQueueGauges(q statestore.Queue, queueName string) {
	metrics.Int64ObservableGauge("fission_async_queue_depth",
		"Async invocation queue depth: visible messages awaiting delivery",
		func(ctx context.Context, o metric.Int64Observer) error {
			st, err := q.Stats(ctx, queueName)
			if err != nil {
				return nil
			}
			o.Observe(st.Visible)
			return nil
		})
	metrics.Int64ObservableGauge("fission_async_oldest_age_seconds",
		"Age in seconds of the oldest visible async invocation (0 when none)",
		func(ctx context.Context, o metric.Int64Observer) error {
			st, err := q.Stats(ctx, queueName)
			if err != nil {
				return nil
			}
			o.Observe(int64(st.OldestVisibleAge.Seconds()))
			return nil
		})
}

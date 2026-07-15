// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/fission/fission/pkg/utils/metrics"
)

// RFC-0027 eventing consumer meters (RFC-0019 style). Publishes are counted by
// mqpub (fission_eventing_published_total); this side counts deliveries,
// retries, error-topic routing, and retention — every drop path is countable.
var (
	eventingDelivered = metrics.Int64Counter("fission_eventing_delivered_total",
		"Count of topic-event deliveries reaching terminal handling, labeled by condition (success/exhausted)")
	eventingRetries = metrics.Int64Counter("fission_eventing_retries_total",
		"Count of topic-event delivery retries")
	eventingErrorTopic = metrics.Int64Counter("fission_eventing_errortopic_total",
		"Count of exhausted events routed to the error topic, labeled by outcome (published/error/dropped — dropped = no error topic configured)")
	eventingTrimmed = metrics.Int64Counter("fission_eventing_trimmed_total",
		"Count of topic events trimmed by retention, labeled by reason (mincursor/age/size)")
)

func recordDelivery(ctx context.Context, condition string) {
	eventingDelivered.Add(ctx, 1, metric.WithAttributes(attribute.String("condition", condition)))
}

func recordRetry(ctx context.Context) {
	eventingRetries.Add(ctx, 1)
}

func recordErrorTopic(ctx context.Context, outcome string) {
	eventingErrorTopic.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

func recordTrimmed(ctx context.Context, reason string, n int64) {
	eventingTrimmed.Add(ctx, n, metric.WithAttributes(attribute.String("reason", reason)))
}

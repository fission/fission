// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqpub

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/fission/fission/pkg/utils/metrics"
)

// eventingPublished counts topic publishes (RFC-0027 observability, RFC-0019
// meters): every outcome is labeled, so a dropped publish is countable, never
// silent.
var eventingPublished = metrics.Int64Counter("fission_eventing_published_total",
	"Count of topic publishes, labeled by provider and outcome (published/error/invalid/capped/unsupported)")

func recordPublish(ctx context.Context, provider, outcome string) {
	eventingPublished.Add(ctx, 1, metric.WithAttributes(
		attribute.String("provider", provider),
		attribute.String("outcome", outcome),
	))
}

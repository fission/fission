// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/fission/fission/pkg/utils/metrics"
)

// Egress-side meter (RFC-0019 style). Enqueues are counted by mqpub
// (fission_eventing_published_total{outcome=enqueued}); this counts what the
// broker heads did with the jobs. The dead-letter tail is covered by the
// queue's own DLQ metrics and admin surface (E4: every drop is countable).
var eventingEgress = metrics.Int64Counter("fission_eventing_egress_total",
	"Count of broker egress job outcomes (published/retry/malformed)")

func recordEgress(ctx context.Context, outcome string) {
	eventingEgress.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

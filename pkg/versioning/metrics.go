// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/fission/fission/pkg/utils/metrics"
)

// Result labels for fission_autopublish_total. Deliberately bounded to these
// three: an "error" bucket was considered and rejected (plan review) because
// Go error values are unbounded — that would make the label itself a
// cardinality leak. Reconcile errors still surface through the normal
// controller-runtime error-count/backoff machinery instead.
const (
	autopublishResultCreated   = "created"
	autopublishResultUnchanged = "unchanged"
	autopublishResultDeferred  = "deferred"
)

// autopublishTotal counts RFC-0025 auto-publish reconcile outcomes: a new
// FunctionVersion minted (created), a runtime-affecting check that matched
// the newest existing version or an idempotent Publish call (unchanged), or
// a runtime-affecting change deferred behind a not-yet-build-ready Package
// (deferred).
var autopublishTotal = metrics.Int64Counter(
	"fission_autopublish_total",
	"RFC-0025 auto-publish reconcile outcomes by result (created|unchanged|deferred).",
)

// recordAutopublish counts one auto-publish reconcile outcome by result.
func recordAutopublish(ctx context.Context, result string) {
	autopublishTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

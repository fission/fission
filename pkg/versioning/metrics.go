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

// Skip-reason labels for fission_versiongc_skipped_total. Bounded to these
// two -- the only reasons SweepVersions ever skips a candidate instead of
// deleting it (see SweepResult's doc comment).
const (
	versionGCSkipReasonReferenced = "referenced"
	versionGCSkipReasonForbidden  = "forbidden"
)

// versionGCDeletedTotal counts RFC-0025 retention GC (pkg/versioning.
// SweepVersions) FunctionVersion deletes.
var versionGCDeletedTotal = metrics.Int64Counter(
	"fission_versiongc_deleted_total",
	"RFC-0025 retention GC: FunctionVersions deleted.",
)

// versionGCSkippedTotal counts RFC-0025 retention GC deletes skipped, by
// reason (referenced|forbidden).
var versionGCSkippedTotal = metrics.Int64Counter(
	"fission_versiongc_skipped_total",
	"RFC-0025 retention GC: FunctionVersion deletes skipped, by reason (referenced|forbidden).",
)

// recordVersionGCDeleted counts n retention-GC deletes (a no-op for n<=0, so
// callers can pass a result slice's length unconditionally).
func recordVersionGCDeleted(ctx context.Context, n int) {
	if n <= 0 {
		return
	}
	versionGCDeletedTotal.Add(ctx, int64(n))
}

// recordVersionGCSkipped counts n retention-GC skips for reason.
func recordVersionGCSkipped(ctx context.Context, reason string, n int) {
	if n <= 0 {
		return
	}
	versionGCSkippedTotal.Add(ctx, int64(n), metric.WithAttributes(attribute.String("reason", reason)))
}

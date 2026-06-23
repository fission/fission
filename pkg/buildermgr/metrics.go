// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/fission/fission/pkg/utils/metrics"
)

// ociPublishes counts OCI producer outcomes per build (RFC-0012):
// result="published" (the Package carries a digest-pinned OCI archive)
// or result="degraded" (the push failed and the build fell back to the
// storagesvc tarball). A fleet-wide registry outage shows up here.
var ociPublishes = metrics.Int64Counter(
	"fission_buildermgr_oci_publish_total",
	"OCI package publish outcomes by result (published|degraded).",
)

// recordOCIPublish counts one OCI publish outcome by result (published|degraded).
func recordOCIPublish(ctx context.Context, result string) {
	ociPublishes.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

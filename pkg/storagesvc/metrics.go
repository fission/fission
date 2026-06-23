// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"github.com/fission/fission/pkg/utils/metrics"
)

var (
	// totalArchives and totalMemoryUsage are increment/decrement gauges, mapped
	// to UpDownCounters (exposed by the Prometheus bridge as gauges).
	totalArchives = metrics.Int64UpDownCounter(
		"fission_archives",
		"Number of archives stored",
	)
	totalMemoryUsage = metrics.Int64UpDownCounter(
		"fission_archive_memory_bytes",
		"Amount of memory consumed by archives",
	)
	// legacyArchiveAccess counts accesses by a namespace-scoped caller to a
	// legacy (unscoped) archive, which are grandfathered for backward
	// compatibility. A nonzero-and-not-shrinking value tells operators how much
	// traffic still touches un-migrated archives before tightening the policy.
	legacyArchiveAccess = metrics.Int64Counter(
		"fission_storagesvc_legacy_archive_access_total",
		"Accesses by a namespace-scoped caller to a legacy (unscoped) archive, grandfathered for backward compatibility",
	)
)

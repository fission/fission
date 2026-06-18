// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/fission/fission/pkg/utils/metrics"
)

var (
	functionLabels = []string{}
	totalArchives  = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_archives",
			Help: "Number of archives stored",
		},
		functionLabels,
	)
	totalMemoryUsage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_archive_memory_bytes",
			Help: "Amount of memory consumed by archives",
		},
		functionLabels,
	)
	// legacyArchiveAccess counts accesses by a namespace-scoped caller to a
	// legacy (unscoped) archive, which are grandfathered for backward
	// compatibility. A nonzero-and-not-shrinking value tells operators how much
	// traffic still touches un-migrated archives before tightening the policy.
	legacyArchiveAccess = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_storagesvc_legacy_archive_access_total",
			Help: "Accesses by a namespace-scoped caller to a legacy (unscoped) archive, grandfathered for backward compatibility",
		},
		functionLabels,
	)
)

func init() {
	registry := metrics.Registry
	registry.MustRegister(totalArchives)
	registry.MustRegister(totalMemoryUsage)
	registry.MustRegister(legacyArchiveAccess)
}

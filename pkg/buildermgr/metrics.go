// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/fission/fission/pkg/utils/metrics"
)

var (
	// ociPublishes counts OCI producer outcomes per build (RFC-0012):
	// result="published" (the Package carries a digest-pinned OCI archive)
	// or result="degraded" (the push failed and the build fell back to the
	// storagesvc tarball). A fleet-wide registry outage shows up here.
	ociPublishes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_buildermgr_oci_publish_total",
			Help: "OCI package publish outcomes by result (published|degraded).",
		},
		[]string{"result"},
	)
)

func init() {
	metrics.Registry.MustRegister(ociPublishes)
}

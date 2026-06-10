// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/fission/fission/pkg/utils/metrics"
)

const (
	resultMatch = "match"
	resultMiss  = "miss"
	resultLag   = "lag"
)

var (
	// shadowResults counts shadow-mode comparisons of executor answers against
	// the slice-fed index. "match" = agreement; "miss" = the index knows no
	// ready endpoint for the function; "lag" = endpoints exist but the
	// executor's (poolmgr) address is not yet among them — expected briefly
	// after a fresh specialization. A steady-state non-match rate of zero is
	// the promotion criterion from shadow mode to cutover. No function-name
	// labels by design (cardinality).
	shadowResults = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_router_endpointcache_shadow_total",
			Help: "Shadow-mode comparisons of executor answers vs the EndpointSlice index, by result (match|miss|lag).",
		},
		[]string{"result"},
	)
)

// RegisterSizeGauge registers a gauge reporting the number of functions in the
// index. Separate from init so the gauge closes over a live Index.
func RegisterSizeGauge(ix *Index) {
	metrics.Registry.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "fission_router_endpointcache_size",
			Help: "Number of functions with at least one EndpointSlice in the router's endpoint index.",
		},
		func() float64 { return float64(ix.Size()) },
	))
}

func init() {
	metrics.Registry.MustRegister(shadowResults)
}

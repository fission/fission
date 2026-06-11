// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/fission/fission/pkg/utils/metrics"
)

// Shadow-comparison results (see RecordShadowResult).
const (
	ShadowMatch = "match"
	ShadowMiss  = "miss"
	ShadowLag   = "lag"
)

// Warm-path fallback reasons (see RecordFallback). Admission refusals are
// labeled with the AdmitResult strings (all_busy, all_quarantined, ...).
const (
	FallbackStrict              = "strict"
	FallbackNoEndpoints         = "no_endpoints"
	FallbackCapacityUnsupported = "capacity_unsupported"
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

	// hits counts warm-path requests admitted from the index (zero executor
	// RPCs); misses counts requests for which the index knew no READY endpoint
	// (no entry at all, or an entry whose endpoints are all unready).
	hits = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "fission_router_endpointcache_hits_total",
		Help: "Requests served from the EndpointSlice endpoint index (no executor RPC).",
	})
	misses = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "fission_router_endpointcache_misses_total",
		Help: "Requests with no ready endpoint in the EndpointSlice endpoint index.",
	})
	// quarantines counts endpoints quarantined after a dial failure. Aggregate
	// fallback reasons only show when EVERY endpoint of a function is out;
	// this counter makes partial quarantine (one bad pod among many) visible.
	quarantines = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "fission_router_endpointcache_quarantines_total",
		Help: "Endpoints quarantined from the index after a dial failure (lifted on the next slice event).",
	})
	// fallbacks counts warm-path requests routed to the executor for a
	// specific reason (strict-mode annotation, no endpoints, all endpoints
	// saturated, or the executor not supporting ensureCapacity).
	fallbacks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "fission_router_endpointcache_fallbacks_total",
		Help: "Warm-path requests routed to the executor instead of the endpoint index, by reason.",
	}, []string{"reason"})
)

// RecordShadowResult counts one shadow comparison (router package hook —
// the comparator lives there to use the AddressResolver types).
func RecordShadowResult(result string) { shadowResults.WithLabelValues(result).Inc() }

// ShadowResultCounter returns one shadow result counter (test hook).
func ShadowResultCounter(result string) prometheus.Counter {
	return shadowResults.WithLabelValues(result)
}

// RecordHit counts one index-admitted request.
func RecordHit() { hits.Inc() }

// RecordMiss counts one index miss.
func RecordMiss() { misses.Inc() }

// RecordFallback counts one executor fallback by reason.
func RecordFallback(reason string) { fallbacks.WithLabelValues(reason).Inc() }

// RecordQuarantine counts one endpoint quarantine.
func RecordQuarantine() { quarantines.Inc() }

// RegisterModeInfo registers a constant info gauge exposing the requested and
// effective endpointslice cache modes. Registered for EVERY mode (including
// off) so a fail-soft degrade (e.g. missing RBAC flipping on→off at startup)
// is alertable as requested != effective rather than as an absent series.
func RegisterModeInfo(requested, effective string) {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "fission_router_endpointcache_mode",
		Help:        "Always 1; labels carry the requested and effective EndpointSlice cache modes (off|shadow|on).",
		ConstLabels: prometheus.Labels{"requested": requested, "effective": effective},
	})
	g.Set(1)
	// Tolerate re-registration (in-process test harnesses restart the router
	// within one process); identical const labels mean an identical series.
	var already prometheus.AlreadyRegisteredError
	if err := metrics.Registry.Register(g); err != nil && !errors.As(err, &already) {
		panic(err)
	}
}

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
	metrics.Registry.MustRegister(hits)
	metrics.Registry.MustRegister(misses)
	metrics.Registry.MustRegister(fallbacks)
	metrics.Registry.MustRegister(quarantines)
}

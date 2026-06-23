// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/fission/fission/pkg/utils/metrics"
)

// Warm-path fallback reasons (see RecordFallback). Admission refusals are
// labeled with the AdmitResult strings (all_busy, all_quarantined, ...).
const (
	FallbackStrict              = "strict"
	FallbackNoEndpoints         = "no_endpoints"
	FallbackCapacityUnsupported = "capacity_unsupported"
)

var (
	// hits counts warm-path requests admitted from the index (zero executor
	// RPCs); misses counts requests for which the index knew no READY endpoint
	// (no entry at all, or an entry whose endpoints are all unready).
	hits = metrics.Int64Counter(
		"fission_router_endpointcache_hits_total",
		"Requests served from the EndpointSlice endpoint index (no executor RPC).",
	)
	misses = metrics.Int64Counter(
		"fission_router_endpointcache_misses_total",
		"Requests with no ready endpoint in the EndpointSlice endpoint index.",
	)
	// lbPicks counts newdeploy/container requests dialed to a pod IP by the
	// endpoint-LB path (NOT hits: the Service entry resolution may have cost
	// an executor RPC). Doubles as the liveness signal that the endpointLB
	// flag is active.
	lbPicks = metrics.Int64Counter(
		"fission_router_endpointcache_endpointlb_picks_total",
		"Requests dialed directly to a pod IP by the endpoint-LB path (newdeploy/container).",
	)
	// quarantines counts endpoints quarantined after a dial failure. Aggregate
	// fallback reasons only show when EVERY endpoint of a function is out;
	// this counter makes partial quarantine (one bad pod among many) visible.
	quarantines = metrics.Int64Counter(
		"fission_router_endpointcache_quarantines_total",
		"Endpoints quarantined from the index after a dial failure (lifted by the next slice event or the quarantine TTL).",
	)
	// fallbacks counts warm-path requests routed to the executor for a
	// specific reason (strict-mode annotation, no endpoints, all endpoints
	// saturated, or the executor not supporting ensureCapacity).
	fallbacks = metrics.Int64Counter(
		"fission_router_endpointcache_fallbacks_total",
		"Warm-path requests routed to the executor instead of the endpoint index, by reason.",
	)
	// modeInfo is the always-1 info gauge whose labels carry the requested and
	// effective cache modes (see RegisterModeInfo).
	modeInfo = metrics.Int64Gauge(
		"fission_router_endpointcache_mode",
		"Always 1; labels carry the requested and effective EndpointSlice cache modes (off|on) and whether endpoint LB is enabled.",
	)
)

// RecordHit counts one index-admitted request.
func RecordHit() { hits.Add(context.Background(), 1) }

// RecordMiss counts one index miss.
func RecordMiss() { misses.Add(context.Background(), 1) }

// RecordFallback counts one executor fallback by reason.
func RecordFallback(reason string) {
	fallbacks.Add(context.Background(), 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// RecordEndpointLBPick counts one endpoint-LB pod-IP pick.
func RecordEndpointLBPick() { lbPicks.Add(context.Background(), 1) }

// RecordQuarantine counts one endpoint quarantine.
func RecordQuarantine() { quarantines.Add(context.Background(), 1) }

// RegisterModeInfo publishes the constant info gauge exposing the requested and
// effective endpointslice cache modes. Recorded for EVERY mode (including off)
// so a fail-soft degrade (e.g. missing RBAC flipping on->off at startup) is
// alertable as requested != effective rather than as an absent series.
func RegisterModeInfo(requested, effective string, endpointLB bool) {
	modeInfo.Record(context.Background(), 1, metric.WithAttributes(
		attribute.String("requested", requested),
		attribute.String("effective", effective),
		attribute.String("endpoint_lb", strconv.FormatBool(endpointLB)),
	))
}

var (
	sizeIndex     atomic.Pointer[Index]
	sizeGaugeOnce sync.Once
)

// RegisterSizeGauge publishes an observable gauge reporting the number of
// functions in the index. It is idempotent: the observable instrument is
// registered exactly once and always reports the most recently registered
// Index, so a repeat call (e.g. an in-process router restart in tests)
// re-points the gauge at the live Index instead of stacking a second callback
// on a now-dead one.
func RegisterSizeGauge(ix *Index) {
	sizeIndex.Store(ix)
	sizeGaugeOnce.Do(func() {
		metrics.Int64ObservableGauge(
			"fission_router_endpointcache_size",
			"Number of functions with at least one EndpointSlice in the router's endpoint index.",
			func(_ context.Context, o metric.Int64Observer) error {
				if ix := sizeIndex.Load(); ix != nil {
					o.Observe(int64(ix.Size()))
				}
				return nil
			},
		)
	})
}

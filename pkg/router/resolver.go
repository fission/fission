// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"net/url"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// ResolvedEntry is what the transport needs to proxy one request.
type ResolvedEntry struct {
	// SvcURL is the dialable address for the function.
	SvcURL *url.URL
	// FromCache reports whether the address came from a cache (a cached
	// address is tapped, and is evicted on persistent dial failures).
	FromCache bool
	// TapURL, when non-nil, is the address to TAP for liveness accounting in
	// place of SvcURL: endpoint-LB entries dial a pod IP, but newdeploy/
	// container atime (idle scale-down) is keyed on the Service address the
	// executor knows. nil means tap SvcURL itself.
	TapURL *url.URL
	// Release, when non-nil, returns the request slot taken by router-local
	// admission accounting; the proxy invokes it once the request completes
	// (response done / stream drained). nil means the resolution path does its
	// accounting executor-side (RPC untap). A non-nil Release is produced
	// solely by Index.Admit (idempotent via sync.Once) — resolver authors must
	// not fabricate one, or the two accounting modes mix and corrupt the
	// executor's PoolCache counters.
	Release func()
}

// tapTarget returns the address liveness taps should use for this entry.
func (e ResolvedEntry) tapTarget() *url.URL {
	if e.TapURL != nil {
		return e.TapURL
	}
	return e.SvcURL
}

// InvalidateReason classifies the dial failure driving an Invalidate call, so
// the index can tell a dead endpoint from a saturated one.
type InvalidateReason int

const (
	// InvalidateHard marks the endpoint presumed dead: connection refused
	// (port closed / pod gone), a non-timeout dial error, or the executor-
	// cached path's retry ladder exhausting. Quarantines immediately.
	InvalidateHard InvalidateReason = iota
	// InvalidateSoft marks a dial timeout — how a saturated-but-alive pod
	// presents (full SYN backlog). Quarantining a function's only endpoint on
	// one of these turns saturation into an executor-fallback specialization
	// storm, so soft failures only quarantine after repeated strikes.
	InvalidateSoft
)

// AddressResolver resolves a function to a dialable service URL. It is the
// single choke point of the data plane (RFC-0002): the executor-RPC resolver
// is the legacy path (mode=off, and the fallback target), and the fallback
// resolver serves the warm path from the EndpointSlice index when the cache
// mode is on (the default).
type AddressResolver interface {
	// Resolve returns the service entry for fn. stickyKey, when non-empty, is
	// the RFC-0023 sticky routing key extracted from the request per the
	// function's StickyConfig; the index-fed resolver ranks ready endpoints
	// by rendezvous hash of it, and the executor-RPC resolver ignores it
	// (the legacy data plane does not support stickiness).
	Resolve(ctx context.Context, fn *fv1.Function, stickyKey string) (ResolvedEntry, error)
	// Invalidate drops any cached state for fn's address. The transport calls
	// it on dial failure of an index-admitted endpoint (hard -> immediate
	// quarantine, soft -> strike-counted) and after the retry ladder for
	// cached executor-resolved addresses (hard). addr may be nil when no
	// address had been resolved.
	Invalidate(fn *fv1.Function, addr *url.URL, reason InvalidateReason)
}

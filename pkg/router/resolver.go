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
	// Release, when non-nil, returns the request slot taken by router-local
	// admission accounting; the proxy invokes it once the request completes
	// (response done / stream drained). nil means the resolution path does its
	// accounting executor-side (RPC untap).
	Release func()
}

// AddressResolver resolves a function to a dialable service URL. It is the
// single choke point of the data plane (RFC-0002): the executor-RPC resolver
// is today's behavior, the shadow wrapper compares it against the
// EndpointSlice index, and the fallback resolver serves the warm path from the
// index at cutover.
type AddressResolver interface {
	// Resolve returns the service entry for fn.
	Resolve(ctx context.Context, fn *fv1.Function) (ResolvedEntry, error)
	// Invalidate drops any cached state for fn's address (called by the
	// transport when the address keeps failing dials). addr may be nil when no
	// address had been resolved.
	Invalidate(fn *fv1.Function, addr *url.URL)
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"net/url"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// AddressResolver resolves a function to a dialable service URL. It is the
// single choke point of the data plane (RFC-0002): the executor-RPC resolver
// is today's behavior, the shadow wrapper compares it against the
// EndpointSlice index, and the slice-fed resolver replaces it on the warm path
// at cutover.
type AddressResolver interface {
	// Resolve returns the service URL for fn and whether it came from a cache
	// (a cached address is tapped and is evicted on persistent dial failures).
	Resolve(ctx context.Context, fn *fv1.Function) (svcURL *url.URL, fromCache bool, err error)
	// Invalidate drops any cached address for fn (called by the transport when
	// the address keeps failing dials).
	Invalidate(fn *fv1.Function)
}

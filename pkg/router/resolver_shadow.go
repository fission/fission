// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"net/url"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/endpointcache"
)

// shadowResolver wraps the live resolver: every successful lookup is compared
// against the slice-fed index and classified, with zero influence on routing.
// The shadow counter is the machine-checked promotion criterion from shadow
// mode to cutover.
type shadowResolver struct {
	logger logr.Logger
	inner  AddressResolver
	index  *endpointcache.Index
}

// newShadowResolver wraps inner with the shadow comparator.
func newShadowResolver(logger logr.Logger, inner AddressResolver, ix *endpointcache.Index) *shadowResolver {
	return &shadowResolver{logger: logger.WithName("endpointcache_shadow"), inner: inner, index: ix}
}

// Resolve delegates to the live resolver and compares its answer to the index.
func (s *shadowResolver) Resolve(ctx context.Context, fn *fv1.Function) (ResolvedEntry, error) {
	entry, err := s.inner.Resolve(ctx, fn)
	if err == nil && entry.SvcURL != nil {
		s.compare(fn, entry.SvcURL)
	}
	return entry, err
}

// Invalidate delegates to the live resolver.
func (s *shadowResolver) Invalidate(fn *fv1.Function, addr *url.URL) {
	s.inner.Invalidate(fn, addr)
}

// compare classifies one executor answer against the index:
//
//   - poolmgr: "match" when the returned pod address is among the function's
//     ready endpoints; "miss" when the index knows no endpoint for the function
//     (e.g. the executor-side Service flag is off, or the function was never
//     specialized since the Service appeared); "lag" when endpoints exist but
//     the returned address is not (yet) among them — expected transiently right
//     after a fresh specialization, before the slice event lands.
//   - newdeploy/container: the executor returns the Service DNS name, never a
//     pod address, so only state is compared: ≥1 ready endpoint is a "match"
//     (the index agrees the function is scaled up), none is a "miss".
func (s *shadowResolver) compare(fn *fv1.Function, svcURL *url.URL) {
	ready := 0
	addrMatch := false
	for _, ep := range s.index.Lookup(fn.Namespace, fn.Name) {
		if !ep.Ready {
			continue
		}
		ready++
		if ep.Address == svcURL.Host {
			addrMatch = true
		}
	}

	var result string
	switch fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType {
	case fv1.ExecutorTypePoolmgr:
		switch {
		case addrMatch:
			result = endpointcache.ShadowMatch
		case ready == 0:
			result = endpointcache.ShadowMiss
		default:
			result = endpointcache.ShadowLag
		}
	case fv1.ExecutorTypeNewdeploy, fv1.ExecutorTypeContainer:
		if ready > 0 {
			result = endpointcache.ShadowMatch
		} else {
			result = endpointcache.ShadowMiss
		}
	default:
		return
	}
	endpointcache.RecordShadowResult(result)
	if result != endpointcache.ShadowMatch {
		s.logger.V(1).Info("shadow compare divergence",
			"function", fn.Name, "namespace", fn.Namespace,
			"executor_address", svcURL.Host, "ready_endpoints", ready, "result", result)
	}
}

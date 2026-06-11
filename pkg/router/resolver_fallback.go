// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"net/url"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/router/endpointcache"
)

// CapacityClient is the optional executor-client facet the fallback resolver
// uses when every known endpoint is saturated: the executor — still the
// capacity authority — specializes one more pod (synchronous address answer)
// or rejects at the function's concurrency cap. Implemented by the executor
// client against POST /v2/ensureCapacity.
type CapacityClient interface {
	EnsureCapacity(ctx context.Context, fn *fv1.Function, observedReady, observedBusy int) (string, error)
}

// fallbackResolver is the cutover (mode=on) AddressResolver for the warm path:
// index first, executor for everything the index cannot answer safely.
//
//   - poolmgr, non-strict: admit from the index (least-outstanding below
//     requestsPerPod, router-local accounting via the entry's Release). All
//     endpoints saturated → ensureCapacity (executor specializes one more pod
//     or 429s at the cap). No endpoints at all → the legacy RPC (cold start,
//     Istio mode, executor flag off — all degrade identically).
//   - poolmgr, strict-annotated: the legacy RPC path, untouched.
//   - newdeploy/container: ≥1 ready endpoint → the legacy resolver (cached
//     Service DNS); zero ready endpoints → bypass the address cache and RPC the
//     executor proactively, replacing the dial-fail backoff ladder on
//     scale-from-zero.
type fallbackResolver struct {
	logger   logr.Logger
	index    *endpointcache.Index
	executor *executorResolver
	capacity CapacityClient // nil when the executor client doesn't support it
}

func newFallbackResolver(logger logr.Logger, ix *endpointcache.Index, executor *executorResolver, capacity CapacityClient) *fallbackResolver {
	return &fallbackResolver{
		logger:   logger.WithName("endpointcache_resolver"),
		index:    ix,
		executor: executor,
		capacity: capacity,
	}
}

func (f *fallbackResolver) Resolve(ctx context.Context, fn *fv1.Function) (ResolvedEntry, error) {
	if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypePoolmgr {
		return f.resolveDeployBacked(ctx, fn)
	}
	// Strict-mode and OnceOnly functions take the legacy RPC path: strict for
	// exact global concurrency accounting, OnceOnly because its pods serve
	// exactly one request and must never be re-admitted from slices (the
	// executor also never creates a function Service for them — this check is
	// the router-side belt to that brace).
	if fn.StrictConcurrencyEnforcement() || fn.Spec.OnceOnly {
		endpointcache.RecordFallback(endpointcache.FallbackStrict)
		return f.executor.Resolve(ctx, fn)
	}

	ep, release, admit := f.index.Admit(fn.Namespace, fn.Name, fn.GetRequestPerPod())
	if admit == endpointcache.Admitted {
		endpointcache.RecordHit()
		return ResolvedEntry{SvcURL: ep.URL, FromCache: true, Release: release}, nil
	}

	ready := f.index.ReadyCount(fn.Namespace, fn.Name)
	if ready == 0 {
		// Cold start (or Istio mode / executor flag off): the synchronous
		// executor RPC, byte-identical to the pre-RFC path — this is what keeps
		// the ~100ms poolmgr cold start unchanged.
		endpointcache.RecordMiss()
		return f.executor.Resolve(ctx, fn)
	}

	// Endpoints exist but none was admissible (busy / quarantined): ask the
	// executor (the capacity authority) for one more pod. The reason is logged
	// and labeled so a divergence between the kube slices and the index view
	// is diagnosable from the router log alone. The specialization must use
	// the CURRENT function spec (the resolved snapshot can be stale after
	// `fn update --pkg`), same as fromExecutor.
	f.logger.V(1).Info("no admissible endpoint; falling back to executor",
		"function", fn.Name, "namespace", fn.Namespace,
		"reason", string(admit), "ready_endpoints", ready)
	endpointcache.RecordFallback(string(admit))
	if f.capacity != nil {
		// observedBusy == ready here: on this path every ready endpoint was
		// inadmissible (busy or quarantined) — both counts are diagnostic-only
		// on the executor side.
		addr, err := f.capacity.EnsureCapacity(ctx, f.executor.currentFunction(ctx, fn), ready, ready)
		if err == nil {
			svcURL, perr := url.Parse("http://" + addr)
			if perr != nil {
				return ResolvedEntry{}, fmt.Errorf("error parsing ensureCapacity address %q: %w", addr, perr)
			}
			// Executor-side accounting (it allotted the pod) — no Release.
			return ResolvedEntry{SvcURL: svcURL}, nil
		}
		if ferr, ok := err.(ferror.Error); ok && ferr.Code == ferror.ErrorNotFound {
			// Old executor without /v2/ensureCapacity — degrade to the legacy
			// RPC, which still works (upgrade-order safety).
			endpointcache.RecordFallback(endpointcache.FallbackCapacityUnsupported)
			return f.executor.Resolve(ctx, fn)
		}
		return ResolvedEntry{}, err
	}
	return f.executor.Resolve(ctx, fn)
}

// resolveDeployBacked handles newdeploy/container: the Service DNS address
// stays the dial target; the index contributes scale-state awareness.
func (f *fallbackResolver) resolveDeployBacked(ctx context.Context, fn *fv1.Function) (ResolvedEntry, error) {
	if f.index.ReadyCount(fn.Namespace, fn.Name) > 0 {
		return f.executor.Resolve(ctx, fn)
	}
	// Zero ready endpoints: scaled to zero (or never up). Bypass the address
	// cache — the cached Service DNS would dial into a backendless Service and
	// climb the retry ladder — and RPC the executor (coalesced through the
	// throttler), which scales the Deployment up and waits for readiness.
	endpointcache.RecordFallback(endpointcache.FallbackNoEndpoints)
	svcURL, err := f.executor.resolveUncached(ctx, fn)
	if err != nil {
		return ResolvedEntry{}, err
	}
	return ResolvedEntry{SvcURL: svcURL}, nil
}

// Invalidate quarantines the failing endpoint (until the next slice event) and
// drops the executor resolver's cached address. Logged at Info: dial failures
// are rare, and a partial quarantine (one bad pod among many) is otherwise
// invisible — the aggregate fallback metric only fires when every endpoint of
// a function is out.
func (f *fallbackResolver) Invalidate(fn *fv1.Function, addr *url.URL) {
	if addr != nil {
		f.logger.Info("quarantining endpoint after dial failure",
			"function", fn.Name, "namespace", fn.Namespace, "address", addr.Host)
		f.index.Quarantine(fn.Namespace, fn.Name, addr.Host)
	}
	f.executor.Invalidate(fn, addr)
}

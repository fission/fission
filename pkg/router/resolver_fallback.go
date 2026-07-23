// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"math"
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
	// endpointLB dials newdeploy/container pod IPs directly (least
	// outstanding across ready endpoints) instead of the Service VIP.
	// Default off (ROUTER_ENDPOINTSLICE_ENDPOINT_LB).
	endpointLB bool
}

func newFallbackResolver(logger logr.Logger, ix *endpointcache.Index, executor *executorResolver, capacity CapacityClient, endpointLB bool) *fallbackResolver {
	return &fallbackResolver{
		logger:     logger.WithName("endpointcache_resolver"),
		index:      ix,
		executor:   executor,
		capacity:   capacity,
		endpointLB: endpointLB,
	}
}

func (f *fallbackResolver) Resolve(ctx context.Context, fn *fv1.Function, stickyKey string) (ResolvedEntry, error) {
	if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypePoolmgr {
		return f.resolveDeployBacked(ctx, fn, stickyKey)
	}
	// Strict-mode and OnceOnly functions take the legacy RPC path: strict for
	// exact global concurrency accounting, OnceOnly because its pods serve
	// exactly one request and must never be re-admitted from slices (the
	// executor also never creates a function Service for them — this check is
	// the router-side belt to that brace).
	if fn.StrictConcurrencyEnforcement() || fn.Spec.OnceOnly {
		endpointcache.RecordFallback(endpointcache.FallbackStrict)
		return f.executor.Resolve(ctx, fn, stickyKey)
	}

	// version "" -- the router does not yet resolve a specific
	// FunctionVersion to route to (RFC-0025 phase 3+); until then every
	// function's pool lives at the unversioned FnKey.
	ep, release, admit := f.index.Admit(fn.Namespace, fn.Name, "", fn.GetRequestPerPod(), stickyKey)
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
		return f.executor.Resolve(ctx, fn, stickyKey)
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
			return f.executor.Resolve(ctx, fn, stickyKey)
		}
		return ResolvedEntry{}, err
	}
	return f.executor.Resolve(ctx, fn, stickyKey)
}

// resolveDeployBacked handles newdeploy/container: the Service DNS address
// stays the dial target (the index contributes scale-state awareness) unless
// endpointLB is on, in which case ready pod IPs are dialed directly with
// least-outstanding selection.
func (f *fallbackResolver) resolveDeployBacked(ctx context.Context, fn *fv1.Function, stickyKey string) (ResolvedEntry, error) {
	if f.index.ReadyCount(fn.Namespace, fn.Name) > 0 {
		entry, err := f.executor.Resolve(ctx, fn, stickyKey)
		// The nil-SvcURL guard covers the throttler-follower race (Resolve can
		// answer nil, nil): without it the LB entry would carry TapURL=nil and
		// taps would silently key on the pod IP — starving newdeploy atime and
		// inviting idle scale-down under live traffic. Returning the entry
		// keeps the loud "serviceURL is empty, retrying" transport behavior.
		if err != nil || !f.endpointLB || entry.SvcURL == nil {
			return entry, err
		}
		// Endpoint LB: dial the least-outstanding ready pod directly,
		// bypassing the Service VIP. No per-pod admission cap — newdeploy
		// concurrency is the Deployment's business (HPA), the index only
		// spreads — so the cap is effectively unbounded. Taps stay keyed on
		// the Service address (TapURL): newdeploy idle scale-down ages on the
		// address the executor knows, not the pod IP. Any inadmissible state
		// (e.g. every endpoint quarantined) keeps the VIP entry, which still
		// works.
		ep, release, admit := f.index.Admit(fn.Namespace, fn.Name, "", math.MaxInt32, stickyKey)
		if admit == endpointcache.Admitted {
			// Counted separately from hits: the Service entry above may have
			// cost an executor RPC, so this is NOT a "zero-RPC" hit — it is an
			// LB pick, and the dedicated counter is also the steady-state
			// signal that the flag is actually doing something.
			endpointcache.RecordEndpointLBPick()
			return ResolvedEntry{SvcURL: ep.URL, TapURL: entry.SvcURL, FromCache: true, Release: release}, nil
		}
		// Inadmissible (all quarantined, no counted-ready endpoint, CAS
		// contention): the VIP still works, but the degradation must be
		// observable — a NoCountedReady here (slice endpoints without pod
		// targetRefs) would otherwise leave the flag permanently, invisibly
		// inert for this function.
		f.logger.V(1).Info("endpoint LB inadmissible; dialing the Service VIP",
			"function", fn.Name, "namespace", fn.Namespace, "reason", string(admit))
		endpointcache.RecordFallback(string(admit))
		return entry, nil
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

// Invalidate handles a reported dial failure: hard failures quarantine the
// endpoint immediately, soft ones are strike-counted (see
// endpointcache.ReportDialTimeout). Both drop the executor resolver's cached
// address. Quarantines log at Info: they are rare, and a partial quarantine
// (one bad pod among many) is otherwise invisible — the aggregate fallback
// metric only fires when every endpoint of a function is out.
func (f *fallbackResolver) Invalidate(fn *fv1.Function, addr *url.URL, reason InvalidateReason) {
	if addr != nil {
		if reason == InvalidateSoft {
			if f.index.ReportDialTimeout(fn.Namespace, fn.Name, addr.Host) {
				f.logger.Info("quarantining endpoint after repeated dial timeouts",
					"function", fn.Name, "namespace", fn.Namespace, "address", addr.Host)
			} else {
				f.logger.V(1).Info("dial timeout strike recorded",
					"function", fn.Name, "namespace", fn.Namespace, "address", addr.Host)
			}
		} else {
			f.logger.Info("quarantining endpoint after dial failure",
				"function", fn.Name, "namespace", fn.Namespace, "address", addr.Host)
			f.index.Quarantine(fn.Namespace, fn.Name, addr.Host)
		}
	}
	f.executor.Invalidate(fn, addr, reason)
}

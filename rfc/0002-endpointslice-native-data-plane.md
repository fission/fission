# RFC-0002: EndpointSlice-Native Data Plane

- Status: Proposed
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.(N+1) / v1.(N+2)
- Requires: Kubernetes 1.33+ (EndpointSlice GA since 1.21 — comfortable
  minimum)

## Summary

Replace Fission's bespoke router↔executor address-lookup protocol with
Kubernetes-native service discovery via Services and EndpointSlices. The
router maintains its function-address index by watching EndpointSlices
directly; the executor becomes responsible only for *creating* the
address (specializing a pod, scaling a Deployment) and no longer sits on
the hot path.

While we are in the code, fix three correctness/performance hotspots:

1. Single-goroutine cache channel in `pkg/cache/cache.go` → sharded map.
2. Unbounded goroutines in poolmgr specialization → bounded worker pool.
3. `sync.WaitGroup`-based request multiplexer that doesn't respect
   request context cancellation → context-aware channel-based
   deduplication.

## Motivation

Current cold lookup flow (functionHandler.go:127–150, executor/client/client.go:78):

```
router                             executor
  |                                   |
  |---- POST /v2/getServiceForFunction|
  |                                   |---- (poolmgr) claim pool pod,
  |                                   |       call fetcher, wait ready
  |                                   |
  |<---- 200 { url: pod-ip:8888 } ----|
  |                                   |
  |---- proxy request to pod-ip ------|
  |                                   |
```

Problems:

- **HTTP round-trip on every cache miss** — adds 3–10ms under load, orders
  of magnitude more than EndpointSlice informer hit.
- **Single point of failure** — executor slowness stalls all routers.
- **Stale entries** — router caches pod IPs keyed on `resourceVersion`;
  on pod replacement, router doesn't know until a proxy attempt fails
  and retries (functionHandler.go:79–90).
- **5-second tap ticker** — the keep-alive signal batched at 5s
  granularity is coarse for eviction decisions.
- **Poolmgr bypasses Services entirely** — pods are tracked by labels
  only. `kubectl get svc` shows nothing; other k8s tooling is blind.

Native approach:

```
router (informer)                  executor
  |                                   |
  |  watch EndpointSlices -----------.|
  |                                  v|
  |                           (slice updated when pod Ready)
  |                                   ^
  |                                   |
  |                              create Service + manage slice
  |                                   ^
  |---- EnsureEndpoints(fn) ----------|  (miss path only; fire-and-forget)
  |                                   |
  |  <--- EndpointSlice event --------|
  |                                   |
  |--- proxy to svc/pod direct -------|
  |                                   |
```

This is how `kube-proxy`, the Gateway API dataplane, Istio, and every
other in-cluster proxy works. Fission should stop being different.

## Goals

- Every function, regardless of executor type, has a Kubernetes Service
  (headless for poolmgr, ClusterIP for newdeploy/container) owned by the
  executor.
- Pod readiness is reflected in the Service's EndpointSlices by the
  executor (for poolmgr) or by the kubelet (for newdeploy/container).
- Router watches EndpointSlices via informer; in-memory index from
  `(functionNamespace, functionName) → []endpoint` is updated on slice
  events.
- Router's hot path does **not** call the executor. Cache-miss path calls
  `EnsureEndpoints` (new API, fire-and-forget) that triggers
  specialization/scaling asynchronously; router waits for the matching
  slice event (short timeout) before falling back to error.
- Cache in `pkg/cache/cache.go` moves from single-goroutine to sharded
  `sync.Map` with per-shard TTL eviction. Hot path is lock-free read.
- Executor specialization uses a bounded worker pool
  (`k8s.io/client-go/util/workqueue`) — no more unbounded goroutines.
- Executor request multiplexer replaces `sync.WaitGroup` with a
  per-function channel that is closed on completion; waiters select on
  that channel and `ctx.Done()`.
- Tap/UnTap becomes an annotation + controller: router annotates the
  Service with `fission.io/last-used` (SSA patch, rate-limited to 1/sec),
  executor GC loop reads annotation, no more periodic ticker.

## Non-goals

- Replacing the router process with kube-proxy/Gateway data plane. That's
  a separate, larger RFC. This one keeps Fission's router in place but
  makes its cache Kubernetes-native.
- Changing the user-visible executor types or their semantics.
- Removing the executor's HTTP API. Legacy callers and the
  `EnsureEndpoints` path continue to go through it.

## Design

### Service + EndpointSlice per function

Executor, on first specialization of a function `F` in namespace `N`:

- Create `Service F` in `N` with `clusterIP: None` (headless), owned by
  the Function object (controller owner ref).
- Label selector is unused (headless, manually-managed endpoints).
- Executor creates a single `EndpointSlice` for the Service, named
  `F-<shard>`, and owns its lifecycle.
- On pod Ready transitions for pods belonging to `F`, executor updates
  the slice's `Endpoints[]`.

For newdeploy/container, the existing per-function Service is reused;
the kubelet populates EndpointSlices automatically.

File locations:
- `pkg/executor/executortype/poolmgr/gp_endpointslice.go` (new)
- `pkg/executor/executortype/poolmgr/gp.go` (hook in specialize + clean up)
- `pkg/executor/executortype/poolmgr/poolpodcontroller.go` (watch pod Ready)

### Router informer

New package: `pkg/router/endpointcache/`.

- An `endpointslices` informer filtered by label
  `fission.io/function-name` (set by executor on the Service +
  propagated by the slice controller).
- An in-process index keyed on `(namespace, functionName) → []endpoint`.
- Index is backed by `sync.Map` with per-key read-lock-free access.
- Subscribers register for change notifications (used by
  `functionServiceMap` replacement to invalidate proxy connection pools
  on endpoint changes).

Router hot path (`pkg/router/functionHandler.go` and
`functionReferenceResolver.go`):

- `getServiceEntry(fn)` reads from `endpointcache`; returns endpoint slice
  directly. No HTTP call to executor.
- On cache miss, call `executor.EnsureEndpoints(fn)` (new API). This is
  fire-and-return. Router then waits (bounded `select` with ctx) on the
  `endpointcache` signal for up to `maxColdstartWait` (config, default
  30s).
- Load balancing across endpoints: start with random choice for parity;
  leave weighted/least-outstanding-request strategies for a follow-up.

### EnsureEndpoints API

```go
// pkg/executor/client
type Client interface {
    EnsureEndpoints(ctx context.Context, fn *fv1.Function) error
    // Legacy; kept for compat, delegates to EnsureEndpoints internally.
    GetServiceForFunction(ctx context.Context, fn *fv1.Function) (*url.URL, error)
}
```

Executor side (`pkg/executor/executor.go`):

```go
// Before:
//   serveRequests serialized on sync.WaitGroup; no ctx cancel.
// After:
//   in-flight map[funcKey]chan struct{}
//   handler:
//     if ch, exists := inFlight[key]; exists {
//       select { case <-ch: return current status; case <-ctx.Done(): return ctx.Err() }
//     }
//     ch := make(chan struct{}); inFlight[key] = ch
//     go func() {
//       defer func() { close(ch); delete(inFlight, key) }()
//       specialize(fn)
//     }()
//     select { case <-ch: ...; case <-ctx.Done(): ... }
```

### Bounded worker pool for specialization

Replace ad-hoc `go` statements in `pkg/executor/executor.go:125-142`
with a `client-go` workqueue + N worker goroutines (config:
`executor.specializationConcurrency`, default `max(4, 2×CPUs)`).

### Cache modernization

`pkg/cache/cache.go` today uses a single `requestChannel` with a
serialization goroutine (cache.go:104–169). Replace with:

```go
type shardedCache[K comparable, V any] struct {
    shards [N]cacheShard[K, V]
}

type cacheShard[K, V any] struct {
    mu    sync.RWMutex
    m     map[K]*entry[V]
    // TTL sweep per shard, independent cadence.
}
```

`N = 64` shards. Key selection via `fnv.Sum32`. Reads under RLock, writes
under Lock. TTL sweeper runs per-shard at jittered intervals.

### Tap via annotation + controller

Replace `executor/client.Client.service()` ticker (client.go:73,
142–150) with a per-request annotation patch (SSA), debounced at 1/sec
per function, using a `workqueue.RateLimitingInterface`. Executor's
existing GC loop reads `fission.io/last-used` from Services and honors
the same idle TTL as today.

### Observability

- `fission_router_endpointcache_size{namespace=...}` — gauge.
- `fission_router_cache_hit{result=hit|miss}` — counter.
- `fission_executor_specialization_seconds` — histogram (was per-RPC
  wall time).
- OTel spans: `router.lookup`, `executor.ensure`, `executor.specialize`.
- Conditions on `Function` status: `EndpointsReady`, `LastSpecializedAt`.

## Alternatives considered

1. **Gateway API HTTPRoute instead of our own router.** Strictly bigger
   scope; loses custom Fission features (canary weighted routing via
   `CanaryConfig`, per-function retries, tap-based idle detection) until
   we port them to Gateway API extensions. Queued as a separate, later
   RFC.
2. **Keep reactive executor lookup, but add caching in executor client.**
   Treats symptoms, not cause. Also doesn't fix the tap ticker or the
   cache bottleneck.
3. **Use `kube-proxy` to proxy directly.** Forgoes Fission's observability
   hooks, canary logic, and custom retries. Same objection as (1).

## Backward compatibility

- External API: `GetServiceForFunction` stays (legacy callers, tests).
  Now a thin wrapper over `EnsureEndpoints` + read-back from informer.
- CRDs: no changes.
- Helm values: one new optional setting `router.maxColdstartWait`
  (default 30s).
- Observable: new Services + EndpointSlices appear per function in the
  user's namespace. Non-breaking but operators should be notified via
  release notes; NetworkPolicy users may need to update selectors (see
  Open questions).

## Rollout phases

1. **Phase 1 — Additive: create Services + EndpointSlices for poolmgr
   functions.** No router change. Executor dual-writes. Ships in v1.N.
   Observable via `kubectl get svc -n <ns>` immediately.
2. **Phase 2 — Router informer in shadow mode.** Router watches slices
   and compares lookups against its current cache; emits
   `fission_router_shadow_mismatch` metric. No behavior change. v1.N.
3. **Phase 3 — Router switches to informer as primary source.** Executor
   HTTP call becomes fallback-only. v1.(N+1).
4. **Phase 4 — Remove executor HTTP lookup from hot path.** Executor
   API retains the endpoint for legacy clients; internally delegates.
   v1.(N+1).
5. **Phase 5 — Cache sharding + bounded worker pool + ctx-aware
   multiplexer.** Pure internal refactor, independently landable but
   sequenced last for risk containment. v1.(N+1) or v1.(N+2).

## Verification

- **Unit**: ShardedCache correctness under concurrency (race detector +
  table-driven TTL tests). EnsureEndpoints idempotency. Context
  cancellation unblocks waiters.
- **Envtest**: executor creates Service+Slice on specialization; router
  informer observes change; router test proxies to correct endpoint.
- **E2E**: existing `test_node_hello_http.sh` and friends pass
  unmodified. New `test_endpointslice_visibility.sh` asserts
  `kubectl get svc` and `kubectl get endpointslice` show the function.
- **Load test** (`test/loadtest/`, new): 10k sustained RPS against a
  warm function. Compare p50/p99 and executor CPU:
  - Target: ≥ 20% reduction in router→response p99 at warm paths.
  - Target: executor CPU drops ≥ 40% under steady warm load (no
    per-request RPCs).
- **Failure injection**: stop executor, verify warm function traffic
  continues; start a new function with executor down, verify it times
  out gracefully with a clear error.
- **Leak check**: create/delete 1000 functions in a loop, verify no
  leaked Services or Slices after reaper cycle.

## Open questions

- **NetworkPolicy impact**: users who have a `default-deny` policy on the
  function namespace today will suddenly have Services that must be
  reachable from the router. We should ship a default NetworkPolicy in
  the Helm chart that allows router→function traffic.
- **Headless vs ClusterIP for poolmgr**: headless gives the router every
  pod IP directly (good for load balancing), but loses kube-proxy's
  conntrack. ClusterIP with manual endpoints gives us kube-proxy for
  free but adds a hop. Start headless; measure.
- **Multi-cluster**: does this interact poorly with Admiralty/Submariner
  or KubeFed-style setups? Likely not; EndpointSlices are the intended
  federation boundary. Flag for release-notes call-out.

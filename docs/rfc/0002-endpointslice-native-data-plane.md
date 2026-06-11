# RFC-0002: EndpointSlice-Native Data Plane

- Status: Implemented — phases 0–3 ([#3485](https://github.com/fission/fission/pull/3485), merged 2026-06-11); phase 4 (defaults on, shadow-mode removal, newdeploy endpoint-LB flag) ships in v1.26 as well — the flip was pulled forward after the perf runbook, multi-replica, and index-scale verification all passed (see 0002-perf-runbook-results.md).
- Perf verification: the pre-phase-4 runbook passed all acceptance bars on 2026-06-11 — see [0002-perf-runbook-results.md](0002-perf-runbook-results.md).
- Tracking issue: TBD
- Supersedes: rev 1 of this document
- Targets: Fission v1.26 for all phases (the phase-4 defaults flip was originally planned for v1.(N+2) and pulled forward on verification evidence)
- Requires: Kubernetes 1.32+ (current floor per `MinimumKubernetesVersion` in `pkg/apis/core/v1/const.go`; no bump). No new third-party dependencies.

## Summary

Make the router discover function addresses natively from Kubernetes Services and EndpointSlices instead of HTTP-calling the executor on the request hot path.
The executor narrows to **provisioning** (specialize a pod, scale a Deployment, reap idle capacity); the router widens to **admission and load balancing** over endpoints it learns from a label-filtered EndpointSlice informer.
The design thesis is *"router admits, executor provisions."*

The cold-start path stays byte-identical to today — the synchronous `getServiceForFunction` RPC returning a pod IP is untouched, preserving the ~100 ms poolmgr cold-start budget that Fission is known for.
EndpointSlices serve the **warm** path: a specialized poolmgr pod (or a newdeploy/container pod) is visible to every router replica through slices, so warm traffic flows with zero executor RPCs and survives executor downtime.

One semantic change is made deliberately and documented: per-pod concurrency enforcement (`requestsPerPod`) moves from globally-strict (single leader-elected executor accounting) to per-router-replica local accounting, with a per-function `fission.io/concurrency-enforcement: strict` annotation that preserves today's exact behavior.

The RFC also carries a **structural track**: the seams it rewrites (`pkg/router/functionHandler.go`, `pkg/executor/executor.go`, `pkg/executor/executortype/poolmgr/gp.go`) are decomposed along SOLID lines — small consumer-side interfaces, god-file splits, and a testable specialization dispatcher — with each extraction riding the functional phase that motivates it.

## Changes from rev 1

Rev 1 of this RFC predates the controller-runtime migration of router and executor, RFC-0008 streaming, the internal-listener HMAC split, and the k8s 1.32 floor.
Beyond stale facts, three rev-1 design pillars are **rejected** in this revision:

1. **Fire-and-forget `EnsureEndpoints` + wait-for-slice-event on cache miss.**
   This puts EndpointSlice-controller batching plus informer watch latency (~50–150 ms) on *every cold start*, blowing the 100 ms poolmgr budget.
   Rev 2 keeps the cold start synchronous and uses slices only for warm-path reads and invalidation.
2. **Executor-managed EndpointSlices on a manually-endpointed headless Service.**
   Specialized pool pods already carry per-function labels (`labelsForFunction`, `pkg/executor/executortype/poolmgr/gp.go:384`), so a *selector-based* Service gets its slices written by the built-in EndpointSlice controller for free — battle-tested GC, terminating semantics, and pod-delete handling that keeps working even when the executor is down.
   Rev 2 ships zero custom slice-writing code and zero new executor RBAC.
3. **Tap via SSA annotation patches on the Service.**
   Per-function annotation writes are etcd write amplification: at 10k active functions even a 1/min debounce is 10k writes/min fanning out to every Service watcher.
   Rev 2 keeps the existing batched tap RPC (one POST per router per 5 s regardless of function count, `pkg/executor/client/client.go:150`), which also already underpins the RFC-0008 streaming keepalive heartbeat.

The rev-1 idea of bundling a generic `pkg/cache` sharded rewrite is descoped to a non-goal: the hot path that motivated it moves to a purpose-built endpoint index, leaving only low-QPS users of `pkg/cache`.

## Motivation

What actually happens today, per executor type (all references current at HEAD):

**Poolmgr — the executor is on the hot path of every single request.**
The router has no address cache for poolmgr: `getServiceEntry` (`pkg/router/functionHandler.go:851`) calls the executor's `/v2/getServiceForFunction` for *every* request, because the executor's `PoolCache` performs per-request, concurrency-aware dispatch — admit a pod only when `activeRequests < requestsPerPod` (`pkg/executor/fscache/poolcache.go:126`), eject on CPU overuse, enforce the `concurrency` cap (`poolcache.go:143-145`), and park excess requests on a `svcWait` queue (`poolcache.go:151-164`).
All of that runs through a **single goroutine** consuming a request channel (`poolcache.go:110`), serializing every poolmgr request in the cluster.
On completion, the router sends a synchronous `UnTapService` RPC per request (`functionHandler.go:749`) to decrement `activeRequests`.
That is two executor RPCs plus one global serialization point per warm request, and it makes the executor a single point of failure for *all* poolmgr traffic — an executor restart or crash takes the data plane down with it.

**Newdeploy/container — better, but blind and slow at the edges.**
These types create a per-function ClusterIP Service (`pkg/executor/executortype/newdeploy/newdeploy.go:393`), and the router caches the Service DNS address with a 1-minute TTL (`pkg/router/functionServiceMap.go`, backed by the single-goroutine `pkg/cache`).
The TTL forces a pointless executor RPC per function per minute.
Worse, the router has no visibility into endpoint readiness: when the idle reaper scales a Deployment to zero, the next request dials a Service with no endpoints, fails, and climbs the `RetryingRoundTripper` exponential-backoff ladder (up to 10 retries, `functionHandler.go:225-407`) — commonly 1–10 s of added latency before the executor is finally asked to scale up.

**Operational opacity.**
Poolmgr functions have no Service by default (`getFuncSvc` returns `podIP:8888`, `gp.go:538-643`), so `kubectl get svc`, service meshes, NetworkPolicy tooling, and every other Kubernetes-native observer are blind to them.

**Structural debt in exactly the files this RFC must touch.**
`functionHandler.go` is 988 lines mixing six responsibilities (canary pick, address lookup, retry transport, streaming orchestration, metrics, request rewrite), with `RetryingRoundTripper` holding a back-pointer into `functionHandler` that makes the transport untestable without a live executor stub.
`executor.go` (614 lines) mixes manager wiring, the HTTP API, adopt/cleanup, and a request multiplexer that spawns an **unbounded goroutine per poolmgr request** (`executor.go:290-322`) and dedups newdeploy/container requests via a `sync.Map` of `sync.WaitGroup`s whose waiters cannot honor context cancellation (`executor.go:325-366`).

Watching EndpointSlices is how kube-proxy, the Gateway API dataplanes, and every in-cluster mesh learn endpoints.
Fission should stop being different — without giving up the latency profile or the concurrency semantics that make it Fission.

## Goals

- Warm-path function invocation requires **zero executor RPCs** for all executor types; warm traffic keeps flowing when the executor is down, upgrading, or failing over.
- Poolmgr cold-start latency is **unchanged to the byte**: same synchronous RPC, same pod-IP response, same specialization sequence.
- Every invoked poolmgr function gets a Kubernetes Service (headless) and controller-managed EndpointSlices, owned and lifecycle-managed by the executor — visible to standard tooling.
- The router maintains a sharded, read-mostly endpoint index fed by one label-filtered EndpointSlice informer; reads on the hot path are lock-free pointer loads, not channel round-trips.
- Newdeploy/container scale-from-zero is detected from slice emptiness and triggers a proactive executor call, replacing the dial-fail backoff ladder.
- Idle reaping becomes drain-aware (unlabel → drain → delete) and therefore safe against the multi-router in-flight race that exists today.
- The executor request multiplexer becomes context-aware and bounded (single `Dispatcher` type, testable without Kubernetes).
- Router and executor internals are restructured along SOLID seams (consumer-side `AddressResolver`/`Tapper`/`Provisioner` interfaces, god-file decomposition) with behavior-preserving extraction PRs guarded by golden tests.
- Everything ships behind feature gates with a shadow mode whose mismatch metric is the machine-checked promotion criterion.

## Non-goals

- Replacing the router process with kube-proxy or a Gateway API dataplane (separate, larger RFC; the Gateway API route provider from RFC-0007 already attaches HTTPRoutes *to* the router and is orthogonal).
- Removing the executor HTTP API; `getServiceForFunction` remains the cold-start path and the universal fallback.
- CRD schema changes (the strict-mode switch is an annotation; CRD evolution belongs to RFC-0003).
- Cross-router-replica in-flight coordination (explicitly rejected; the strict-mode annotation covers workloads that need exact global accounting).
- Changing poolmgr Istio mode or `useSvc` mode semantics (these keep their existing address forms; the endpoint path is disabled for them).
- Zone-aware / topology routing (slices carry the data; a follow-up can exploit it).
- A generic rewrite of `pkg/cache` (descoped; see "Changes from rev 1").

## Design

### Per-executor-type overview

| | poolmgr | newdeploy | container | poolmgr Istio/useSvc | strict-annotated fn |
|---|---|---|---|---|---|
| Service today | none (pod IP) | ClusterIP, selector | ClusterIP, selector | svc/Istio DNS | n/a |
| Service after | **headless, selector, async-created** | unchanged + `managed-by` label | unchanged + `managed-by` label | unchanged | per its executor type |
| Slice writer | built-in controller | built-in controller (already) | built-in controller (already) | n/a (endpoint path off) | n/a (legacy RPC path) |
| Cold start | sync executor RPC (unchanged) | sync executor RPC (unchanged) | sync executor RPC (unchanged) | unchanged | unchanged |
| Warm path | router index + local admission | Service DNS, slice-driven invalidation | Service DNS, slice-driven invalidation | legacy | legacy RPC + UnTap |
| Scale-from-zero | n/a (specialize) | slice empty → proactive RPC | slice empty → proactive RPC | legacy | legacy |

### Poolmgr: headless Service per invoked function

On the first successful specialization of function F, the executor *asynchronously* ensures a Service in the function namespace:

- Name: `fn-<name>-<uid-hash8>` (deterministic, ≤63 chars).
- `clusterIP: None` — headless.
  The router dials pod IPs directly today and continues to; headless avoids kube-proxy programming iptables/IPVS rules on every node for thousands of per-function Services.
- Selector: the existing specialized-pod labels from `labelsForFunction` (`gp.go:384` — `functionName`, `functionUid`, namespace, `managed=false`) **plus two new pod labels**:
  - `fission.io/function-generation: <gen>` — folded into the existing relabel patch in `choosePod` (`gp.go:258`).
    `functionUid` is stable across function updates; today stale-generation pods are excluded by the executor's `CacheKeyURG` keying, so the selector must encode generation or routers would route to stale code.
    On a function update the executor's reconciler updates the Service selector and the slice controller atomically swaps the endpoint set (~100 ms — strictly better than newdeploy's up-to-1-minute TTL window today).
  - `fission.io/served: "true"` — folded into the **existing** post-specialization pod patch (`gp.go:642-650`, the `ANNOTATION_SVC_HOST` patch).
    Pool pods pass readiness probes *before* specialization; without this gate the slice controller would publish a relabeled-but-unspecialized pod as ready.
    Folding both labels into existing patches means **zero added API writes on the cold path**.
- Labels on the Service: `fission.io/managed-by=fission` plus the function labels.
  The EndpointSlice controller mirrors Service labels onto its slices, which is what the router's filtered informer keys on.
- OwnerReference: the Function object (cascade delete); `EXECUTOR_INSTANCEID_LABEL` annotation so the Service participates in `AdoptExistingResources` and orphan cleanup like every other executor-owned object (`gpm.go:371`).
- Creation timing: enqueued on a workqueue **after** `getFuncSvc` returns the pod IP — fire-and-forget with retry, never on the synchronous cold path.
  Until the Service exists (typically ~50–150 ms after first cold start), the router serves the function from its provisional entry (below) or the legacy RPC path.

Cold path, unchanged bytes: router → `POST /v2/getServiceForFunction` → executor claims a warm pod from `readyPodQueue`, relabels (now including generation), specializes via fetcher, patches `served=true` + svc-host annotation, returns `podIP:8888`.

Warm path, new: router reads its slice-fed index, admits against per-endpoint in-flight counters, proxies straight to `podIP:8888`.
No executor RPC, no UnTap RPC, no PoolCache serialization.

Saturated path, new: all known endpoints at local capacity → router calls `POST /v2/ensureCapacity` and waits on whichever resolves first — the synchronous response (a freshly specialized pod address) or a slice event adding capacity.

### Newdeploy / container

Services and slices already exist; they gain the `fission.io/managed-by=fission` label so the router's informer sees them.
The router **keeps dialing the ClusterIP DNS name** by default (conntrack, mesh compatibility); slices are used for *state*, not addressing:

- The 1-minute TTL refetch in `functionServiceMap` is dropped; invalidation becomes event-driven (slice emptiness, Service deletion, function generation change).
- When the ready-endpoint count is zero, the router calls `getServiceForFunction` proactively *before* dialing — the executor scales the Deployment and waits for readiness — replacing the dial-fail exponential-backoff ladder.
  This is the single biggest p99 win for scale-to-zero newdeploy functions.
- Endpoint-level load balancing (dial pod IPs directly, per-pod keep-alive reuse, bypass the VIP) ships behind `router.endpointSliceCache.endpointLB`, default **off**, last phase.
  Value is real but marginal versus risk (terminating-endpoint handling, `trafficDistribution` interactions, mesh assumptions); it is promoted only if metrics justify.

### Poolmgr concurrency: router-side admission

`PoolCache`'s per-request dispatch cannot be replaced by naive slice-watching, and pretending otherwise was the biggest hole in rev 1.
The chosen model:

- The router keeps an atomic in-flight counter per endpoint, keyed by **pod UID** so counters survive index rebuilds.
  Admit when `inflight < requestsPerPod` (least-outstanding tie-break); decrement on response completion or stream drain.
  This replaces the per-request UnTap RPC for router-admitted traffic.
- Enforcement becomes per-router-replica.
  Worst-case over-admission is `(R−1) × requestsPerPod` for R router replicas; with the typical 1–2 replicas that is ≤1 extra in-flight request per pod, and environment runtimes are ordinary HTTP servers — over-admission degrades to brief queueing at the pod, not failure.
- The global `concurrency` cap (default 500) **stays exact**, because it is pod-count-based and the executor still creates every pod: today `concurrencyUsed = len(svcs) + waiting` (`poolcache.go:143`), and pod inventory plus in-flight specializations remain fully executor-owned.
  `ensureCapacity` returns 429 exactly where `poolcache.go` does today.
- The `svcWait` queue is subsumed: instead of the executor parking a request channel, the router parks the request and wakes on "endpoint added" (slice event) or the synchronous `ensureCapacity` response, whichever comes first.
- CPU gating (`poolcache.go:127`) is reframed from an admission input to a **recycling signal**: on sustained overload the executor's existing CPU-poll loop removes the `fission.io/served` label, the pod drains out of the slices, routers stop admitting, and the pod is disposed.
  Distributing live CPU metrics to routers buys nothing this doesn't.
- **Strict mode escape hatch**: the annotation `fission.io/concurrency-enforcement: strict` on a Function routes it through today's exact path — per-request `getServiceForFunction` + UnTap against the leader-elected executor's PoolCache, which is retained unmodified as both the strict path and the universal fallback.
  Functions that genuinely rely on hard single-concurrency (non-thread-safe handlers, exclusive resource holders) opt in and lose only the latency win for that one function.
  No CRD change.

### Executor API evolution

- `POST /v2/getServiceForFunction` — **unchanged**: synchronous, primary cold-start path, strict-mode path, universal fallback.
- `POST /v2/ensureCapacity` — **new**: body `{function metadata, observedReadyEndpoints, observedBusyEndpoints}`.
  The executor compares the router's observation against its pod inventory, enforces the concurrency cap, and either synchronously specializes one more pod (returning `{address}`, same shape as `getServiceForFunction`) or returns 429.
  Idempotent under bursts via the bounded dispatcher (below).
  An old executor 404s this path; the router degrades to `getServiceForFunction`, which still works — upgrade order is free.
- `POST /v2/tapServices` — unchanged (batched atime keepalive).
- `POST /v2/unTapService` — retained for strict-mode functions only; routers stop calling it for router-admitted traffic.
- `pkg/executor/client` gains `EnsureCapacity`; the router consumes it through a narrow consumer-side interface (see Structural design) rather than widening `ClientInterface` for all callers.

### Router endpoint index (`pkg/router/endpointcache/`)

- **One** `discovery.k8s.io/v1` EndpointSlice informer registered with the router's existing controller-runtime manager (`pkg/router/router.go:321`; the router stays leader-election-free — every replica watches independently, which is the point).
  Label-filtered on `fission.io/managed-by=fission`, namespace-scoped exactly as the manager cache already is, with a `TransformFunc` stripping `managedFields` and annotations before storage.
- Sharded, read-mostly index — purpose-built, not `pkg/cache`:
  - `index: [256]shard`, shard chosen by `fnv32(fnKey) % 256`; `shard{mu sync.RWMutex; m map[fnKey]*fnEntry}`.
  - `fnEntry{eps atomic.Pointer[[]endpoint]; waiters chan struct{}; gen int64}` — the endpoint list is immutable and swapped copy-on-write on slice events; the waiter channel is closed-and-replaced on endpoint-add to wake saturated requests.
  - `endpoint{addr, podUID, ready bool, inflight *atomic.Int64}` — in-flight counters keyed by pod UID, carried across rebuilds.
  - Hot-path read: shard RLock to fetch the entry pointer, then lock-free atomic load of the list and atomic increment of `inflight`.
- Event flow: slice add/update/delete → map slice→fnKey via mirrored function labels → recompute that one function's endpoint list (union of its slices, filtered on `conditions.ready`) → pointer swap → close waiter channel → if the list became empty, mark the entry cold (drives the proactive executor call).
- **Provisional entries**: after a synchronous executor response (cold start or `ensureCapacity`), the router inserts the returned address as a provisional endpoint (`source=executor`, 30 s self-expiry) so the very next request is a warm hit even before the first slice event confirms it.
- Dial errors keep the existing `RetryingRoundTripper` ladder as backstop, except "refetch address" now means: re-read the index; if the endpoint is gone, pick another; if none, fall back to the executor RPC.
  A dial-failed endpoint is locally quarantined until the next slice event touches it.
- Memory bound: a stripped slice is ~1 KB for ≤10 endpoints; 10k functions ≈ 10 MB informer store plus ~2× for the index — comfortably under 50 MB, independent of request rate.

### Tap and idle reaping

The batched 5 s tap RPC is kept verbatim: it is off the hot path, costs O(routers) not O(functions), and the RFC-0008 streaming keepalive heartbeat (re-tap every `idleTimeout/2`) already rides it.
Endpoints served from the index still tap, so executor atime stays fresh.

Idle reaping becomes **drain-aware two-step** in the poolmgr reaper:

1. atime stale past `IdleTimeout` → executor removes the `fission.io/served` label (one PATCH) → the endpoint leaves the slices → every router stops admitting within watch latency (~ms–150 ms).
2. After a drain grace of `max(function timeout, 30 s)` → delete the pod and, when it was the function's last pod, the Service.

This is strictly safer than today's reap (which races in-flight requests across routers) and protects long-lived streams: the router holds the in-flight counter for the stream's duration, and streaming re-taps keep atime fresh anyway.
UnTap accounting in PoolCache simply stops mattering for router-admitted functions; the strict-mode path keeps the old `activeRequests == 0` check.

### Structural design (SOLID track)

The functional change above forces open the right seams; this track makes the cut deliberate instead of incidental.

**Verified diagnosis.**
`pkg/router/functionHandler.go` (988 lines) is a god-type with six responsibilities — canary pick, address lookup, retry transport, streaming orchestration, metrics (which hides a tap side effect at `functionHandler.go:953` onwards), request rewrite — and a circular back-pointer (`RetryingRoundTripper.funcHandler`) that makes the transport untestable without a live executor stub.
`pkg/executor/executor.go` (614 lines) mixes manager wiring, the HTTP API, the multiplexer, and adopt/cleanup.
`executortype.ExecutorType` is 14 methods wide but no consumer uses more than 8.
`gp.go` (740 lines) mixes pod choice, specialization, service creation, and CPU polling.
`api.go` duplicates the cache-validity dance across executor types.

**Consumer-side interfaces** (budget rule: an interface exists only with ≥2 implementations or a test fake an actual test uses):

- `router.AddressResolver { Resolve(ctx, fn) (resolvedEntry, error); Invalidate(fn) }` with `resolvedEntry{svcURL, fromCache, release func()}` — three implementations: `executorResolver` (today's lookup moved verbatim), `endpointcache.Resolver` (index + admission; `release` decrements in-flight), and `fallbackResolver` (composite: index first; strict/saturated/miss → executor).
  This is the single choke point where shadow comparison, admission, and fallback policy live.
- `router.Tapper { Tap; UnTap }` — unifies the four tap/untap call sites (classic transport defer, metrics hook, streaming heartbeat); second implementation (local accounting + batched atime tap) arrives at cutover.
- `router.CapacityClient { EnsureCapacity }` — `executor/client.ClientInterface` does not widen; the router depends on the three small interfaces, all satisfied by the one client object.
- `executor.Provisioner { GetServiceForFunction; EnsureCapacity }` — consumed by a new `apiServer` type so HTTP handlers are testable against a fake with zero Kubernetes.
- `executortype` facet split along *real* consumer lines (precedent: the existing `FuncReconciler`/`EnvReconciler` facets in the same file): `ServiceProvider`, `CacheManager`, `PodRefresher` (cms uses only `RefreshFuncPods`), `Lifecycle`; the composed `ExecutorType` survives as the registry type.
- `pkg/executor/dispatch.Dispatcher { Do(ctx, key, create) }` — the context-aware dedup plus bounded worker pool as one testable type, replacing `serveCreateFuncServices`, `requestChan`, and the `fsCreateWg` `sync.Map` of WaitGroups whose waiters cannot honor `ctx.Done()`.
  Bounded by `EXECUTOR_SPECIALIZATION_CONCURRENCY` (default 0 = today's unbounded behavior, so the change is inert until opted in).

**File decomposition.**
Router: `canary.go`, `rewrite.go` (URL prefix-trim + forwarded-host as pure functions), `transport.go` (retryingRoundTripper with injected resolver/tapper), `resolver_executor.go`, `tapper.go`, `stream.go` (onStreamResponse, keepalive heartbeat, watchdog setup); `collectFunctionMetric` moves to `metrics.go` minus the hidden tap (which moves to the ModifyResponse hook with identical ordering); residual `functionHandler.go` ≤ ~250 lines of per-request orchestration.
Executor: `start.go` split out of `executor.go`; `gp.go` → `gp_pod.go` / `gp_specialize.go` / `gp_service.go` (home of the new `ensureFunctionService`, generalizing the existing Istio-path `createSvc` at `gp.go:513`) / `gp_metrics.go`; gpm `service()` actor arms become named methods; `AdoptExistingResources` splits into `adoptPools` / `adoptPerImagePoolDeployments` / `adoptSpecializedPods`.
`poolcache.go` is deliberately **not** restructured: its admission arms go dead at cutover and are deleted in the final phase instead of refactoring code that is about to be removed.

**Guardrails.**
Extraction PRs move bodies byte-identical (receiver mechanics aside); any behavior fix found en route gets its own flagged commit.
Golden tests for URL rewrite, retry classification, and canary distribution land in phase 0, *before* the transport is touched.
`go test -race ./pkg/router/... ./pkg/executor/...` green before and after every extraction; SPDX headers on new files; goimports local prefix; no churn-only PRs beyond the small phase-0 set.

### Configuration

Helm values (`charts/fission-all/values.yaml`) and env vars, following existing `ROUTER_*` / `ENABLE_*` patterns:

| Helm value | Env var | Default (phases 0–3) | Default (phase 4) |
|---|---|---|---|
| `executor.functionServices.enabled` | `ENABLE_FUNCTION_SERVICES` | `false` | `true` |
| `router.endpointSliceCache.mode` | `ROUTER_ENDPOINTSLICE_CACHE_MODE` (`off\|shadow\|on`) | `off` | `on` |
| `router.endpointSliceCache.endpointLB` | `ROUTER_ENDPOINTSLICE_ENDPOINT_LB` | `false` | `false` |
| `executor.specializationConcurrency` | `EXECUTOR_SPECIALIZATION_CONCURRENCY` | `0` (unbounded) | `0` |

Per-function: `fission.io/concurrency-enforcement: strict` annotation.
`router.endpointSliceCache.mode=on` requires `executor.functionServices.enabled` for the poolmgr benefit; newdeploy/container benefits stand alone.

### Security, RBAC, NetworkPolicy

- Router rules in `charts/fission-all/templates/_fission-component-roles.tpl` gain read-only `discovery.k8s.io / endpointslices / get,list,watch` plus core `services / get,list,watch`.
- Executor RBAC: **no additions** — it already has Services CRUD, and kube-controller-manager writes the slices.
- NetworkPolicy: **no changes** — the router already dials function pod IPs directly for poolmgr, so router→pod flows are identical; the executor policy (router→8888 only) is untouched.
- The two-listener HMAC split (public 8888 / internal 8889) and `utils.UrlForFunction` default-namespace folding are untouched.

### Observability

New metrics (no function-name labels — cardinality discipline matches existing router metrics):

- `fission_router_endpointcache_size{executortype}` (gauge)
- `fission_router_endpointcache_hits_total`, `fission_router_endpointcache_misses_total`
- `fission_router_endpointcache_shadow_mismatches_total{reason="miss|extra|addr_mismatch"}`
- `fission_router_endpointcache_fallbacks_total{reason="ambiguous|empty|invalidated|strict|mode_off"}`
- `fission_executor_function_service_ensures_total{result="created|exists|error"}`

OTel spans on `Resolve` and `ensureCapacity`; existing `fission_function_overhead_seconds` and executor cold-start counters are the before/after yardsticks.

## Latency analysis

| Path | Today | After (phase 3) | Notes |
|---|---|---|---|
| Poolmgr cold | ~100 ms (relabel + specialize) + RPC framing | **identical** | Same bytes; new labels ride existing patches; Service ensure is async. |
| Poolmgr warm | executor RPC 1–5 ms p50 (p99 grows under load — single PoolCache goroutine serializes all poolmgr requests) + UnTap RPC | index read <10 µs, zero RPCs | Removes two RPCs and the global serialization point per request. |
| Poolmgr saturated | executor `svcWait` queue or specialize ~100 ms | `ensureCapacity` ~100 ms (same specialize) | Sync response races the slice event; equivalent latency. |
| Newdeploy warm | µs (router cache) + 1 RPC/min TTL refetch | µs (index), zero refetch | |
| Newdeploy scale-from-zero | dial fail → backoff ladder, often 1–10 s | slice empty → immediate executor call | Removes the entire backoff ladder. |
| Slice propagation | n/a | ~10–150 ms (slice-controller batching + watch) | Affects only warm-path freshness and drain speed; never the cold path. |

## Failure modes

| Failure | Today | After |
|---|---|---|
| Executor down | **all** poolmgr traffic fails; newdeploy warm survives ≤1 min TTL | poolmgr + newdeploy warm traffic unaffected; cold/scale-up fails (unchanged exposure, far smaller blast radius) |
| Stale endpoint (pod gone, slice lagging) | dial fail → retry/invalidate | same retry backstop + local quarantine; slice event removes it ≤150 ms, and the slice controller does this even with the executor dead |
| apiserver watch disconnect | n/a (router barely watches) | reflector relist/resync; router serves last-known index; legacy RPC fallback always available |
| Router restart | newdeploy cache cold → RPC burst to executor | index rebuilt from informer LIST; no executor involvement |
| Executor restart | pod adoption via `ANNOTATION_SVC_HOST` | unchanged adoption for provisioning; routing truth is slices, consistent regardless of executor state; function Services adopted via instanceID like other objects |
| Unspecialized pod matches selector | impossible (executor registers post-specialize) | impossible (`fission.io/served` gate) |
| Stale-generation pod serves new traffic | excluded via `CacheKeyURG` | excluded via generation label in the Service selector; ~100 ms swap window vs newdeploy's up-to-1-min TTL window today |

## Compatibility

- **Streaming (RFC-0008)**: address-source change only; the tap heartbeat is unchanged, and the router's in-flight counter is held until stream drain, which the drain-aware reaper respects.
- **Canary**: weighted function selection happens in `functionReferenceResolver` *before* address resolution — untouched.
- **MCP (RFC-0011)** and all internal publishers ride the router's internal listener — untouched.
- **Gateway API (RFC-0007)**: HTTPRoutes attach to the router Service — orthogonal.
- **Upgrade ordering**: safe in any order with gates at defaults; with gates on, an old router ignores the new Services and an old executor 404s `ensureCapacity`, both degrading to today's RPC path.
- Public APIs (CRDs, CLI, existing Helm values) unchanged; new Helm values are additive.

## Alternatives considered

1. **Executor-managed EndpointSlices** (rev 1): custom slice-writer code, `discovery.k8s.io` write RBAC, and bespoke failure modes — versus the built-in controller doing it for free off selector labels that specialized pods already carry.
   Rejected.
2. **Fire-and-forget `EnsureEndpoints` + wait-for-slice-event** (rev 1): puts 50–150 ms of slice propagation on every cold start.
   Rejected; cold start stays synchronous.
3. **SSA-annotation tap** (rev 1): etcd write amplification at function-count scale; the batched tap RPC is O(routers).
   Rejected.
4. **Conservative hybrid admission** (router serves from slices only when capacity is provably unambiguous): with the default `requestsPerPod=1` and >1 router replica, capacity is *always* ambiguous, so the hybrid silently degenerates to "always call the executor" while still shipping the dual-accounting complexity.
   Rejected in favor of router-side admission with the strict-mode annotation.
5. **Gateway API dataplane / kube-proxy as the proxy**: loses canary logic, per-function retries, tap-based idle detection, streaming policy.
   Separate RFC.

## Rollout phases

Structural extractions ride the functional phase that motivates them; phases are independently landable and revertable.

1. **Phase 0 — prep + golden tests.**
   Router: extract `canary.go` + `rewrite.go` with table tests locking today's URL and canary semantics; extract `routerConfig` from env parsing.
   Executor: split `start.go` out of `executor.go`; collapse the duplicated specialization-timeout block; named gpm actor methods; `AdoptExistingResources` three-way split; small readability fixes.
   Pure-churn PRs allowed here only, each ≤ ~200 moved lines.
2. **Phase 1 — executor: function Services + dispatcher** (`ENABLE_FUNCTION_SERVICES`, default off).
   `gp.go` four-way split; `gp_service.go` `ensureFunctionService`; generation + `served` labels folded into existing patches; reaper deletes the Service; adoption covers it.
   `dispatch.Dispatcher` replaces the multiplexer (ctx-aware dedup + bounded pool — required because `ensureCapacity` must dedup without an HTTP waiter); `Provisioner` + `apiServer` + `ensureCapacityHandler`; `ExecutorType` facet split.
3. **Phase 2 — router: informer + shadow mode** (`ROUTER_ENDPOINTSLICE_CACHE_MODE=shadow`).
   `AddressResolver` introduced with today's lookup moved verbatim into `executorResolver`; `transport.go`/`tapper.go`/`stream.go` extracted (behavior-identical — only `executorResolver` is wired).
   New `pkg/router/endpointcache/` informer + index + shadow comparator emitting the mismatch metric; RBAC delta lands here.
   Promotion criterion to phase 3: zero shadow mismatches over a kind-ci burn-in, machine-checked against the kind-ci Prometheus.
4. **Phase 3 — warm-path cutover** (`mode=on`, default off).
   `fallbackResolver` wired as default; index admission live; strict-mode annotation honored; second `Tapper` implementation; `ensureCapacity` consumed; drain-aware reaping; newdeploy slice-driven invalidation + scale-from-zero detection; UnTap dropped for router-admitted traffic.
5. **Phase 4 — defaults on + deletion** (pulled forward into the same release on verification evidence).
   Flip `executor.functionServices.enabled=true` and `mode=on`; `endpointLB` ships default-off; one CI leg pins `mode=off` so the legacy path stays tested; delete now-dead code — as shipped, only the shadow comparator: the PoolCache admission arms and `functionServiceMap` survive because `mode=off`, strict-mode functions, and cold starts still drive them (see the deviation note in 0002-implementation-plan.md) — earlier phases only add or move.

## Verification

Condensed; the full plan with file-level test inventories, acceptance thresholds, and the risk register lives in `rfc/0002-implementation-plan.md`.

- **Unit** (`-race`, table-driven, fake clientsets): endpoint index under event storms with concurrent readers; shadow comparator classification; `ensureFunctionService` idempotency; `Dispatcher` cancellation/bounding/dedup; retry-transport matrix against a fake `AddressResolver`; `fallbackResolver` decision table; phase-0 golden tests for rewrite/canary.
- **Integration** (`test/integration/`, gates on in kind-ci): poolmgr invoke → Service + ready EndpointSlice; warm-hit metric increments; newdeploy slice empty→ready→empty across scale-to-zero and idle; reap deletes Service + slice; existing canary/streaming/MCP/internal-listener suites as regression guard; serial suite: **warm invoke succeeds with the executor scaled to zero** (the headline), Services adopted across executor restart, mixed-gate upgrade-order safety.
- **Performance**: cold-start microbenchmark (30 sequential cold starts, p95 regression <10% vs `mode=off`, specialization wall time unchanged); k6 burst-load warm p99 ≥20% lower with hit ratio ≥99%; pprof artifact comparison (router goroutines flat, executor dispatch samples down).
- **CI**: three k8s versions (1.32/1.34/1.36) with gates on, one leg pinned `mode=off`; shadow-mismatch == 0 as a machine-checked promotion step.

## Open questions

- Should the per-function Service also be created for poolmgr functions that have *never* been invoked (e.g. at Function-create time by the reconciler), trading a small steady-state object count for slightly earlier slice availability?
  Current answer: no — create on first invoke, reap on idle, keep the object count proportional to the working set.
- Drain grace default: `max(function timeout, 30 s)` is proposed; functions with very long `FunctionTimeout` values could pin pods for that long after idle.
  Cap it (e.g. 5 min) or honor the full timeout?
- Should `endpointLB` (phase 4, default off) filter on `endpoint.conditions.serving` to include terminating-but-serving endpoints during rollouts, or strictly `ready`?
  Strictly `ready` proposed until measured.

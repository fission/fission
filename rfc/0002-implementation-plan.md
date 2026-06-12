# RFC-0002 Implementation Plan: EndpointSlice-Native Data Plane

Companion to [0002-endpointslice-native-data-plane.md](0002-endpointslice-native-data-plane.md) (rev 2).
Phases 0–3 shipped in [#3485](https://github.com/fission/fission/pull/3485) (merged 2026-06-11); phase 4 remains.
This document is the executable plan: PR-by-PR phasing, file-level scope, test inventories, performance acceptance thresholds, and the risk register.
All paths are relative to the repo root; line references are current at HEAD and should be re-verified at implementation time.

## Ground rules

- Every phase is an independently landable, revertable PR (or small PR set); phases 0–3 can ship within one minor release, phase 4 waits one release.
- Structural extractions ride the functional PR that motivates them; pure-churn PRs are allowed only in phase 0 and stay ≤ ~200 moved lines each.
- Extraction commits move bodies byte-identical (receiver mechanics aside); any behavior fix gets its own commit with an explicit note.
- `go test -race ./pkg/router/... ./pkg/executor/...` green before and after every extraction; `make code-checks` and `make license-check` before every push.
- New files get SPDX headers (`make license`); imports keep the `github.com/fission/fission` goimports local prefix.
- Interface budget rule: an interface exists only with ≥2 real implementations or a test fake an actual test uses.

## Phase 0 — prep + golden tests

Two small PRs, no flags, no chart changes, behavior-identical.

**PR 0a — router extractions + golden tests.**

- Extract `pkg/router/canary.go`: `findCeil`, `getCanaryBackend`, `functionWeightDistribution` (from `functionHandler.go:681-705` and `functionReferenceResolver.go`).
- Extract `pkg/router/rewrite.go`: the URL prefix-trim block from `RoundTrip` (`functionHandler.go:275-312`) as pure function `rewriteFunctionURL(req, trigger, fnMeta, svcURL)`, plus `addForwardedHostHeader` (`functionHandler.go:708`).
- Extract `routerConfig` struct from the ~90 lines of env parsing in `router.Start` (`router.go:215-301`), including fixing the double env read at `router.go:280-281`.
- Hoist `dumpReqFunc`/`dumpRespFunc` (`functionHandler.go:202-223`) to package-level helpers; `RoundTrip` drops toward ~150 lines of pure retry loop.
- **Golden tests** (these anchor every later phase): `pkg/router/rewrite_test.go` (prefix-trim with/without `KeepPrefix`, default `/fission/<name>/<ns>` stripping, forwarded-host) and canary distribution tests (weight boundaries, binary-search ceiling).

**PR 0b — executor extractions + quick wins.**

- File-split `pkg/executor/start.go` out of `executor.go`: `StartExecutor`, `executorCacheOptions`, `executorControllers`, `runAdoptCleanup`, `bindAddr` move verbatim.
- Collapse the duplicated specialization-timeout block (`executor.go:304-313` vs `343-352`) into `withSpecializationTimeout(ctx, fn)`.
- gpm `service()` actor arms (`gpm.go:556-638`) become named methods (`handleGetPool` / `handleCleanupPool` / `handleGetEnvPools`).
- `AdoptExistingResources` (`gpm.go:371-530`) splits into `adoptPools` / `adoptPerImagePoolDeployments` / `adoptSpecializedPods`; fix `gpm.go:552` logging `err` instead of `errs`.
- Hoist per-pool `POD_READY_TIMEOUT` parsing (`gp.go:105-112`) to `MakeGenericPoolManager`; dedupe the api.go cache-validity dance (`api.go:56-97`) into one `serveFromCache` helper; name the anonymous goroutines (untap launchers `functionHandler.go:271/604` — router side rides PR 0a — tap-flush loop `client.go:164-176`, readiness goroutine `executor.go:207-212`).

Rollback: revert; nothing depends on these.

## Phase 1 — executor: function Services + dispatcher (flag `ENABLE_FUNCTION_SERVICES`)

One PR (or two: split + functional).

**Files.**

- `pkg/executor/executortype/poolmgr/gp.go` four-way split first: `gp_pod.go` (`choosePod`, `scheduleDeletePod`, `labelsForFunction`), `gp_specialize.go` (`specializePod`, `getFetcherURL`), `gp_service.go` (existing `createSvc` from `gp.go:513` moves in verbatim), `gp_metrics.go` (`updateCPUUtilizationSvc` and friends); residual `gp.go` keeps the struct, `MakeGenericPool`, and the `getFuncSvc` orchestration.
- `gp_service.go` gains `ensureFunctionService(ctx, fn)`: idempotent create/patch of the **headless** Service (`clusterIP: None`, name `fn-<name>-<uid-hash8>`, selector = `labelsForFunction(fn)` + generation + `served` labels, labels `fission.io/managed-by=fission` + function labels, OwnerReference to the Function, `EXECUTOR_INSTANCEID_LABEL` annotation).
  Enqueued on a workqueue after `getFuncSvc` returns — fire-and-forget with retry, never on the cold path.
- Pod label changes folded into **existing** patches: `fission.io/function-generation` in the `choosePod` relabel; `fission.io/served=true` in the post-specialize `ANNOTATION_SVC_HOST` patch (`gp.go:642-650`).
  Zero added API writes on the cold path.
- Reaper: poolmgr cleanup deletes the function Service when the last specialized pod for the function is reaped.
- Adoption: `adoptSpecializedPods`-adjacent pass lists Services by `fv1.EXECUTOR_TYPE=poolmgr` selector and re-stamps instanceID (mirror the pool-deployment adoption block at `gpm.go:413-437`); the post-adopt stale-instanceID reaper deletes orphans.
- `pkg/executor/dispatch/dispatch.go` (new package): `Dispatcher.Do(ctx, key, create)` — per-key dedup via a `chan struct{}` closed on completion (waiters select on it and `ctx.Done()`), bounded by a semaphore sized from `EXECUTOR_SPECIALIZATION_CONCURRENCY` (0 = unbounded, today's behavior, so the PR is inert by default).
  Replaces `serveCreateFuncServices`, `requestChan`, and `fsCreateWg` (`executor.go:131-132, 290-391`); the poolmgr key includes generation so per-function parallelism matches today's cache-miss behavior.
- `pkg/executor/api.go`: `apiServer` type consuming a `Provisioner { GetServiceForFunction; EnsureCapacity }` interface; new `POST /v2/ensureCapacity` handler (sync `{address}` or 429 at the concurrency cap).
- `pkg/executor/executortype/executortype.go`: facet split (`ServiceProvider`, `CacheManager`, `PodRefresher`, `Lifecycle`; composed `ExecutorType` survives as the registry type); consumers narrow (cms takes `map[fv1.ExecutorType]PodRefresher`, the dispatcher path takes `ServiceProvider`).
- Chart: `executor.functionServices.enabled` → `ENABLE_FUNCTION_SERVICES` and `executor.specializationConcurrency` → `EXECUTOR_SPECIALIZATION_CONCURRENCY` in `charts/fission-all/templates/executor/deployment.yaml` + `values.yaml`, wired like `ENABLE_OCI_IMAGE_VOLUME`.

**Pre-merge guard**: run `TestPoolCacheRequests` 50× with `-race` (known-flaky adjacent surface — `scenario-test7` flakes independently of diffs; distinguish pre-existing flake from new regression before blaming the PR).

Rollback: flag off; startup orphan cleanup (instanceID mechanism) removes leftover Services.

## Phase 2 — router: informer + shadow mode (`ROUTER_ENDPOINTSLICE_CACHE_MODE=shadow`)

**Files.**

- Introduce `pkg/router/resolver.go`: `AddressResolver { Resolve(ctx, fn) (resolvedEntry, error); Invalidate(fn) }`, `resolvedEntry{svcURL, fromCache, release func()}`.
- `pkg/router/resolver_executor.go`: `getServiceEntry` / `getServiceEntryFromCache` / `getServiceEntryFromExecutor` / cache add-remove (`functionHandler.go:765-896`) move with bodies verbatim; receiver becomes `executorResolver{logger, fmap, reader, executor, throttler}`.
- `pkg/router/transport.go`: `RetryingRoundTripper` → `retryingRoundTripper` with injected `AddressResolver` + `Tapper` + `transportParams`; the back-pointer into `functionHandler` is severed (`funcHandler.getServiceEntry` → `resolver.Resolve`, `removeServiceEntryFromCache` → `resolver.Invalidate`, untap defer → `tapper.UnTap`).
- `pkg/router/tapper.go`: `Tapper { Tap; UnTap }`; `executorTapper` absorbs `tapService` (`functionHandler.go:475`) and `unTapService` (`functionHandler.go:749`) and `unTapServiceTimeout`.
- `pkg/router/stream.go`: `onStreamResponse` (`functionHandler.go:617`), `startKeepaliveHeartbeat` (`functionHandler.go:661`), and the watchdog/max-duration setup from `handler` as `setupStreamContext`; heartbeat consumes `Tapper`.
- `collectFunctionMetric` (`functionHandler.go:953`) → `metrics.go`, **minus** the hidden tap call, which moves to the `ModifyResponse` hook in `handler` with identical ordering (flagged as the one deliberate behavior-preserving relocation).
- New `pkg/router/endpointcache/{informer,index,shadow,resolver}.go`: label-filtered EndpointSlice informer on the existing manager (`router.go:321`), sharded copy-on-write index with per-endpoint `atomic.Int64` in-flight counters, shadow comparator invoked after each successful executor RPC, `endpointcache.Resolver` (admission logic present but unwired until phase 3).
- Mode knob parsed in `router.go` alongside the other `ROUTER_*` envs; chart env in `charts/fission-all/templates/router/deployment.yaml`; Helm `router.endpointSliceCache.mode` (default `off`).
- RBAC: add `discovery.k8s.io / endpointslices / get,list,watch` + core `services / get,list,watch` to `router-rules` in `charts/fission-all/templates/_fission-component-roles.tpl` (block starting ~line 175).

Behavior-identical: only `executorResolver` is wired in `shadow` and `off`; shadow additionally watches + compares + counts.

**Promotion criterion to phase 3 (machine-checked)**: a kind-ci burn-in with `mode=shadow` where a post-suite workflow step queries the kind-ci Prometheus and asserts `fission_router_endpointcache_shadow_mismatches_total == 0`.

Rollback: `mode=off`; the informer never starts; the RBAC is harmless surplus.

## Phase 3 — warm-path cutover (`mode=on`, default off)

**Files.**

- `pkg/router/resolver_fallback.go`: `fallbackResolver` composite — index first; on miss / all-endpoints-saturated / strict annotation / Istio-useSvc / mode≠on, delegate to `executorResolver`; on saturation call `CapacityClient.EnsureCapacity` and race the sync response against the index waiter channel.
- `endpointcache.Resolver` admission goes live: admit when `inflight < requestsPerPod` (least-outstanding tie-break), `release()` decrements; provisional entries (30 s self-expiry) inserted from sync RPC responses; dial-failed endpoints quarantined until the next slice event.
- Second `Tapper` implementation: local in-flight accounting + the batched atime tap kept; UnTap RPC dropped for router-admitted traffic (retained for strict mode).
- `pkg/executor/client/client.go`: `EnsureCapacity` method (consumed via the router-side `CapacityClient` interface; `ClientInterface` itself does not widen).
- Executor drain-aware reaping: `PoolDeleteStrategy.Reap` (`pkg/executor/reaper/idle/idle.go`) gains unlabel (`served` removed) → drain grace `max(fn timeout, 30 s)` → delete pod (+ Service when last).
- Newdeploy/container: `fission.io/managed-by` label on their Services; slice-driven invalidation replaces the 1-min TTL in `functionServiceMap`; zero-ready-endpoints triggers the proactive executor call before dialing.
- kind-ci skaffold profile enables both gates (`executor.functionServices.enabled=true`, `mode=on`), same patch style as `enableOCIImageVolume`.

Rollback: `mode=shadow` or `off` at runtime via Helm upgrade; no data migration.

## Phase 4 — defaults on + endpoint LB flag + deletion (pulled forward into v1.26 on verification evidence)

As shipped, two deviations from the plan below: the PoolCache admission arms and the `functionServiceMap` are NOT deleted — `mode=off` (a supported configuration with its own CI leg), strict-mode functions, and cold starts all still drive them, so they stay until a future RFC removes the legacy plane entirely; and the quarantine TTL (a review follow-up) shipped separately as [#3487](https://github.com/fission/fission/pull/3487) after CI demonstrated quarantine permanence during executor downtime.

- Flip `executor.functionServices.enabled=true` and `router.endpointSliceCache.mode=on` in `values.yaml`.
- `router.endpointSliceCache.endpointLB` ships default-off (newdeploy per-endpoint dialing, `ready`-condition filter).
- One CI matrix leg pins `mode=off` (post-deploy `kubectl -n fission set env deploy/router ROUTER_ENDPOINTSLICE_CACHE_MODE=off` on a single kind version, mirroring the `TEST_GATEWAY_PARENTREF` conditional pattern in `.github/workflows/push_pr.yaml`) so the legacy path stays tested until a future RFC removes it.
- Delete now-dead code: shadow comparator, warm-path `functionServiceMap` usage (it remains inside `executorResolver` for strict mode), dead PoolCache admission arms (`poolcache.go:124-174` for non-strict traffic).
  Earlier phases only add or move; this is the only deletion phase.
- Release notes: defaults changed, RBAC delta, new visible Services, upgrade-order guidance.

## Unit test plan

Conventions per `.claude/resources/test-writing-guidelines.md`: testify (`require` preconditions / `assert` checks), table-driven, `t.Parallel()`, `t.Context()`, fake clientsets over envtest, all under `-race`.

| Test file | Coverage |
|---|---|
| `pkg/router/rewrite_test.go` (phase 0) | Golden: prefix-trim with/without `KeepPrefix`, default-path stripping, forwarded-host header. |
| `pkg/router/canary` tests (phase 0) | Golden: weighted distribution boundaries, ceiling binary search. |
| `pkg/router/endpointcache/index_test.go` | Slice add/update/delete via fake informer; `ready` vs `serving=false` vs terminating endpoints; multiple slices per Service; invalidation fires exactly once per transition; 100-goroutine reader storm during an event storm (race detector is the assertion); provisional-entry confirm and 30 s expiry; waiter-channel wakeup on endpoint-add. |
| `pkg/router/endpointcache/shadow_test.go` | Comparator classification table → `reason` label (match / miss / extra / addr_mismatch); comparator never mutates the index. |
| `pkg/router/transport_test.go` | Retry/backoff/dial-error-classification matrix against a fake `AddressResolver` (impossible today without a live executor stub); cache-invalidate-on-dial-fail; untap-on-return via fake `Tapper`. |
| `pkg/router/resolver_fallback_test.go` | Decision table with two fake resolvers: warm hit / saturated → ensureCapacity race / strict annotation → executor / shadow mode → executor / newdeploy zero endpoints → executor / ensureCapacity 404 → getServiceForFunction degradation. |
| `pkg/executor/executortype/poolmgr/gp_service_test.go` | `ensureFunctionService` idempotency on fake clientset (no Update on no-op — assert via reactor); drifted selector patched; concurrent ensures tolerate `AlreadyExists`; instanceID annotation set; owner ref set. |
| `pkg/executor/dispatch/dispatch_test.go` | Canceled waiter doesn't affect others; error fan-out to all waiters; done-channel closed exactly once; no goroutine leak; N=2 cap honored under 10 queued; cancellation dequeues; 0 = unbounded compat. |
| `pkg/executor/api_*_test.go` (extend) | Handlers against a fake `Provisioner`, zero Kubernetes; `ensureCapacity` 429 path; legacy endpoints unchanged. |
| `pkg/router/functionHandler_test.go` (extend) | Handler wiring with injected resolver/tapper fakes; streaming heartbeat against fake `Tapper`. |

## Integration test plan

`test/integration/`, build tag `integration`; gates on in kind-ci.

New framework helpers:

- `framework/k8s_resources.go`: `WaitForFunctionService(t, ctx, ns, fnName)` and `WaitForEndpointSliceReady(t, ctx, ns, svcName, minReady)` — model on the EndpointSlice listing already in `framework/builder.go`.
- `framework/executor_lifecycle.go`: `ScaleExecutor(t, ctx, replicas) (restore func())` — sibling of `SetExecutorEnv` + `WaitForExecutorRollout`.
- `framework/router.go`: `RouterEndpointSliceMode(t)` so gate-dependent tests `t.Skip` cleanly on the `mode=off` CI leg.

**suites/common** (parallel):

| Test | Assertions |
|---|---|
| `endpointslice_dataplane_test.go` / `TestPoolmgrFunctionEndpointSlice` | Create env + fn; invoke → 200; Service exists in fn namespace with function labels + correct selector; EndpointSlice has ≥1 ready endpoint whose address matches a pod labeled for the function (and `served=true`). Skip unless executor flag on. |
| same file / `TestEndpointCacheWarmHit` | Two sequential invokes; scrape router metrics; `fission_router_endpointcache_hits_total` increased. Skip unless `mode=on`. |
| extend `backend_newdeploy_test.go` | minScale=0 function: first invoke wakes it; slice transitions empty→ready; after idle reap, slice empties again. |
| extend `idle_objects_reaper_test.go` | After poolmgr idle reap: `served` label removed before pod delete (drain order), function Service deleted, slice gone. |
| extend `streaming_test.go` | Long-lived stream stays open while another pod for the same function is reaped mid-stream (slice churn under an active stream). |
| existing canary / MCP / internal-listener / gateway suites | Unchanged — they run with gates on in kind-ci, which is the regression guard. |

**suites/serial** (control-plane mutation; never in common):

| Test | Assertions |
|---|---|
| `executor_down_warm_test.go` / `TestWarmInvokeWithExecutorDown` — **the headline** | Warm a poolmgr fn; `ScaleExecutor(0)`; invoke → 200 served from the endpoint cache (skip if mode≠on); invoke a never-invoked fn → clean error (cold start needs the executor, by design); restore + `WaitForExecutorRollout`. |
| extend the adopt-existing serial test | Warm fn → restart executor → function Service present with refreshed instanceID, fn still invokable; orphan Service (fn deleted while executor down) reaped post-adopt. |
| `mixed_gate_upgrade_test.go` | `SetExecutorEnv("ENABLE_FUNCTION_SERVICES","false")` with router `mode=on`: invokes still succeed via RPC fallback (upgrade-order safety); restore. |

Known-flaky guardrails: `TestPoolCacheRequests/scenario-test7` and `TestGoEnv` flake independently of these diffs — re-run before attributing; all executor restarts stay in `suites/serial`.

## Performance verification plan

New metrics (no function-name labels): `fission_router_endpointcache_size{executortype}`, `fission_router_endpointcache_hits_total`, `fission_router_endpointcache_misses_total`, `fission_router_endpointcache_shadow_mismatches_total{reason}`, `fission_router_endpointcache_fallbacks_total{reason}`, `fission_executor_function_service_ensures_total{result}`.

**(a) Cold start unchanged — the gate.**

- Same-commit comparison: the `mode=off` CI leg vs the `mode=on` legs in one run, via the kind-ci Prometheus (`fission_function_overhead_seconds` histogram + `fission_function_cold_starts_total` / `_errors_total`).
- Dedicated microbenchmark `test/benchmark/tests/cold-start/`: loop of N=30 sequential cold starts of a trivial node function (create → invoke → delete → wait for reap), emit p50/p95.
- Acceptance: **p95 regression <10% vs `mode=off`**, and executor specialization wall time unchanged (the Service ensure is async — verify via the executor histogram).
- Enforcement: manual pre-merge runbook on the phase 1 and phase 3 PRs (kind timing is too noisy for a hard CI gate); the Prometheus comparison is recorded in the PR description.

**(b) Warm-path p99 improvement (poolmgr).**

- Existing k6 burst-load suite (`test/benchmark/tests/burst-load` + the picasso chart tool) run twice on the same kind cluster: `mode=off` then `mode=on`.
- Acceptance: **p99 ≥20% lower** at the burst profile; **hit ratio ≥99%** steady-state (`hits/(hits+misses)`); `fallbacks_total{reason="ambiguous"}` rate documented (expected for high-concurrency functions).
- Enforcement: runbook step, results pasted into the phase 3 PR; optionally a nightly workflow later (not per-PR).

**(c) CPU / goroutines.**

- Existing kind-ci pprof artifact capture; compare heap/goroutine profiles between the off-leg and on-legs.
- Expect: router goroutine count flat across the suite (the informer adds a constant handful, not per-function); executor dispatch samples drop under warm load.
- The `debug-github-ci` profile-analysis flow is the tool for before/after deltas.

**CI-enforced vs runbook-enforced.**

- CI-enforced (cheap, deterministic): shadow-mismatch == 0 promotion step; `TestEndpointCacheWarmHit`; the full functional suite on k8s 1.32/1.34/1.36 with gates on plus the one `mode=off` leg.
- Runbook-enforced (perf, noisy): cold-start microbenchmark thresholds, k6 p99, pprof deltas — a required checklist on the phase 3 and phase 4 PRs.

## Risk register

| Risk | Mitigation | Covered by |
|---|---|---|
| Informer lag → router proxies to a just-deleted pod | Existing retry ladder kept as backstop; dial-fail → quarantine endpoint + one RPC fallback; slice event removes it ≤150 ms | `index_test.go` invalidation; streaming churn test |
| Slice churn floods the index at high pod turnover | resync 0; coalesced per-key copy-on-write updates; O(1) eviction per event; comparator/index never block the proxy path | event-storm unit test under `-race`; k6 burst with small pool forcing recycling |
| apiserver object load at 10k functions | Services exist only for *invoked* poolmgr functions and are reaped on idle; informer label-filtered; ensure is async with retry | leak-check runbook: create/invoke/delete 1k fns → 0 leaked Services/slices after reap |
| Multi-replica router divergence (informers at different RVs) | Divergence degrades to executor RPC, never to wrong-function routing (index keyed by function labels); over-admission bounded at (R−1)×requestsPerPod | fallback decision-table tests; optional `router.replicas=2` CI leg |
| Executor leader failover mid-specialization | Cold path unchanged; Dispatcher waiters honor ctx; adoption covers the new Services | dispatch tests; adopt serial test |
| Upgrade ordering with gates on | Router-first: no Services yet → miss → RPC fallback. Executor-first: Services ignored by old router, inert. `ensureCapacity` 404 → degrade | `mixed_gate_upgrade_test.go` |
| Rollback leaves orphan Services | instanceID annotation + startup orphan cleanup, same mechanism as pools | adopt test orphan case |
| Strict-concurrency workloads silently over-admitted | `fission.io/concurrency-enforcement: strict` annotation documented in release notes and upgrade guide; legacy path retained unmodified | fallback decision-table test (strict → executor) |
| Streaming/tap regressions | Tap RPC retained verbatim; index-served requests still tap; in-flight held to stream drain; drain-aware reap | `streaming_test.go` extension; reaper test |
| NetworkPolicy regressions | None expected: router keeps dialing pod IPs (poolmgr) and Service DNS (newdeploy) — identical flows; kind-ci runs `networkPolicy.enabled=true` on all legs | full suite on kind-ci |

## Operator-facing docs / release notes

- New visible objects: each actively-invoked poolmgr function gets a headless Service (+ controller-managed EndpointSlices) in the function namespace, lifecycle-managed by the executor (created on first invoke, reaped on idle, adopted across restarts).
  Do not edit them.
- RBAC delta: router role gains `discovery.k8s.io/endpointslices` + core `services` get/list/watch; Helm upgrade applies it; bespoke-RBAC users must add it before enabling `shadow|on`.
- NetworkPolicy: no changes required.
- Recommended adoption path: enable `executor.functionServices.enabled` → run `mode=shadow` and watch `fission_router_endpointcache_shadow_mismatches_total` (should be 0) → switch to `mode=on`.
  Rollback is a values flip; no migration.
- Resilience headline: with `mode=on`, warm traffic keeps flowing during executor outages and upgrades; cold starts still require the executor.
- Semantic note: per-pod concurrency enforcement becomes per-router-replica; functions needing exact global single-concurrency set `fission.io/concurrency-enforcement: strict`.

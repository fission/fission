# RFC-0013: Incremental Router Route Updates

- Status: Implemented (phases 0–2; phase 3 not built — its benchmark gate was not crossed, see "As shipped")
- Tracking issue: —
- Supersedes: —
- Targets: Fission v1.27+ (phases 0–2); phase 3 conditional on benchmark evidence
- Requires: no new dependencies (gorilla/mux is retained through phase 2); no Kubernetes floor change
- Related: [RFC-0002](0002-endpointslice-native-data-plane.md) (addressResolver/tapper seams, shadow-promotion playbook), [RFC-0014](0014-router-hot-path-efficiency.md) (hot-path efficiency; handler-reuse interlock — shipped, including the uncached resolver this RFC's route table builds on)

## Summary

Every HTTPTrigger or Function change today rebuilds the router's entire public and internal muxes from full LISTs — at 10k triggers, a single canary weight tick re-allocates and re-registers every route.
This RFC splits **route shape** (path/prefix/methods/host — changes rarely, human-driven) from **handler** (function map, canary weights — changes constantly): handlers live behind a stable atomic pointer, so the steady-churn class becomes a single pointer store with zero mux rebuild, while gorilla/mux is retained as a "materializer" rebuilt only on shape changes.
A second, conditional phase replaces gorilla's per-request **linear regex scan** (O(triggers) per request) with a native incremental matcher behind a shadow-comparison gate — built only if benchmarks show the scan matters at target scale.

## Motivation

Verified anatomy of the current cost (all against main):

- `buildMuxes` (`pkg/router/httpTriggers.go:232-442`) reconstructs two complete `mux.Router` instances from full LISTs of HTTPTriggers and Functions out of the Manager cache, on **every** trigger or function reconciliation, debounced 20ms (`httpTriggers.go:95`).
  Per trigger it allocates a `functionHandler` (logger `WithName`, resolved function map, canary weight list, CORS wrap) and registers 1–2 routes; the internal mux gets 2 routes per Function.
- The rebuild is cache-fed (zero network), so the cost is allocation + gorilla route compilation: gorilla/mux v1.8.1 **compiles a regexp per registered route**, even for literal paths.
- **Canary weight updates change `FunctionWeights` — they bump the trigger generation but do not change the route's match shape.**
  Today each weight tick is a full O(triggers + functions) rebuild; under steady canary traffic-shifting this is the dominant, recurring cost.
- Second-order problem: gorilla's `Router.Match` is a **linear scan over all routes per request** — at 10k triggers the per-request match is O(10k) regex tests on the proxy hot path.
- gorilla/mux has no route-removal API, which is why full rebuilds exist at all.

What already works and must be preserved: trigger conditions PATCH only on transitions (`httpTriggers.go:506-542`); `UseEncodedPath` is applied inside `buildMuxes` (the CLAUDE.md gotcha — applying it only at startup gets dropped on the first swap); the GHSA-3g33-6vg6-27m8 public/internal split is pinned by `httpTriggers_test.go`; the atomic mux swap under load is pinned by `mutablemux_test.go`.

## Goals

- Canary weight ticks, function updates, and resolve refreshes update routing state in O(1) with zero mux rebuild.
- Trigger create/delete (shape changes) stay correct and bounded; conditions are marked per-changed-trigger.
- Defined, documented route-precedence and conflict semantics (today: accidental list order).
- A conditional path to O(path-length) request matching at 10k+ triggers, gated on evidence.

## Non-goals

- Replacing gorilla/mux in phases 0–2.
- Touching the ingress / Gateway API route providers (reconciled separately from the mux).
- Changing the `addressResolver`/`tapper` seams (RFC-0002).
- Admission-time **rejection** of conflicting routes (conditions only; a webhook is an open question).

## Design

### Route table (`pkg/router/routetable/`)

```go
type HandlerRef struct{ h atomic.Pointer[http.Handler] } // registered once; swapped freely
func (r *HandlerRef) ServeHTTP(w, req)                   // one pointer load + delegate

type RouteSpec struct {
    // identity / change detection
    TriggerUID types.UID
    Namespace, Name, TriggerRV string
    FnRVs map[string]string            // resolved fn name -> ResourceVersion
    // match shape (equality drives "shape change")
    ExactPath, PrefixPath, Host string
    Methods []string                   // sorted
    Created metav1.Time                // precedence tiebreak
    // mutable side
    Handler *HandlerRef
}

type Table struct {
    mu       sync.Mutex
    public   map[types.UID]*RouteSpec
    internal map[types.NamespacedName]*internalSpec
    fnIndex  map[types.NamespacedName]sets.Set[types.UID] // fn -> referencing triggers
}

func (t *Table) ApplyTrigger(spec *RouteSpec) ApplyResult // NoChange|HandlerSwapped|ShapeChanged|Conflict
func (t *Table) DeleteTrigger(uid types.UID) ApplyResult  // always ShapeChanged
func (t *Table) ApplyFunction(fn *fv1.Function) ApplyResult
func (t *Table) Snapshot() []*RouteSpec                   // precedence-sorted, for materializers
```

The reconcilers (`pkg/router/reconciler.go:42-85,105-121`) become per-event diff sources, replacing the blanket `syncTriggers` signal:

- **Trigger reconcile**: compute the `RouteSpec` for that trigger only — config validation, `resolver` resolution, `functionHandler` + CORS wrap built only when `(TriggerRV, FnRVs)` changed.
  Shape equal → store the new handler into the existing `HandlerRef` (one atomic swap — **this is the canary-weight path**); shape changed → replace the spec and signal the materializer's debouncer.
  Mark only this trigger's condition.
- **Function reconcile**: internal route handler swap (keyed by fn ns/name, stable across updates) + `fnIndex` lookup → re-resolve and swap each referencing trigger's handler.
  Match shapes never change from a function event, so **function churn never rebuilds either mux**.
- **Resolve failure semantics (parity)**: function NotFound → remove the route + `Ready=False` (today's skip behavior); transient reader error → return the error from Reconcile (controller-runtime requeue) and keep the last-known-good route — parity with today, where a LIST error keeps the old mux.
- **Drift guard**: a periodic full-resync (LIST → table diff) catches any missed event, with a `fission_router_route_resync_drift_total` metric whose CI acceptance bar is zero.

`FnRVs` in the spec also supersedes the resolver cache's TTL-scan invalidation with precise per-trigger invalidation (interlocks with RFC-0014's refCache removal).

### Handler indirection

`HandlerRef` is registered into the mux once per shape; all volatile state (canary weights, function snapshots, resolved policy) lives in the swappable inner handler.
A request entering the wrapper sees one consistent handler — one consistent canary distribution — for its lifetime; in-flight requests are untouched by swaps (same property the atomic mux swap gives today, pinned by `mutablemux_test.go`).

### Gorilla materializer

`buildMuxes` becomes `Materialize(snapshot)`: today's body minus resolution and handler allocation, iterating the precedence-sorted snapshot and registering each spec's `HandlerRef`.
It runs (debounced) only when a diff reports `ShapeChanged`.
**`UseEncodedPath` and the middleware chain (panic recovery → metrics → auth) are applied inside the materializer** — the CLAUDE.md gotcha carries over verbatim.
Router-owned routes (`/router-healthz`, `/readyz`, `/_version`, the GKE `/` fallback) and the GHSA public/internal split live in the materializer, never in the table.

### Precedence and conflict semantics (phase 2 — the only user-visible change)

Today conflict outcomes are an accident of cache list order; two triggers can overlap silently.
Specified precedence:

1. Host-qualified routes before host-less routes (parity — Fission hosts are literal, gorilla already disambiguates).
2. Exact path beats prefix path.
3. Among prefixes, longest prefix wins.
4. Method sets filter rather than rank (mismatch contributes to 405, matching gorilla's `ErrMethodMismatch`).
5. Exact-duplicate tiebreak: oldest `creationTimestamp`, then lexicographic namespace/name; the loser stays registered (shadowed) and gets `RouteAdmitted=False, reason=RouteConflict` naming the winner — conflicts become observable for the first time.

For non-overlapping route sets (the overwhelmingly common case) this is behavior-identical to today, since order only matters when two routes match the same request.

### Native matcher (phase 3 — conditional, gated)

`pkg/router/routematcher/`: host buckets → method-filtered exact map + longest-prefix radix, consuming the same table diffs incrementally with a COW root swap per applied batch.
Gate: `ROUTER_MATCHER=gorilla|shadow|native`, default `gorilla`; shadow mode runs both matchers and emits `fission_router_matcher_shadow_mismatches_total` — promotion requires zero mismatches over a kind-ci burn-in plus a differential fuzz suite (random route sets × request corpus; gorilla and native must agree on matched trigger and status), the exact RFC-0002 playbook.
**Build trigger stated up front: do not implement phase 3 unless the phase-0 10k-route benchmark shows p99 proxy match overhead > 1ms, or a user reports it.**

### Consistency model

Route-level atomicity suffices: the only cross-object invariant is the exact+prefix route *pair* a non-slash-prefix trigger registers, and both derive from one `RouteSpec` and swap together.
Cross-trigger snapshot consistency was never client-observable, and the public/internal swap is already non-transactional today.

### Observability

`fission_router_routes_total{listener}`, `route_table_applies_total{result}`, `mux_rebuilds_total{listener,reason}`, `route_resync_drift_total`, and (phase 3) the shadow mismatch counter.

## Configuration

- `ROUTER_INCREMENTAL_ROUTES` (default `true` once phase 1 ships) — escape hatch reinstating the legacy full-rebuild loop for one release; the legacy loop is ~80 lines and stays compiled.
- `ROUTER_MATCHER=gorilla|shadow|native` (phase 3 only), default `gorilla`.

## Backward compatibility

- Phase 1 is behavior-identical for non-overlapping route sets; in-flight requests keep the same swap guarantees.
- Phase 2 changes overlap-winner outcomes deliberately (nondeterministic → specified) — release-noted, with `RouteConflict` conditions making every affected trigger visible, and the phase-1 escape hatch still active in that release.
- The GHSA public/internal split, CORS deny-by-default, and rewrite semantics are pinned by the existing test suite, which must pass unchanged in every phase.

## Rollout phases

**Phase 0 — pin behavior + baselines.**
Route-shape derivation golden tests (trigger spec → expected exact/prefix/methods/host registrations, including the non-slash-prefix dual registration, empty-Methods edge, CORS OPTIONS append, GKE `/` route).
`BenchmarkBuildMuxes{100,1k,10k}` and `BenchmarkMuxMatch10k` baselines.
`test/benchmark/tests/route-churn/generate.sh` (scale-index pattern: N triggers + N functions as API objects only — no pods needed; `churn M` rewrites M canary weights/sec).

**Phase 1 — route table + handler indirection.**
New `pkg/router/routetable/`; reconcilers become diff sources; gorilla materializer; per-trigger conditions; periodic resync; escape hatch env.
Tests: `Table.Apply*` decision table, `HandlerRef` swap under concurrent ServeHTTP (`-race`), fnIndex maintenance; the full parity suite unchanged.

**Phase 2 — precedence + conflict conditions.**
Deterministic snapshot ordering per the spec; `RouteConflict` conditions; release note.

**Phase 3 (conditional) — native matcher.**
Differential fuzz; shadow burn-in; promotion per the gate.

## Verification

- Unit (`-race`): table decision tables; handler-swap concurrency; precedence-sort property tests; (phase 3) differential fuzz.
- Parity (must pass unchanged): `httpTriggers_test.go` (GHSA pins, health/version routes, CORS), `rewrite_test.go`, canary tests, `mutablemux_test.go`.
- No-route-flap test (extends `mutablemux_test.go`): sustained requests against one stable trigger plus one in-flight streaming request while 1k unrelated triggers and canary weights churn; zero non-200s, the stream survives handler swaps.
- Benchmarks + acceptance bars at 10k triggers + 10k functions:
  - canary weight update: **zero mux rebuilds** (metric-asserted), apply < 1ms, O(1) allocs;
  - trigger create/delete: phase-1 rebuild ≤ today's baseline CPU; phase-3 apply ≤ 100µs;
  - p99 proxy latency within ±5% of idle during 100 updates/s churn;
  - router RSS flat over a 10-minute churn soak (no old-handler leak).
- CI: one leg pins `ROUTER_INCREMENTAL_ROUTES=false` through the deprecation release; phase 3 adds a `ROUTER_MATCHER=shadow` burn-in leg with mismatch==0 as the machine-checked promotion step.

## Alternatives considered

- **Namespace-sharded muxes**: rejected — public `RelativeURL`/`Prefix` paths carry no namespace key, so shard dispatch requires a first-level path matcher, which is the native matcher in disguise.
- **Straight to the native matcher**: owns matching semantics (encoded paths, trailing slashes, 405-vs-404, host matching, prefix boundaries) with no fallback; phase 1 captures nearly all of the rebuild win at a fraction of the risk.
- **Handler cache without indirection** (reuse handlers, still re-register): still pays a regexp compile per route per rebuild on every canary tick.
- **Per-trigger gorilla subrouters**: no removal API at any level; the linear scan remains.

## Open questions

- Bounded-staleness window for keep-last-good on transient resolve errors?
- Should `RouteConflict` eventually become an admission-webhook rejection?
- Does phase 3 subsume the internal mux or only the public one (the internal mux is namespace-structured and cheap)?

## As shipped

Phases 0–2 landed together (the PR carries per-phase commits); phase 3 (the native matcher) was **not** built, per its own gate:
the phase-0 benchmark put gorilla's worst-case match at 10k routes at ~0.7ms on Apple M2 (last-registered route; ~70µs at 1k routes), under the >1ms build trigger.
The evidence is recorded in the PR; the gate stays armed — a user report or a slower-hardware measurement above 1ms reopens it.

Measured on the shipped implementation (Apple M2, fake-cache resolver):

- One canary weight tick with 10k routes in the table: **~15µs / 9.6KB / 97 allocs** through the incremental path, vs **~208ms / 303MB / 3.54M allocs** for the same event through the legacy full rebuild (BenchmarkIncrementalWeightTick vs BenchmarkBuildMuxes/triggers=10000) — the steady-churn class is O(1), roughly four orders of magnitude off the rebuild cost.
- The no-route-flap test drives sustained requests through a stable route while handler swaps and unrelated materializations churn concurrently under `-race`: zero non-200s.

Differences from the proposal, decided during implementation and review:

- **Generation, not ResourceVersion, keys change detection** (`TriggerGen`/`FnGens` rather than the proposed `TriggerRV`/`FnRVs`): the reconcilers run behind `GenerationChangedPredicate`, so the resync must use the same notion of "changed" — RV keying would have counted every status-only write (the router's own conditions, the executor's function readiness) as drift and rebuilt a handler per pass.
- **A failed materialize is sticky**: the drained condition batch is re-queued, a dirty flag stays set, and the resync loop re-signals until a build succeeds; `fission_router_mux_materialize_failures_total` and `fission_router_route_resync_failures_total` make the materializer's and the drift guard's own failure modes alertable (review finding — a consumed signal must not strand table state out of the served mux).
- **An unresolved-trigger index** keeps the trigger→function edge alive when the function does not exist yet, so the function's create event re-admits the route immediately via the cascade (the trigger-before-function apply ordering would otherwise wait for the next resync).
- **Conflict losers are guarded on the apply path too**: the `NoChange`/`HandlerSwapped` condition write consults the shadow set, so a weight tick or resync pass cannot flip a shadowed trigger back to `RouteAdmitted=True` (review finding).
- The routes gauge shipped as `fission_router_routes` (promlinter: `_total` implies a counter).
- A second registration-level fix fell out of the no-flap race test: gorilla's `Route.Methods()` uppercases the slice it is handed **in place**, so the legacy path had been silently mutating informer-owned trigger objects; both paths now clone per registration.
- `FunctionNotFound` became an explicit `RouteAdmitted=False` reason — previously an unresolvable trigger 404'd while its condition claimed the route was admitted.

The escape hatch (`ROUTER_INCREMENTAL_ROUTES=false`, chart `router.incrementalRoutes`) ships active for this release with the v1.34 CI leg pinned to it (the same leg that pins the RFC-0002 legacy data plane), and is scheduled for removal one release later.

## Risks (top 3, with mitigations)

1. **Phase-3 matching-semantics regressions** (encoded paths / `UseEncodedPath`, the exact+`prefix/` dual-registration boundary that prevents `/foo` matching `/foobar`, 405-vs-404, host matching).
   Mitigation: differential fuzz, shadow mode with a machine-checked zero-mismatch bar, default-gorilla gate — this risk is precisely why phase 3 is conditional.
2. **Table/cache drift**: a missed or misordered event leaves a stale route serving or a live trigger 404ing, where today's full rebuild self-heals every event.
   Mitigation: periodic full-resync diff with a drift metric (zero-in-CI acceptance), controller-runtime requeue semantics preserved.
3. **Phase-2 overlap-winner flips** for users unknowingly relying on accidental registration order.
   Mitigation: `RouteConflict` conditions surface every affected trigger before and after; release note; escape hatch active for that release.

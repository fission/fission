# RFC-0014: Router Hot-Path Efficiency — Shared Transport, Per-Request Allocation Diet, Resolver-Cache Removal

- Status: Proposed
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.27
- Requires: no new dependencies; no Kubernetes floor change; no user-facing API change
- Related: RFC-0002 (perf harness, structural seams; the warm path this RFC now optimizes), RFC-0008 (streaming settle invariants that constrain the refactor), RFC-0013 (handler-reuse interlock; the deferred per-route ReverseProxy is designed jointly)

## Summary

The router constructs a **new `http.Transport` per proxied request** (`pkg/router/transport.go:137` via `getDefaultTransport()` at `transport.go:374-391`), which silently defeats HTTP keep-alive entirely: every request to a function pod pays a fresh TCP dial, the configured `MaxIdleConns: 100` is dead config, and the `otelhttp` wrapper is rebuilt per attempt too (`transport.go:276`).
This RFC fixes that bug-class finding with one shared, properly sized transport; puts the per-request handler path on an allocation diet (pre-built loggers, per-route proxy policy); and removes the `functionReferenceResolver` refCache — a TTL'd cache of the Manager informer cache that sits off the hot path, costs two goroutines plus an O(n) copy-walk per Function reconcile, and whose staleness class is already mitigated elsewhere.

## Motivation

Verified against main:

- **Transport**: `RoundTrip` calls `getDefaultTransport()` (value receiver — copies the whole ~15-field struct) per request; the comment says it exists "to prevent the value of http.DefaultTransport from being changed by goroutines" — a safety choice that created the bug.
  Per-attempt code then mutates `transport.DialContext` with `Dialer{Timeout: executingTimeout, KeepAlive: keepAliveTime}` (`transport.go:248-251`).
  Function pods are plain HTTP `:8888`, so the per-request cost is a TCP handshake + slow-start — pure waste under steady load, most visible at high RPS and larger responses.
- **Per-request allocations** (`pkg/router/functionHandler.go:40-156`): a `RetryingRoundTripper` literal conflating immutable per-route config (resolver, tapper, trigger, params) with per-request state (serviceURL, tapURL, release, retryCounter — mutated at `transport.go:191-193,256`); `logger.WithName("roundtripper")` per request; `resolveProxyPolicy` per request (a pure function of fn + timeout); an `httputil.ReverseProxy` + closures per request; a metrics+tap goroutine (two spawn sites: `functionHandler.go:110-115` and the error handler).
- **refCache** (`pkg/router/functionReferenceResolver.go`): keyed `(namespace, triggerName, triggerRV)` with a 1-minute TTL; each `pkg/cache` instance runs its own actor + expiry goroutines; `resolve` is called **only from `buildMuxes`** — results are closed into handlers at build time, so the cache is not on the request path at all.
  Its known staleness class (trigger-RV keying misses function updates) is already mitigated by `resolver_executor.currentFunction` re-reading before any specialization.
  `invalidateForFunction` does a full `Copy()` map walk on every Function reconcile (hook at `pkg/router/reconciler.go:117`).

## Goals

- Restore connection reuse on the proxy hot path with retry/quarantine semantics byte-identical to today.
- Reduce per-request allocations without touching the delicate streaming-settle structure.
- Delete the redundant resolver cache and its reconcile-time costs.

## Non-goals

- No retry-ladder semantic change — the transport/settle/streaming test matrices must pass **unchanged**.
- No `pkg/cache` removal (`functionServiceMap` still uses it for the legacy/fallback address path; rationalization direction noted only).
- No per-route `ReverseProxy` in this RFC (designed below, explicitly deferred; interlocks with RFC-0013's handler reuse).
- No user-facing API or chart schema change (one new tuning env var only).

## Design

### Shared transport with a context-carried dial ladder

One `*http.Transport` per router process (plus a once-wrapped `otelhttp.NewTransport` sibling for the non-websocket path), built where `tsRoundTripperParams` is constructed and stored on it.
`disableKeepAlive` is process-wide config, so a single transport configured from it at startup suffices — no per-variant pool.

The critical constraint: the per-attempt `Dialer.Timeout = executingTimeout` is not just a timeout, it is the **backoff-scaled fast-retry ladder for cold pods** — a not-yet-listening pod must fail the dial quickly so the loop re-resolves.
It is preserved exactly via a context value read by a single shared `DialContext`:

```go
transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
    if d, ok := ctx.Value(dialTimeoutKey{}).(time.Duration); ok {
        var cancel context.CancelFunc
        ctx, cancel = context.WithTimeoutCause(ctx, d, errDialLadderTimeout)
        defer cancel()
    }
    return sharedDialer.DialContext(ctx, network, addr) // Dialer{KeepAlive: params.keepAliveTime}
}
```

`RoundTrip` stashes the attempt's `executingTimeout` into the per-attempt request context; a pooled-conn hit skips the dial entirely (correct — there is nothing to time out).
Classification is unchanged: a ctx-deadline dial failure is `*net.OpError{Op: "dial"}` with `Timeout() == true` — identical through `network.Adapter` (`pkg/error/network/error.go`) to today's `Dialer.Timeout` path (which `net` implements as a ctx deadline internally), and `transport.RoundTrip` is called directly so there is no `*url.Error` wrapping.
A pin test makes this an invariant (see Verification).

Sizing (the current `MaxIdleConns: 100` never mattered because the transport never survived a request):

- `MaxIdleConnsPerHost`: new env `ROUTER_ROUND_TRIP_MAX_IDLE_CONNS_PER_HOST`, default 32 — each poolmgr pod is its own host (`ip:8888`), and the Go default of 2 would throttle per-pod reuse well below `requestsPerPod` ceilings.
- `MaxIdleConns`: 1024 (explicit bound for fd hygiene).
- `IdleConnTimeout`: 90s → **30s**, shorter than the poolmgr idle-reap window (120s), bounding how long a pooled conn can outlive its pod.

**Pooled conns vs dead pods** is the one real behavioral shift: today a dead pod fails at dial (refused/timeout → quarantine/retry ladder); with pooling, a stale conn fails at write/read instead.
Graceful reaps send FIN, which evicts the conn from the pool before reuse; the residual race is covered by Go's automatic retry of replayable requests on a reused-conn failure; a non-replayable POST surfaces the error to the caller (a 500 via the proxy error handler, logged with a distinct 'pooled connection failed mid-request' line) — documented and pinned by a unit test.
The quarantine path is unaffected (it keys on dial errors, and a write-failure on a reused conn correctly is not one).
`disableKeepAlive` — which today is nearly meaningless — becomes the documented escape hatch back to per-request connections.

### Handler hoists (allocation diet)

Computed once in `buildMuxes` and stored on the route's `functionHandler`:

- The round-tripper logger (`logger.WithName("roundtripper")` moves out of the request path).
- A per-(route, backend-UID) map of `proxyPolicy` + `funcTimeout` (`resolveProxyPolicy` is a pure function; the map covers the canary case, where `fh.function` is selected per request from the weight distribution).

Kept deliberately per-request:

- The `RetryingRoundTripper` itself — it *is* the per-request state (one small allocation; `sync.Pool` rejected: the streaming-deferred settle and `closeContextFunc` lifetimes make pooling error-prone for negligible gain).
- The `ReverseProxy` + closures — a per-route proxy requires carrying per-request state through the request context so shared `ModifyResponse`/`ErrorHandler` can find it; the win is small next to the transport fix and the risk touches the streaming settle block, the most delicate code in the router.
  The context-key design is recorded here; implementation is deferred and revisited alongside RFC-0013's handler reuse.
- The metrics+tap goroutines (async is required — the tap send must never block response delivery; a worker pool adds queue/drop/shutdown semantics to save microseconds).

An optional `roundTripperConfig` field-regrouping makes the immutable/mutable boundary explicit, as a refactor not a behavior change.

### refCache removal

`buildMuxes` resolves directly against the informer cache: `resolve()` stays as a thin uncached dispatch on `FunctionReference.Type` (smallest diff; `resolveResult` shape unchanged), and the following are deleted:

- the `refCache` field and its `MakeCache` construction (−2 goroutines),
- `invalidateForFunction` and its `Copy()` walk,
- the reconciler hook at `reconciler.go:117` (the Function reconcile still triggers the rebuild — that is exactly what makes the cache redundant).

Behavior delta: N informer `Get`s per rebuild instead of cache hits — in-memory map reads, negligible; canary weight lists are rebuilt per rebuild with identical results; the up-to-1-minute stale-snapshot window the cache could serve is gone outright (and was already moot for specialization via `currentFunction`).

## Configuration

| Knob | Default | Notes |
|---|---|---|
| `ROUTER_ROUND_TRIP_MAX_IDLE_CONNS_PER_HOST` | 32 | new; tuning, not gating |
| `ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE` | false | pre-existing; becomes the meaningful escape hatch |
| `IdleConnTimeout` | 30s | internal constant, rationale documented |

No new feature gate: the change is internal, and the keep-alive escape hatch already exists.

## Backward compatibility

- Failure-mode shift for non-replayable requests racing pod shutdown (documented above; integration-tested; GETs retry transparently).
- `disableKeepAlive` semantics upgrade called out in chart docs.
- Everything else is allocation/log-shape only; retry, settle, quarantine, and streaming semantics are frozen by the existing test matrices.

## Rollout phases (one PR each, bisectable)

**Phase 0 — baseline harness.**
Alloc benchmarks (`BenchmarkHandlerClassic`, `BenchmarkRoundTripWarm` against a fake resolver + httptest backend, `b.ReportAllocs`) plus a dial-counting listener wrapper; capture the k6 warm-path baseline into `rfc/perf-results`.
No production change — lands first so phases 1–2 have honest before/after deltas.

**Phase 1 — shared transport (the bug fix, isolated).**
Build transport + otel wrapper once; context-value dial ladder; delete `getDefaultTransport`; sizing knobs; classification pin test; chart-docs note for `disableKeepAlive`.

**Phase 2 — handler hoists.**
Per-route logger; per-(route, backend-UID) policy/timeout map; optional config/state field regrouping.
Benchmarks show the allocs/op delta.

**Phase 3 — refCache removal.**
As designed; independent of phases 1–2 (sequenced last to keep perf-sensitive diffs first).

**Deferred (recorded, not scheduled):** per-route `ReverseProxy` via context-carried per-request state — re-evaluate with RFC-0013.

## Verification

- **Connection reuse (headline)**: with the dial-counting listener, N serialized warm requests to one backend perform ≤ `MaxIdleConnsPerHost` dials after warmup (today: exactly N).
- **Allocs**: `go test -bench -benchmem` — allocs/op strictly reduced after phases 1–2; numbers recorded in `rfc/perf-results`.
- **k6 warm path** (RFC-0002 runbook): predicted p50 down by roughly one intra-cluster RTT plus slow-start effects, throughput up at fixed VUs; acceptance = p50 improvement observed, no p99 regression beyond noise.
- **Semantics frozen**: `transport_test.go` retry matrix, settle dispatch matrix, streaming release tests, `functionHandler_test.go`, canary tests pass **unchanged** (the only deletion is `getDefaultTransport`'s own coverage).
- **Classification pin**: unit test dialing a blackhole (TEST-NET `192.0.2.1`) with a 1ms ladder value asserts `IsDialError() && IsTimeoutError()`.
- **Stale-conn behavior**: integration test kills the backend between requests; GET transparently retried by the transport; POST behavior documented and asserted.
- **refCache removal**: resolver tests rewritten against direct resolution; router idle goroutine count drops by 2 (assertable via the CI pprof artifacts); CI pprof heap shows the `getDefaultTransport` allocation site gone.

## Alternatives considered

- Per-(keepalive-setting) transport variants — unnecessary; the setting is process-wide.
- `sync.Pool` for the round tripper — lifetime hazards (streaming settle, closeContextFunc) for negligible gain.
- A worker pool for the metrics/tap goroutines — queue/drop/shutdown complexity to save microseconds; revisit only if profiles show scheduler pressure.
- Per-route `ReverseProxy` now — deferred (risk concentrated in the streaming settle block; small residual win).
- Keeping refCache with function-RV keying — still a cache of an O(1) in-memory cache; deletion is simpler and removes the staleness class instead of re-keying it.

## Open questions

- Should `MaxIdleConnsPerHost` derive from poolmgr `requestsPerPod` ceilings instead of a flat default?
- Is the 30s `IdleConnTimeout` right once RFC-0002's drain-aware reaping (unlabel → grace → delete) is considered — should it key off the drain grace floor (30s) explicitly?

## Risks (top 3, with mitigations)

1. **Pooled conns to dead pods change the error mode** (highest): write/read failures replace dial failures for stale conns.
   Mitigations: FIN-driven pool eviction covers graceful reaps; 30s idle timeout; TCP keepalive; Go's replayable-request auto-retry; the integration test above; the `disableKeepAlive` escape hatch.
2. **Dial-ladder semantic drift**: mitigated by the context-value design (ladder values unchanged), the classification pin test, and the frozen transport test matrices.
3. **Pool sizing wrong by default**: too low throttles hot pods, too high holds fds across thousands of pods — mitigated by the env knob and k6 evidence before/after.

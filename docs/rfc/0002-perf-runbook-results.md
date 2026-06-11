# RFC-0002 pre-phase-4 perf runbook — results (2026-06-11)

Status: **all three acceptance bars cleared** — phase 4 (defaults flip) is perf-cleared from this runbook's perspective.

## Setup

- Local kind v1.36.1 (single node, 8-CPU/8GB Docker VM, Apple Silicon), Fission from main commit `29bc8644` (PR #3485 merge), deployed via `SKAFFOLD_PROFILE=kind make skaffold-deploy`.
- Gates-off = chart defaults; gates-on = `helm upgrade --reuse-values --set executor.functionServices.enabled=true --set router.endpointSliceCache.mode=on`.
- Driver: `test/benchmark/rfc0002-perf-runbook.sh`; benchmarks: `test/benchmark/tests/{cold-start,warm-path}`.
- Raw data (k6 summaries, cold-start CSVs, router metric snapshots): [0002-perf-data/](0002-perf-data/).
- Warm-path: k6, 20 constant VUs × 60s, one pre-warmed poolmgr python function with `requestsPerPod=200` (one pod serves all VUs, so the measurement is router overhead, not function capacity).
- Cold-start: 30 sequential first-invocations of fresh functions against a warm pool, with a pool-ready gate between iterations; 404s (mux propagation) excluded from timing.

## Results vs acceptance bars

| Metric | Bar | Gates off | Gates on | Delta | Verdict |
|---|---|---|---|---|---|
| Warm p99 | ≥20% lower | 91.8ms | 22.1ms | **−75.9%** | PASS |
| Cold-start p95 | <10% regression | 1154.7ms | 144.7ms | −87.5% (no regression) | PASS |
| Steady-state hit ratio | ≥99% | n/a | 212,846 hits / 31 misses / 0 fallbacks = **99.985%** | PASS |

Secondary observations:

- Warm median is flat (4.4ms → 4.6ms): the median was already dominated by function execution; the win is entirely in the tail, where the executor RPC + PoolCache serialization lived.
- Closed-loop throughput in the identical 60s/20-VU test went 12,730 → 212,837 requests (**16.7×**) with failures 0.23% → 0.00%: gates-off, RPC-path stragglers (avg 110ms, some 30s timeouts) stall VUs; gates-on, every request is index-admitted at ~4.6ms.
- Cold-start p50 110.3ms gates-on vs 125.7ms gates-off — byte-identical path confirmed within noise (the gates-off p95/max were polluted by laptop scheduling outliers on early iterations, max 9.4s; the gates-on sweep was uniformly 89–184ms).
- Observability counters from the merged review fixes all read clean post-run: `fission_router_endpointcache_mode{requested="on",effective="on"} 1`, `quarantines_total 0`, `tap_flush_errors_total 0`.

## Caveats

- Single-node laptop kind cluster: absolute numbers are not representative; the off-vs-on *deltas* on the same hardware are the signal.
- The gates-off warm run carried a 0.23% failure tail (30s timeouts under closed-loop pressure on the RPC path) — itself a data point for what the RFC removes, but it inflates the gates-off p99 slightly; even the expected-response-only gates-off p99 band (~92ms) clears the −20% bar against 22.1ms.
- First (discarded) attempt over-drove the cluster: 50 VUs at default `requestsPerPod=1` makes poolmgr specialize a pod per concurrent request — a pod storm that measures node saturation, not the router. The committed warm-path benchmark pins `requestsPerPod` high for exactly this reason.

## What this clears / what remains for phase 4

- Cleared: the perf gate (this runbook) for flipping `executor.functionServices.enabled=true` + `router.endpointSliceCache.mode=on` defaults in the next minor after v1.26.
- All of it has since shipped: quarantine TTL in [#3487](https://github.com/fission/fission/pull/3487); the defaults flip, newdeploy `endpointLB` flag, shadow-comparator removal, `EnsureCapacity` interface fold, `settle()` accounting collapse, and the `concurrency-enforcement` webhook warning in the phase-4 change (see [0002-implementation-plan.md](0002-implementation-plan.md) for the two as-shipped deviations).

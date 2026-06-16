# Performance & Benchmarking Plan — Multi-Namespace Tenancy

**Companion to:** [prd.md](./prd.md) · [testing-plan.md](./testing-plan.md)

Performance is a first-class acceptance gate, not an afterthought.
The whole premise of issue #3298 is a *performance* failure (mass restart, function timeouts, node scale-up), and PR #3476's watch-all model trades isolation for a memory/RBAC profile we must out-perform on the isolation axis without regressing latency.
This doc defines what we measure, how, against which baselines, and the gates that block merge.

---

## 1. Two baselines, always

Every benchmark is reported against **two** reference points so we can prove "better, not just different":

- **Baseline-Today** — current `main` with `additionalFissionNamespaces` (per-namespace everything, env-frozen, restart-on-add).
- **Baseline-PR3476** — the watch-all + cluster-wide-RBAC model, as the alternative under consideration.

The design wins if it matches or beats Baseline-Today on latency/restart and beats Baseline-PR3476 on isolation-cost (memory/RBAC blast radius) without a latency penalty.

---

## 2. Headline metric — onboarding disruption (the #3298 fix)

This is the single most important number and a **hard gate**.

| Metric | Baseline-Today | Target (this design) | How measured |
|---|---|---|---|
| Control-plane pod restarts when adding 1 namespace | all control-plane Deployments roll | **0** | Capture pod UIDs before/after onboarding in the serial test; diff. |
| Function 5xx/timeout count during onboarding | non-zero (executor unavailable window) | **0** | Drive steady traffic at an existing-namespace function during onboard; count non-200s. |
| Re-specialization events on existing namespaces | full storm | **0** | Count pod creations in existing function namespaces during the window. |
| Node scale-up triggered | 2–3 extra nodes (reporter) | **0** | Watch Node count / cluster-autoscaler events over the window. |
| Time-to-Ready for the new tenant | n/a (restart-bound) | **< 10 s** p95 | FissionTenant `Ready=True` transition time from CR/label create. |

Measured in the `serial/tenant_no_restart_test.go` integration test and reproduced in a dedicated load scenario (§7).

---

## 3. Scaling sweep — cost vs namespace count

The architectural claim is "Tier-A is one informer (flat in N); only Tier-B scales linearly."
Prove it with a sweep at **N = {1, 10, 50, 100, 250}** tenant namespaces, each holding a small fixed function set.

| Dimension | Expectation | Method |
|---|---|---|
| Executor RSS / Go heap | Sub-linear; Tier-A flat, Tier-B (Secret/ConfigMap informers) the only linear term | CI pprof heap capture (kind-ci observability) at each N; plot heap vs N. |
| Router RSS / heap | Roughly flat (EndpointSlice + CRD watches are cluster-wide, single informer) | pprof heap; compare to Baseline-Today (which is also per-namespace today). |
| Goroutine count | Flat for Tier-A; bounded per-tenant increment for Tier-B caches | pprof goroutine profile; assert no unbounded growth. |
| API server LIST/WATCH connections | Tier-A: O(1) cluster-wide watches; Tier-B: O(N) Secret/ConfigMap watches | apiserver `apiserver_longrunning_requests` / watch count; compare to Baseline-Today O(N-per-type) and Baseline-PR3476 O(1)-but-cluster-wide-secrets. |
| Informer initial-sync time on N tenants | Bounded; Tier-A one sync, Tier-B parallel per-tenant | Manager `WaitForCacheSync` duration. |

Explicit honesty in the report: Tier-B Secret/ConfigMap watches are O(N) — that is the deliberate price of *not* caching all secrets cluster-wide.
We quantify the per-tenant memory cost (MiB/tenant) so operators can size, and compare it to Baseline-PR3476's "O(1) informer but every secret in the cluster resident" — which is cheaper in connections but unbounded and insecure in data resident.

---

## 4. HMAC overhead micro-benchmarks (Phase 5)

Go benchmarks in `pkg/auth/hmac` (`go test -bench`):

- `BenchmarkDeriveServiceKeyNS` — HKDF per-namespace derivation cost (expected ~µs, one-time per key; cached after first use). Confirm it is not on the per-request hot path.
- `BenchmarkVerify_CandidateSet` — verify latency with a 1-key vs the worst-case 4-key candidate set (ns + nsOld + master + masterOld during migration). Each candidate is one `crypto/hmac.Equal`; assert the worst case adds < a few µs and is constant-time.
- `BenchmarkSign_NSScoped` vs `BenchmarkSign_MasterScoped` — confirm the storagesvc `\n<namespace>` canonical suffix adds negligible cost and master-scoped signing is unchanged.
- End-to-end: storagesvc archive GET/POST p50/p99 with ns-scoped verification on vs off — assert within noise of Baseline-Today (the archive path is dominated by I/O, not HMAC).

Gate: HMAC changes must not move storagesvc or fetcher-specialize latency outside the agreed budget (§8).

---

## 5. Cold-start latency (the RFC-0002 invariant)

The poolmgr cold-start path (synchronous `getServiceForFunction`, ~100ms budget) must stay **byte-identical** with the tenancy gates on or off — RFC-0002 already guarantees this and the tenancy work must not break it.

| Metric | Method | Gate |
|---|---|---|
| Poolmgr cold-start p50/p99 | Load test: first-invocation latency across many fresh functions, gates on vs off | p99 within the agreed budget of Baseline-Today (hard) |
| Warm-path (router-admitted) p99 | Steady traffic through the slice-fed index | No regression vs Baseline-Today |
| Specialization latency | executor→fetcher `/specialize` round trip (now ns-key-signed) | Within budget; the HMAC ns-key is precomputed, not per-request HKDF |

Reuse the existing load harness and the `cpuburn` fixture pattern (from RFC-0006 load tests) so latency is measured under realistic CPU pressure, not idle.

---

## 6. Tenant reconcile throughput

The tenant-lifecycle controller is new and on the onboarding critical path.

- `BenchmarkTenantReconcile` (fake client) — reconcile cost per tenant (RBAC + SA + secret derivation + status write).
- Burst test: onboard 50 namespaces simultaneously; measure time-to-all-Ready and controller CPU; assert the work-queue drains without starvation and no apiserver throttling cascade.
- Assert the `escalate`/`bind` RBAC writes are batched/idempotent (no hot-loop re-create).

---

## 7. Load-test scenarios (end-to-end)

Run on kind-ci (or a perf cluster) with the observability profile so pprof + Prometheus are captured:

1. **Steady-state + onboard** — N existing tenants under traffic; onboard one more; record the §2 headline metrics live (this is the #3298 reproduction-and-fix scenario).
2. **Scaling sweep** — bring up N tenants (§3); capture heap/goroutine/watch profiles at each N.
3. **Offboard/re-onboard churn** — repeatedly onboard/offboard a namespace; assert no goroutine/heap leak across cycles (the Tier-B teardown risk).
4. **Isolation under load** — two tenants at traffic; confirm per-namespace HMAC verification adds no measurable throughput loss and cross-tenant forge attempts are rejected without affecting the victim's latency.

---

## 8. Regression gates

**Hard gates (block merge):**
- Onboarding a namespace restarts **0** control-plane pods and causes **0** function 5xx on existing namespaces.
- Poolmgr cold-start p99 within the agreed budget of Baseline-Today (the RFC-0002 byte-identical-path invariant holds).
- `fission_router_route_resync_drift_total` stays **0** (existing CI bar — the dynamic watch must not introduce route drift).
- No goroutine leak across onboard/offboard cycles (goroutine count returns to baseline ± a small constant).

**Soft gates (review, justify, document):**
- Executor/router heap growth per tenant ≤ the agreed MiB/tenant budget; any overage is explained against the Tier-B necessity.
- API server watch-connection count documented at each N; the O(N) Tier-B term is expected and acceptable.
- HMAC verify worst-case (4-candidate) latency increase < a few µs and off the hot path.

---

## 9. Tooling

- **Go benchmarks** — `go test -bench=. -benchmem ./pkg/auth/hmac/...` and the tenant-controller package; track with `benchstat` against the baseline commit.
- **CI pprof capture** — the kind-ci observability/opentelemetry skaffold profiles already emit heap/goroutine profiles; pull them via the `debug-github-ci` skill's pprof-analysis path (leak-vs-baseline classification, before/after deltas). Tenant-controller and executor are the profile targets.
- **Prometheus metrics** — add tenancy metrics: `fission_tenant_reconcile_duration_seconds`, `fission_tenant_ready_total`, `fission_tenant_watch_caches{tier}` (count of active Tier-B caches), `fission_internal_auth_failures_total{service,reason}` (the auth-failure counter the internal-auth design doc flagged as a future add — now justified). These drive the scaling and reconcile dashboards.
- **kube-state / apiserver metrics** — watch-connection and longrunning-request counts for the API-server-load dimension.

---

## 10. Comparison summary (target end-state)

| Axis | Baseline-Today | Baseline-PR3476 (watch-all) | This design |
|---|---|---|---|
| Add-namespace restart | all control-plane pods | 0 | **0** |
| RBAC blast radius | per-namespace Roles | **cluster-wide** (all secrets) | per-namespace Roles (cluster-wide only on opt-in) |
| Secrets resident in control-plane memory | per-watched-ns | **every secret in cluster** | only onboarded-tenant secrets (Tier-B) |
| Internal auth default | on | **off** | **on** |
| Cross-tenant impersonation | master copied everywhere → trivial | master copied everywhere → trivial | **cryptographically prevented** (per-ns keys) |
| API server watch connections | O(N per type) | O(1) but cluster-wide | O(1) Tier-A + O(N) Tier-B (secrets/cms only) |
| Cold-start p99 | baseline | baseline | **== baseline** (RFC-0002 invariant) |

The intent is visible at a glance: equal-or-better on every performance axis, strictly better on every security axis.

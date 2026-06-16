# Multi-Namespace Tenancy

Working folder for the multi-namespace tenancy effort.
These are living planning + implementation-tracking docs; they stay here until the PRD is fully implemented, then condense into permanent docs (CLAUDE.md / `docs/`).

**Problem in one line:** onboarding a Fission namespace today restarts the whole control plane (issue [#3298](https://github.com/fission/fission/issues/3298)), and the current multi-namespace model leaks the master internal-auth secret into every tenant namespace.

**Approach in one line:** declarative `FissionTenant` CRD + label, dynamic per-namespace watching (zero restart), and per-namespace derived HMAC keys (master never leaves the control plane) — least-privilege preserved throughout.

---

## Documents

| Doc | What it covers |
|---|---|
| [prd.md](./prd.md) | The PRD: context, goals, three-pillar design, phased delivery, critical files, and concrete recommendations for PR [#3476](https://github.com/fission/fission/pull/3476). |
| [testing-plan.md](./testing-plan.md) | Per-phase unit + integration test matrix, framework changes, CI wiring, coverage targets. |
| [performance-benchmarking.md](./performance-benchmarking.md) | Metrics, scaling sweeps, baselines (today vs PR #3476), and the hard/soft regression gates. |
| [backward-compatibility.md](./backward-compatibility.md) | Upgrade/rolling-restart compatibility review, issues by severity with mitigations, and the safe-upgrade runbook. |

Add implementation notes, decision logs, and sub-task trackers to this folder as work proceeds.

---

## Decisions locked in

| Fork | Decision |
|---|---|
| v1 scope | Full vision, phased — disruption fix ships first |
| Onboarding API | FissionTenant CRD **and** `fission.io/enabled=true` label, both |
| HMAC secret isolation | Per-namespace derived keys — master never in a tenant namespace |
| Cross-namespace invocation | Admission + NetworkPolicy — no runtime podIP guard |
| Dynamic-watch RBAC (review) | Only Fission CRDs go cluster-wide; all core/workload resources per-namespace dynamic |
| HMAC migration (review) | Version-aware signing — control plane signs each pod with the key it expects; no upgrade 401s |

---

## Phase status

| Phase | Summary | Status |
|---|---|---|
| 0 | Thread-safe `NamespaceResolver` (setter + change feed) | Not started |
| 1 | FissionTenant CRD + `--tenantController` + CLI | Not started |
| 2 | Helm migration Job — **fixes #3298 (zero restart)** | Not started |
| 3 | Tier-A cluster-wide cache + membership predicate | Not started |
| 4 | Tier-B dynamic per-namespace cache + RBAC provisioning | Not started |
| 5 | Per-namespace derived HMAC keys | Not started |
| 6 | Opt-in `tenancy.mode: cluster` (watch-all) | Not started |
| 7 | Archive ns-prefix (storagesvc content isolation) — follow-up | Not started |

Phases 0–2 close the filed issue with zero security regression.
Phases 3–5 deliver true multi-tenant isolation.

---

## Glossary

- **Resource namespace** — where Function/Package/Environment/Trigger CRs live (watched by controllers).
- **Function/Builder namespace** — where function pods / builder pods run for a tenant (per-tenant mapping; defaults to the resource namespace).
- **Tier-A watch** — one cluster-wide, label/predicate-filtered cache for Fission CRDs + Fission-labeled workloads (low sensitivity, free dynamic discovery).
- **Tier-B watch** — per-namespace dynamic `cluster.Cluster` for Secrets/ConfigMaps (tenant data — never cluster-wide).
- **Master vs derived key** — the master HMAC secret stays in the control-plane release namespace; tenants receive only `HKDF(master, service:namespace)` derived keys.

# Multi-Namespace Tenancy

Working folder for the multi-namespace tenancy effort.
These are living planning + implementation-tracking docs; they stay here until the PRD is fully implemented, then condense into permanent docs (CLAUDE.md / `docs/`).

**Problem in one line:** onboarding a Fission namespace today restarts the whole control plane (issue [#3298](https://github.com/fission/fission/issues/3298)), and the current multi-namespace model leaks the master internal-auth secret into every tenant namespace.

**Approach in one line:** declarative `FissionTenant` CRD + label, dynamic per-namespace watching (zero restart), and per-namespace derived HMAC keys (master never leaves the control plane) — least-privilege preserved throughout.

**Status:** functionally complete.
A single chart value, `tenancy.mode: static | dynamic | cluster`, selects the posture; the critical-path implementation, the full per-namespace key security story, and all three modes have shipped (see [Tenancy modes](#tenancy-modes) and [Phase status](#phase-status)).

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

## Tenancy modes

Set `tenancy.mode` in the Helm values (default `static`).
It replaced the older `tenancy.dynamicNamespaces` + `tenantController.enabled` booleans (removed in #3502).

| Mode | Where Fission runs | Onboarding | Control-plane reads | Use when |
|---|---|---|---|---|
| `static` (default) | the env-seeded set (`defaultNamespace` + `additionalFissionNamespaces`) | install-time only | per-namespace Roles, scoped caches | single namespace, or a fixed known set; behaves exactly like pre-tenancy Fission |
| `dynamic` | any namespace onboarded at runtime | `fission tenant enable <ns>` **or** the `fission.io/enabled=true` label — **no control-plane restart** | per-namespace Roles + per-namespace derived HMAC keys; **tenant Secrets/ConfigMaps never in a cluster-wide cache** | untrusted multi-tenant clusters (the recommended isolating posture) |
| `cluster` | **any** namespace, automatically | the controller auto-onboards every namespace (no CR/label needed) | executor/buildermgr read Secrets/ConfigMaps and manage workloads **cluster-wide** | single-tenant / trusted clusters that value simplicity over isolation |

**Least-privilege holds in every mode:** function pods (fetcher/builder) always get a narrow per-namespace RoleBinding and per-namespace derived HMAC key — even in `cluster` mode the controller provisions those per namespace; only the control plane goes cluster-wide. ⚠️ **`cluster` mode trade-off:** a compromised executor/buildermgr can read any namespace's Secrets — use it only on trusted clusters.

**Opting a namespace out of `cluster` mode:** label it `fission.io/enabled=false`.
The controller skips it (and offboards it, tearing down its Fission RBAC/keys, if it was already auto-onboarded).
Removing the label re-onboards it.
The `fission.io/enabled` label is thus a symmetric override: `true` opts in (dynamic mode), `false` opts out (cluster mode), absent = the mode default.

---

## Phase status

All phases shipped as separate PRs off `main` (not the original `feat/multi-namespace-tenancy` branch).

| Phase | Summary | Status |
|---|---|---|
| 0 | Thread-safe `NamespaceResolver` (setter + change feed) | ✅ Shipped (#3497) |
| 1 | FissionTenant CRD + `--tenantController` + CLI | ✅ Shipped (#3497) |
| 2 | Helm migration Job — **fixes #3298 (zero restart)** | ✅ Shipped (#3497) |
| 3 | Tier-A cluster-wide cache + membership predicate + cross-process resolver-sync | ✅ Shipped (#3497) |
| 4 | Executor Tier-A cache + per-namespace fetcher/builder/workload RBAC provisioning | ✅ Shipped (#3497) |
| 5 | Per-namespace derived HMAC keys (fetcher, storagesvc, builder) + master-drop | ✅ Shipped (#3497) |
| 7 | Archive ns-prefix (storagesvc archive-content isolation) | ✅ Shipped (#3500) |
| — | RBAC unification — single-source fetcher/builder rules across Go ↔ Helm + admission policy | ✅ Shipped (#3501) |
| 6 | Opt-in `tenancy.mode: cluster` (watch-all) + config converged to the `tenancy.mode` enum | ✅ Shipped (#3502) |
| 4b | Tier-B dynamic per-namespace Secret/ConfigMap caches (RFC-0004 recycle in runtime-onboarded namespaces, **dynamic mode only**) | ⏳ Remaining — see [implementation-status.md](./implementation-status.md) |

Phases 0–2 close the filed issue with zero security regression; phases 3–5 deliver true multi-tenant isolation; phases 6–7 add the trusted-cluster opt-in and archive-content isolation.
Remaining items (the dynamic-mode Tier-B recycle refinement + optional hardening) are tracked in [implementation-status.md](./implementation-status.md); none block any mode from working.

---

## Glossary

- **Resource namespace** — where Function/Package/Environment/Trigger CRs live (watched by controllers).
- **Function/Builder namespace** — where function pods / builder pods run for a tenant (per-tenant mapping; defaults to the resource namespace).
- **Tier-A watch** — one cluster-wide, label/predicate-filtered cache for Fission CRDs + Fission-labeled workloads (low sensitivity, free dynamic discovery).
- **Tier-B watch** — per-namespace dynamic `cluster.Cluster` for Secrets/ConfigMaps (tenant data — never cluster-wide).
- **Master vs derived key** — the master HMAC secret stays in the control-plane release namespace; tenants receive only `HKDF(master, service:namespace)` derived keys.

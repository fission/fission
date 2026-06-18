# Backward-Compatibility Review — Multi-Namespace Tenancy

**Companion to:** [prd.md](./prd.md) · [testing-plan.md](./testing-plan.md) · [performance-benchmarking.md](./performance-benchmarking.md)

This document reviews the design for backward-compatibility risk against existing Fission installs and the rolling-upgrade contract, and records the two design decisions the review surfaced.

---

## Verdict

The approach is backward-compatible **for the phases that fix #3298 (Phases 0–2)** — they are purely additive (new CRD + controller + migration Job), touch no auth or RBAC scope, and keep env vars authoritative.
The real risk concentrates in **Phase 3 (dynamic-watch RBAC)** and **Phase 5 (HMAC key isolation)**.
None of the issues are blockers; each has a concrete mitigation, and two were genuine design forks now decided (below).

---

## Decisions from this review

| Fork | Decision | Effect |
|---|---|---|
| Dynamic-watch RBAC strictness | **Only Fission CRDs cluster-wide** | The only new ClusterRole is on `fission.io` types (lowest sensitivity — only Fission defines them). All core/workload resources (pods, services, deployments, endpointslices, secrets, configmaps, events) stay **per-namespace dynamic**. Closest to today's namespaced-only posture. |
| HMAC migration mechanism | **Version-aware signing** | Executor/buildermgr sign `/specialize` and `/build` with the key the *target pod* expects (master-derived for pre-upgrade pods, ns-derived for post-upgrade pods), keyed on a pod version annotation. Old pods age out; no operator flag day; no 401 storm. |

The first decision **revises the Tier-A/Tier-B split** in PRD §4.1: Tier A is now **Fission CRDs only**; pods/services/deployments/endpointslices move into the per-namespace dynamic tier alongside secrets/configmaps.
This is stricter (no cluster-wide workload read) at the cost of more per-namespace informers — see [performance-benchmarking.md](./performance-benchmarking.md) §3 for the revised scaling cost.

---

## What is already safe (no action needed)

- **`internalAuth.enabled=false` installs** — empty master ⇒ pass-through everywhere; the namespace-scoped constructors preserve the empty→passthrough short-circuit. Unaffected.
- **Deprecated `FISSION_FUNCTION_NAMESPACE` / `FISSION_BUILDER_NAMESPACE`** — these only ever remap the `default` namespace (`namespace.go:138-158`). The migration maps them onto `FissionTenant(default).spec.functionNamespace/builderNamespace`. Behavior preserved exactly.
- **Existing CRDs and the generated clientset** — adding a cluster-scoped CRD is additive; `genclient:nonNamespaced` generates a new client surface without touching existing typed clients.
- **FissionTenant needs no admission webhook** — immutability (`self == oldSelf`) and the DNS-1123 pattern are CEL-expressible (no cross-namespace check involved), so there is no new `failurePolicy=fail` dependency on the webhook pod.
- **Phases 0–2** — `NamespaceResolver` becomes thread-safe with no behavior change; the CRD/controller/CLI are additive; the migration Job seeds CRs without mutating any pod-template env, so nothing restarts and env remains authoritative.

---

## Issues, by severity

### 🔴 HIGH — Rolling-upgrade HMAC straddle (new signer vs old verifier)

The original §5.5 covered *new verifier accepts old signer* (dual-accept) but missed the reverse.
After upgrade, the new executor **adopts existing pool pods in place** (AdoptExistingResources — same pod UID, the old fetcher sidecar survives).
It then calls `/specialize` on those **old fetchers**, which only know the master-derived key.
If the new executor signs with a namespace-scoped key, the old fetcher rejects it with **401 and specialization fails**.
Fetcher sidecars are not rolled by Helm — they cycle only when the pod is recreated — so this tail is long.

**Mitigation (decided): version-aware signing.**
The executor/buildermgr sign with the key the **target pod** expects, branching on a pod version annotation (pre-upgrade pods → master-derived `K(fetcher)`; post-upgrade pods → `K(fetcher, ns)`).
Old pods age out naturally.
No operator action, no flag day, no 401s.

### 🔴 HIGH — The master must remain in tenant namespaces during the migration window

The security goal is "master out of tenant namespaces," but old fetcher pods read `data.secret` (the master), and the env mount is `Optional: true` (`config.go:64`) — a **missing key fails open to no-auth**, not closed.
If the controller removes `data.secret` from tenant namespaces too early, every surviving old pod silently runs **unauthenticated**.

**Mitigation.**
During the migration window the tenant Secret carries **both** `data.secret` (master, for old pods) **and** the derived keys (`fetcherKey`/`builderKey`/`storageKey`, for new pods).
The master is removed from tenant namespaces only **after all function/builder pods have cycled** to new images.

**Honest consequence:** the isolation benefit (master absent from tenant namespaces) is fully realized only after a pod cycle, not at the upgrade instant.
During the window the posture equals today's (master present in tenant namespaces).
This is no worse than the status quo and must be stated plainly in operator docs.

### 🟠 MED — Phase 3 introduces one ClusterRole even in the default mode

A dynamic cluster-wide watch needs a ClusterRole, and **RBAC cannot filter by label** — a cluster-wide list/watch grant on pods/services lets the SA read *all* of them, even if the cache only retains Fission-labeled objects.
The earlier "no ClusterRoles in the default mode" framing was therefore overstated.

**Mitigation (decided): only Fission CRDs go cluster-wide.**
The single new ClusterRole grants get/list/watch on `fission.io` types only — the lowest-sensitivity grant possible, since only Fission defines those types and their specs carry no third-party secret values.
Every core/workload type stays per-namespace dynamic, so there is **no** cluster-wide read of pods/services/secrets/configmaps.
Operators auditing RBAC will see exactly one new ClusterRole, scoped to Fission's own CRDs.
(Far tighter than PR #3476's cluster-wide *secrets* read.)

### 🟠 MED — Dynamic-provisioning timing race (fail-open)

The static chart guaranteed the auth Secret existed before any pod was scheduled.
The dynamic controller does not, and the optional env keys fail open.
A pod created in a freshly-onboarded namespace **before** the controller writes the derived-key Secret would admit with no auth.

**Mitigation.**
Gate function/builder pod creation in a namespace on the tenant's `AuthKeyProvisioned` (or `Ready`) condition, so the executor does not specialize into a namespace whose keys aren't provisioned yet.
Add a metric/alert on any pass-through (no-auth) pod so a provisioning gap is visible rather than silent.

### 🟠 MED — The env→FissionTenant source-flip can change the watched set

Until the Phase-5 source-flip, env vars are authoritative; afterward, the FissionTenant CRs are.
The migration Job seeds CRs to match env, but if they diverge before the flip (a CR-only tenant, or an edited env var), the flip could silently **drop** a namespace (functions stop) or **add** one (sudden watch).

**Mitigation.**
At flip time, validate that the CR-derived set is a superset of the env-derived set; warn on any divergence; never silently drop a namespace.
Treat the flip as an explicit, logged transition with a drift check.

### 🟡 LOW — Assorted

- **CLI version skew** — new `fission tenant …` against an old cluster (no FissionTenant CRD) must error gracefully ("multi-namespace tenancy not installed; upgrade the chart"), not panic on the missing resource. Old CLI against a new cluster simply lacks the subcommand (fine).
- **storagesvc canonical dual-form** — during the window, storagesvc must accept **both** the old 4-field canonical (master-signed, from old fetchers) **and** the new 5-field namespace-bound canonical (ns-signed, from new fetchers). The transition must dual-accept the canonical *form*, not just the key. Drop the 4-field path after all fetchers cycle.
- **Upgrade ordering** — the new cluster-scoped CRD and the CRD ClusterRole must install before the controller starts and before the migration Job runs (Helm hook weights / resource-kind ordering). Confirm `cmd/preupgradechecks` does not trip on the new CRD or RBAC.
- **`tenancy.mode: cluster` opt-in** — operators choosing the trusted-cluster mode get PR #3476-style cluster-wide RBAC by their own explicit choice; document the trade-off at the value.

---

## Per-phase backward-compatibility summary

| Phase | Backward-compat risk | Notes |
|---|---|---|
| 0 — thread-safe resolver | **None** | No behavior change; `-race` covered. |
| 1 — CRD + controller + CLI | **None** | Additive; CEL handles validation (no webhook dependency). |
| 2 — migration Job (#3298 fix) | **None** | Seeds CRs without touching pod-template env → zero restart; env stays authoritative. |
| 3 — dynamic watch | **Low** | One new low-sensitivity ClusterRole (Fission CRDs only); confirm install ordering; graceful degrade if RBAC lags. |
| 4 — Tier-B dynamic + RBAC provisioning | **Low–Med** | Provisioning-timing race (gate on `AuthKeyProvisioned`); offboard teardown correctness. |
| 5 — per-ns HMAC keys | **Med–High** | The delicate phase: version-aware signing + master-stays-during-window + dual-form storagesvc. Correctly sequenced last. |
| 6 — `tenancy.mode: cluster` | **None** | Opt-in only. |
| 7 — archive ns-prefix | **Med** | Storage-layout + Package-URL migration; back-compat for bare-UUID archives required. Follow-up. |

---

## Suggested safe-upgrade runbook (Phase 5)

1. `helm upgrade` rolls the control plane (new verifiers dual-accept; new signers are version-aware).
2. The tenant controller writes each tenant Secret with **both** the master and the derived keys.
3. New function/builder pods are created with the new fetcher image and consume the derived keys; old pods keep using the master via version-aware signing.
4. Operators drain/cycle long-lived pool pods at their own pace (or let normal churn do it); a metric reports how many pre-upgrade pods remain.
5. Once zero pre-upgrade pods remain, a second `helm upgrade` (or the controller, gated on the metric) drops `data.secret` from tenant Secrets — the master is now absent from every tenant namespace and full isolation is realized.

The key property: at no point is a running function un-served or forced to restart by the auth change; the security benefit lands incrementally as pods cycle.

# Implementation status

This tracks the PRD (`prd.md`) against what has shipped.
Everything is gated behind `tenancy.mode` (default `static`), so a default install is byte-identical to before tenancy existed.

The effort shipped as a sequence of separate PRs off `main` (not the original `feat/multi-namespace-tenancy` branch): the dynamic-tenancy critical path (#3497), archive-content isolation (#3500), the Go↔Helm RBAC unification (#3501), and the `tenancy.mode=cluster` opt-in + config convergence (#3502).

## Shipped

The critical-path implementation and the full security story are complete, across all three modes (`static` / `dynamic` / `cluster` — see [README.md](./README.md#tenancy-modes)).
A namespace onboarded at runtime in `dynamic` mode — via `fission tenant enable <ns>` or the `fission.io/enabled=true` label — is served end-to-end with **no control-plane restart**: the router routes it, the executor pools and specializes in it, the buildermgr builds in it, and it is isolated by per-namespace HMAC keys.
In `cluster` mode the controller auto-onboards every namespace (opt out with `fission.io/enabled=false`), keeping fetcher/builder least-privilege per namespace while the control plane reads cluster-wide.

| PRD phase | What shipped | PR |
|---|---|---|
| 0 — thread-safe resolver | `NamespaceResolver` behind a `RWMutex` + change feed + `IsTenant` | #3497 |
| 1 — onboarding surface | `FissionTenant` CRD (cluster-scoped) + `--tenantController` + `fission tenant` CLI | #3497 |
| 2 — #3298 fix | one-shot seed Job migrates `additionalFissionNamespaces` → CRs on upgrade, env unchanged | #3497 |
| 3 — dynamic discovery | membership predicate + cluster-wide CRD cache + tenant-scoped reconcilers + cross-process resolver-sync (router, buildermgr, the four trigger managers, executor) | #3497 |
| 4 — least-privilege provisioning | controller provisions per-namespace fetcher/builder RBAC; executor Tier-A cluster-wide cache (Secrets/ConfigMaps/ReplicaSets stay namespace-scoped); executor/buildermgr workload RBAC in tenant namespaces | #3497 |
| 5 — per-namespace HMAC keys | namespace-scoped derivation, version-aware signing on executor + buildermgr, controller-owned derived-key Secret, the master-drop (master never in a tenant namespace), and the builder `/build` channel | #3497 |
| 7 — archive ns-prefix | `_tenant_/<ns>/<uuid>` storagesvc archive IDs + verifier-reports-principal authz (path-traversal-guarded) for true archive-content isolation | #3500 |
| — RBAC unification | fetcher/builder rules single-sourced across Go and Helm (`_tenant-workload-roles.tpl`); a `ValidatingAdmissionPolicy` pins which SAs the controller may bind | #3501 |
| 6 — `tenancy.mode=cluster` | opt-in trusted-cluster watch-all (cluster-wide Tier-B cache + control-plane ClusterRoleBindings); the controller auto-onboards every namespace with a `fission.io/enabled=false` opt-out; config converged from the `dynamicNamespaces`/`tenantController.enabled` booleans to the single `tenancy.mode` enum | #3502 |

All three namespace-scoped channels (fetcher, storagesvc, builder) derive and verify per-namespace keys; the master-derived control-plane channels (executor, router-internal) are unchanged.

## Test coverage

Comprehensively unit-tested: the security-critical invariant that the executor's Secret/ConfigMap cache is **never** cluster-wide in `dynamic` mode is test-locked (`TestExecutorCacheOptionsTierSplit`, which also pins the deliberate cluster-wide widening in `cluster` mode), as are the version-aware signing decisions, the resolver-sync, the per-namespace key provisioning, the workload RoleBindings, the `tenancy.mode` enum precedence, and the cluster-mode auto-onboard + opt-out reconciler.

Integration CI exercises **one posture per k8s leg** so every mode stays covered: `v1.32.11` = `dynamic` (the serial `TestDynamicTenantLifecycle` onboards a fresh namespace at runtime and asserts no restart), `v1.36.1` = `cluster` (the serial `TestClusterTenantAutoOnboard` runs a function in a fresh auto-onboarded namespace and asserts the fetcher's grant stays a per-namespace RoleBinding, never a ClusterRoleBinding), `v1.34.8` = `static`.

## Remaining (refinements + optional hardening — not on the critical path)

None of these block any mode from working; each warrants its own focused change.

- **Phase 4b — Tier-B dynamic caches (dynamic mode only).**
  The executor's Secret/ConfigMap watch is namespace-scoped to the env-seeded set, so in `dynamic` mode a function in a runtime-onboarded namespace works (the fetcher reads Secrets directly) but RFC-0004 pod-recycle-on-config-change does not yet follow a config change in that namespace.
  Closing it needs a dynamic per-namespace `cluster.Cluster` with reliable teardown — the PRD's highest-risk new code.
  (`cluster` mode does not have this gap; its cache is cluster-wide.)
- **Builder `/build` channel verification.**
  The builder *container* has no master env wired by buildermgr, so `/build` is pass-through today (buildermgr ns-signs, the builder does not verify).
  An additive hardening.
- **Per-namespace key rotation.**
  Derived keys are create-once; rotating a tenant's key currently means offboard → re-onboard.
  A proper SSA-reconcile rotation path is a follow-up.
- **`functionNamespace` / `builderNamespace` immutability.**
  `fission tenant enable` can overwrite a tenant's function/builder namespace with no orphan cleanup of the old one — flagged in review, needs a codegen-backed immutability marker.
- **Minor polish.**
  Storagesvc spec-apply uploads stay unscoped (dedup-by-checksum follow-up); `ParseTenancyMode`/warn-on-invalid-mode (deferred — the chart already fails the render on a bad value); a reserved-namespace denylist beyond the system ones; a chart-render CI assertion for the mode-gated objects.

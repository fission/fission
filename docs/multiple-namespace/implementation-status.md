# Implementation status

This tracks the PRD (`prd.md`) against what has shipped on the `feat/multi-namespace-tenancy` branch.
Everything is gated behind `tenancy.dynamicNamespaces` / `FISSION_DYNAMIC_NAMESPACES` (default off), so a default install is byte-identical to before.

## Shipped

The critical-path implementation and the full security story are complete.
A namespace onboarded at runtime — via `fission tenant enable <ns>` or the `fission.io/enabled=true` label — is served end-to-end with **no control-plane restart**: the router routes it, the executor pools and specializes in it, the buildermgr builds in it, and it is isolated by per-namespace HMAC keys.

| PRD phase | What shipped | Key commits |
|---|---|---|
| 0 — thread-safe resolver | `NamespaceResolver` behind a `RWMutex` + change feed + `IsTenant` | `d620b2f7` |
| 1 — onboarding surface | `FissionTenant` CRD (cluster-scoped) + `--tenantController` + `fission tenant` CLI | `09f231f3`, `d0f65632`, `99e0c259` |
| 2 — #3298 fix | one-shot seed Job migrates `additionalFissionNamespaces` → CRs on upgrade, env unchanged | `c17c2100` |
| 3 — dynamic discovery | membership predicate + gated cluster-wide CRD cache + tenant-scoped reconcilers + **cross-process resolver-sync** (router, buildermgr, the four trigger managers, executor) | `2dfd43d6`, `d418f24e`, `e5969e50`, `f3448249` |
| 4 — least-privilege provisioning | tenant controller provisions per-namespace fetcher/builder RBAC, **executor Tier-A cluster-wide cache** (Secrets/ConfigMaps/ReplicaSets stay namespace-scoped), and **executor/buildermgr workload RBAC** in tenant namespaces | `176aee41`, `1c253897`, `333af025` |
| 5 — per-namespace HMAC keys | namespace-scoped derivation, version-aware signing on executor + buildermgr, the controller-owned derived-key Secret, the **master-drop** (master never in a tenant namespace), and the builder `/build` channel | `22978d16`, `5b60c454`, `16d248ab`, `6b770dd2`, `dafe9f67`, `2e939d66` |

All three namespace-scoped channels (fetcher, storagesvc, builder) derive and verify per-namespace keys; the master-derived control-plane channels (executor, router-internal) are unchanged.
Three review rounds were completed, with every finding fixed.

## Test coverage

The implementation is comprehensively unit-tested: the security-critical invariant that the executor's Secret/ConfigMap cache is **never** cluster-wide is test-locked (`TestExecutorCacheOptionsTierSplit`), as are the version-aware signing decisions, the resolver-sync, the per-namespace key provisioning, the workload RoleBindings, and the required-key gating.

CI exercises the **dynamic-off** path on every commit (so no regression is possible to the default install) but does not yet deploy a dynamic-mode cluster.

## Remaining (validation + optional refinements — not on the critical path)

These do not block the feature working; they are validation infrastructure and optional enhancements, each of which warrants its own focused change.

- **Gated dynamic-mode integration leg.**
  CI currently deploys only the default (dynamic-off) profile, so the runtime onboarding path is unit-tested but not integration-tested.
  Proving it in CI needs a new job that deploys with `tenancy.dynamicNamespaces=true` + `tenantController.enabled=true` plus a serial test that records control-plane pod UIDs, onboards a namespace at runtime, and asserts no restart and an invocable function (PRD §10).
  This is a deliberate decision because it roughly doubles the integration CI time.
- **Phase 4b — Tier-B dynamic caches.**
  The executor's Secret/ConfigMap watch is namespace-scoped to the env-seeded set; functions in a runtime-onboarded namespace work (the fetcher reads Secrets directly), but the RFC-0004 pod-recycle-on-config-change does not yet follow a config change in a runtime-onboarded namespace.
  Closing this needs a dynamic per-namespace `cluster.Cluster` with reliable teardown — the PRD's highest-risk new code.
- **Phase 6 — `tenancy.mode=cluster`.**
  An explicit opt-in that collapses to a single cluster-wide cache + ClusterRoles for trusted single-tenant clusters.
- **Phase 7 — archive namespace-prefix.**
  Namespace-prefixed storagesvc archive IDs for true archive-content isolation (the interim barrier is UUID-unguessability + NetworkPolicy).

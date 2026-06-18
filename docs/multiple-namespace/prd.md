# PRD: Multi-Namespace Tenancy for Fission

**Status:** Draft for review
**Date:** 2026-06-16
**Tracking:** GitHub issue [#3298](https://github.com/fission/fission/issues/3298); compares against PR [#3476](https://github.com/fission/fission/pull/3476)
**Companion docs:** [testing-plan.md](./testing-plan.md) · [performance-benchmarking.md](./performance-benchmarking.md) · [backward-compatibility.md](./backward-compatibility.md)

---

## 1. Context

Fission already runs in multiple namespaces today, but the mechanism is brittle and the isolation is shallow.
An operator declares tenant namespaces in the Helm value `additionalFissionNamespaces`, which the chart renders into the env vars `FISSION_DEFAULT_NAMESPACE` + `FISSION_RESOURCE_NAMESPACES` on every control-plane Deployment.
`pkg/utils/namespace.go:46` reads those env vars **once, at process `init()`, into a package-global singleton**.

This single fact is the root of issue #3298.
Adding a namespace mutates an env var on every control-plane Deployment's pod template.
That changes the pod-template hash, which triggers a rolling restart of *every* control-plane Deployment (router, executor, buildermgr, triggers) plus a function re-specialization storm.
The reporter runs ~10 isolated namespaces and observes that onboarding one new namespace recreates pods cluster-wide, times out running functions, and forces the autoscaler to add nodes.
No amount of replica/PDB/RollingUpdate tuning fixes it, because the restart is structurally inevitable given env-var-driven, init-frozen configuration.

There is also a latent **security** problem that any honest multi-tenant story must fix.
Per `docs/internal-auth/00-design.md:203-217`, the chart copies the **same master** `fission-internal-auth` secret (identical `data.secret`) into every function namespace, and the `fission-fetcher` ServiceAccount there has `secrets: get`.
HMAC keys are derived per-*service*, not per-*namespace*.
So a single compromised tenant namespace yields the master secret, from which an attacker derives every service key and forges requests to storagesvc / fetcher / builder / executor / router-internal for *any* namespace.
In a multi-tenant install the HMAC scheme's tenant-isolation value is effectively zero.

The community PR #3476 solves the *operational* symptom (dynamic namespaces without restart) but does so by **watching all namespaces with cluster-wide RBAC** and proposes **disabling internal auth by default** (`internalAuth.enabled: false`) plus a **spoof-prone podIP→namespace cache** for invocation isolation.
Each of those is a regression on the axis the user ranked #1: security.

**Intended outcome:** a design where onboarding a namespace touches only that namespace (zero disruption), tenant isolation is *stronger* than today (least-privilege RBAC preserved, master secret never leaves the control plane), and the operator experience is declarative and cloud-native-obvious.

### Decisions locked in (do not relitigate)

| Fork | Decision |
|---|---|
| v1 scope | **Full vision, phased** — PRD documents all three pillars; ship the disruption fix first. |
| Onboarding API | **FissionTenant CRD *and* namespace label**, both. Label is sugar reconciled into the canonical CR. |
| HMAC secret isolation | **Per-namespace derived keys** — the master never lands in a tenant namespace. |
| Cross-namespace invocation | **Admission + NetworkPolicy** — no runtime podIP guard. |

Two further decisions came out of the backward-compatibility review ([backward-compatibility.md](./backward-compatibility.md)):

| Fork | Decision |
|---|---|
| Dynamic-watch RBAC strictness | **Only Fission CRDs go cluster-wide**; all core/workload resources stay per-namespace dynamic (revises the Tier split in §4.1). |
| HMAC migration mechanism | **Version-aware signing** — the control plane signs each pod with the key that pod expects, so rolling upgrades never 401 (see §5.5). |

---

## 2. Goals / Non-Goals

### Goals

- Onboarding/offboarding a tenant namespace causes **zero restart** of unrelated control-plane pods or running functions.
- Preserve **least-privilege per-namespace RBAC** (Roles, not ClusterRoles) as the default and recommended posture.
- **Strengthen** tenant isolation versus today: a fully compromised tenant namespace cannot forge requests as the control plane or as another tenant.
- Declarative, GitOps-friendly onboarding via a `FissionTenant` CRD and a `fission.io/enabled=true` namespace label, with a `fission tenant` CLI.
- Keep internal HMAC auth **on**; isolation is additive, never subtractive.
- Seamless upgrade: existing `additionalFissionNamespaces` installs migrate with no behavior change and no restart.

### Non-Goals

- A service mesh or mTLS (HMAC remains the lowest-common-denominator primitive; mesh is complementary, out of scope).
- Cross-namespace function invocation as a *feature* (it stays default-denied; a future explicit-grant CR is noted but not built).
- Per-tenant network/compute *quotas* baked into the CRD — operators use native `ResourceQuota` (the CLI can create one), not a re-expressed schema.
- Archive-content isolation in storagesvc (namespace-prefixed archive IDs) — flagged as a tracked follow-up, see §6.4.
- A hosted/SaaS control plane or per-tenant control-plane replicas.

---

## 3. Design Overview — three pillars

```
Pillar 1: Zero-disruption dynamic onboarding   (fixes #3298)
   FissionTenant CRD + label  ──▶  tenant-lifecycle controller  ──▶  live namespace set
   controllers watch dynamically (no env, no restart)

Pillar 2: True per-namespace isolation         (security #1)
   per-namespace derived HMAC keys (master stays in control plane)
   least-privilege per-namespace RBAC preserved (reject cluster-wide default)
   admission + NetworkPolicy for cross-ns (KubernetesWatchTrigger already hard-denied)

Pillar 3: Declarative cloud-native lifecycle   (UX)
   `fission tenant enable/list/disable/status`
   finalizer-drained offboarding, status conditions, label↔CR materialization
```

A single new privileged component — the **tenant-lifecycle controller** (`fission-bundle --tenantController`, its own ServiceAccount) — owns all tenant provisioning: per-namespace RBAC + ServiceAccounts + the per-namespace derived-key Secret, and publishes the live tenant set that every other manager consumes.
It is the *only* component that holds the privileged `escalate`/`bind` RBAC verbs and the *only* writer of derived keys, so the blast radius of that privilege is confined to one small, auditable process.

---

## 4. Pillar 1 — Zero-disruption dynamic onboarding

### 4.1 The watch-tier split

controller-runtime v0.24.1 (the version Fission ships) supports adding runnables and watches to a *running* manager — verified in the module cache: `runnableGroup.Add` accepts new runnables when started, `Controller.Watch` starts sources immediately post-start, and `cluster.Cluster` is itself a `Runnable` with its own `GetCache()`.
A reconciler watch can be bound to an arbitrary cache via `source.Kind(cache, obj, handler, predicates…)`.
This makes runtime per-namespace watching feasible without a process restart.

Resources are split into two tiers per manager:

Per the backward-compatibility review, the split is drawn at the strictest defensible line — **only Fission's own CRD types go cluster-wide**:

| Tier | Resource types | Watch model | Cluster-wide? | RBAC justification |
|---|---|---|---|---|
| **A** | Fission CRDs only (Function, Environment, Package, all Triggers, CanaryConfig) | One cluster-wide cache + tenant-membership predicate | **Yes** | Fission is the sole owner of these GVKs — a cluster-wide watch on a type only Fission defines leaks nothing a tenant didn't put in a Fission object. This is the single new ClusterRole, and it touches no core/workload type. It buys free dynamic discovery (a Function in a freshly-onboarded namespace appears with no watch reconfiguration). |
| **B** | Pods, Services, Deployments, EndpointSlices (Fission-labeled), Secrets, ConfigMaps, Events | Per-namespace dynamic `cluster.Cluster`, added/removed at runtime by the tenant controller | **No — never** | RBAC cannot filter by label, so a cluster-wide watch grant on pods/services/secrets means the SA *can* read all of them — a privilege expansion vs today's namespaced-only Roles. Keeping every core/workload type per-namespace preserves least privilege. RFC-0004's CM/Secret pod-recycle uses `.For(&Secret{})` (list+watch, not get), so the watch is unavoidable — security forces it per-namespace anyway. |

Tier A is built once at startup and never touched again on tenant change — new tenants' Functions simply start passing the membership predicate.
Tier B is provisioned and torn down per tenant at runtime, and it is exactly the sensitive-data + workload path we keep least-privilege.
This costs more per-namespace informers than putting workloads in Tier A (see [performance-benchmarking.md](./performance-benchmarking.md) §3) — a deliberate trade for zero cluster-wide read of any core resource.
The RFC-0002 router EndpointSlice watch moves from cluster-wide to per-namespace dynamic to match.

This is strictly better than PR #3476's "watch all + cluster-wide RBAC": tenant Secrets/ConfigMaps/workloads are **never** in a cluster-wide cache, and the only cluster-wide grant is read of Fission's own CRDs.

### 4.2 Making the namespace set dynamic

`pkg/utils/namespace.go` becomes thread-safe.
The mutable tenant set moves behind a `sync.RWMutex`; `FissionResourceNS()` and `FunctionNamespaces()` return copies under `RLock`; a `SetTenants` / `AddTenant` / `RemoveTenant` API (called only by the tenant controller's reconcile) mutates it and publishes a change notification.
The three scalar namespaces (`FunctionNamespace`/`BuilderNamespace`/`DefaultNamespace`) stay env-driven and immutable; only the tenant *set* is dynamic.

Each manager (router, executor, buildermgr, trigger controllers) runs a lightweight **Tier-A watch of `FissionTenant` + labeled `Namespace`** (cheap, low-sensitivity) that drives its own in-process resolver update and, for managers with Tier-B caches (executor), the add/remove of per-namespace `cluster.Cluster` instances.
Cross-process propagation is "every manager watches the tenant CR," not RPC coupling to the tenant controller.

Consumers that today snapshot the static set at startup and must re-read the live set (representative, not exhaustive):
`pkg/utils/crmanager/crmanager.go:58` (`FissionCacheOptions`, shared by 6 managers — the central change), `pkg/executor/start.go:73` (`executorCacheOptions`), `pkg/router/router.go:85` (`sliceWatchNamespaces`/`routerCacheOptions`/`checkSliceWatchRBAC`), `pkg/logger/logger.go:248`, `pkg/utils/informer.go:23`, plus the per-pass adoption/reaper loops in `pkg/executor/executortype/poolmgr/gpm.go` and `pkg/executor/reaper/`.
The router's data-plane lookups are already namespace-agnostic (`resolver_executor.go` keys on `fn.Namespace`; `endpointcache/index.go` keys on `FnKey{Namespace,Name}`), so only the *watch set* widens — no index rework.

### 4.3 The tenant-lifecycle controller

A new `--tenantController` subsystem in `cmd/fission-bundle/main.go`, its own Deployment, its own dedicated ServiceAccount.
Rationale: the multi-headed-binary pattern is idiomatic; an isolated failure domain and isolated RBAC identity are essential because this is the only component holding privileged RBAC verbs.
It is *not* folded into the webhook (admission hot path, most-exposed) or executor (busiest, restart-sensitive).

On each reconcile of a `FissionTenant` (or labeled `Namespace`) it:

1. Updates the shared live tenant set (drives every other manager via their tenant watch).
2. Provisions per-namespace RBAC: the `Role`+`RoleBinding` pairs currently in `charts/fission-all/templates/_function-access-role.tpl` (fetcher: `get` on configmaps/secrets/serviceaccounts/packages; builder: `get` on packages/configmaps/secrets; fetcher-websocket: events + pods) and the per-namespace component Roles.
3. Provisions the `fission-fetcher` / `fission-builder` ServiceAccounts.
4. Provisions the per-namespace **derived-key Secret** (Pillar 2).
5. Writes status conditions.

**RBAC confinement — the key least-privilege win.**
Kubernetes forbids an SA from creating a Role/RoleBinding granting permissions it does not itself hold, unless it has the `escalate` (Roles) / `bind` (RoleBindings) verbs.
We grant the tenant controller's SA `escalate`+`bind` **instead of** granting it cluster-wide secret read.
Result: the controller can *mint* a Role that grants `get secrets`, but it cannot itself `get secrets` — a compromise of the tenant controller cannot directly read tenant Secrets; it can only create RBAC objects, which is auditable and itself a detectable event.
This is strictly tighter for data confidentiality than the "controller holds the union of all permissions" alternative.

### 4.4 Fail-safe for un-onboarded namespaces

With Tier-A CRDs cached cluster-wide, a Function created in a non-tenant namespace appears in cache.
Three independent guards make this safe:

1. **Membership predicate** at the watch layer (composed with the existing `GenerationChangedPredicate`) — a non-tenant object never reaches a workqueue. Reads the live set, so onboarding mid-flight starts admitting; tenant-add enqueues a resync of existing objects so they converge in one pass.
2. **Reconcile-entry guard** — the reconcile body re-checks membership and no-ops, covering the offboard race.
3. **RBAC-absence floor (the real backstop)** — the executor/buildermgr have *no* RBAC in a non-tenant namespace, so any attempt to create a pod/Deployment/SA there is `Forbidden` by the apiserver. Security does not depend on the predicate being correct; it depends on RBAC absence, the strong guarantee.

### 4.5 Opt-in "trusted cluster" mode

A chart value `tenancy.mode: namespaced | cluster` (default `namespaced`).
`cluster` mode collapses Tier B to a single cluster-wide cache and renders ClusterRoles — i.e. PR #3476's model, demoted to an explicit opt-in for single-tenant/trusted clusters that value simplicity over isolation.
The dynamic-cluster machinery is simply not engaged in that mode.

---

## 5. Pillar 2 — Per-namespace HMAC key isolation

### 5.1 The derivation change

`pkg/auth/hmac/keys.go` gains a namespace-scoped derivation alongside the existing one:

```
derived_key     = HKDF(master, info = KeyVersion + ":" + service)                    // existing, unchanged
derived_ns_key  = HKDF(master, info = KeyVersion + ":" + service + ":" + namespace)  // NEW
```

Appending `namespace` *after* `service` preserves every existing master-scoped key byte-for-byte, so master-scoped channels are untouched (critical for backward compat).
New constructors: `DeriveServiceKeyNS`, `ServiceSignerNS`, `ServiceVerifierNS`, plus `VerifierFromKey` / `SignerFromKey` that use already-derived bytes directly (for pods that hold *only* a derived key, never the master).

### 5.2 Only three channels become namespace-scoped

The discriminator is simple: *does a copy of the signing key's source material land in a pod scheduled into a tenant namespace?*
The fetcher sidecar (every function + builder pod) and the builder container run in tenant namespaces.
Everything else runs in the control-plane release namespace.

| Channel | Direction | Signer key | Verifier key | Namespace source | Scope |
|---|---|---|---|---|---|
| `fetcher` (`/fetch`,`/specialize`) | executor/buildermgr → fetcher | control plane derives `K(fetcher, targetNS)` from master | fetcher (tenant ns) holds only `K(fetcher, ownNS)`; ns from downward-API | both sides compute independently; never on wire | **NS-scoped** |
| `builder` (`/build`) | buildermgr → builder | control plane derives `K(builder, builderNS)` | builder (tenant ns) holds only `K(builder, ownNS)` | own pod ns (downward-API) | **NS-scoped** |
| `storagesvc` (`/v1/archive`) | fetcher → storagesvc (+ pruner/CLI) | fetcher signs with `K(storagesvc, ownNS)` | storagesvc (release ns, holds master) derives `K(storagesvc, claimedNS)` | signed `X-Fission-Auth-Namespace` header, folded into canonical string | **NS-scoped** |
| `executor` (`/v2/*`) | router/triggers → executor | master | master | n/a (both control-plane) | **master, unchanged** |
| `router-internal` (`/fission-function/<ns>/<name>`) | executor/triggers → router | master | master | n/a (both control-plane) | **master, unchanged** |

The asymmetry is what makes this clean: `executor` and `router-internal` need **zero changes**.
The `/fission-function/<ns>/<name>` URL namespace is a red herring for HMAC — no tenant pod holds those keys, so there is nothing to scope, and scoping them would needlessly break the multi-target publishers and the unsigned KEDA-connector caveat.

### 5.3 Verifier namespace resolution (unspoofable)

- **fetcher / builder**: verify with a key derived from their *own* pod namespace (kubelet-provided downward API, `fetcher.go:106` already reads it). An attacker reaching the port from another namespace would need a signature under `K(fetcher, thisNS)`, which only the master-holding control plane can produce — and the control plane only signs for the namespace it is legitimately specializing.
- **storagesvc**: the caller sets `X-Fission-Auth-Namespace: <ownNS>`; the verifier derives `K(storagesvc, claimedNS)`; the namespace is **part of the canonical signing string** (`Canonical()` appends `\n<namespace>` *only* for this channel), so a tampered header invalidates the signature. A tenant can truthfully claim only its own namespace, yielding only its own key.

### 5.4 Secret provisioning

The tenant-lifecycle controller (holds the master in the release namespace) writes a `fission-internal-auth` Secret into each tenant namespace containing **only the derived keys** — `fetcherKey`, `builderKey`, `storageKey` (+ `*KeyOld` during rotation) — never the master.
Helm cannot do this (no HKDF/HMAC primitive in Sprig), which is *why* a Go provisioner is required and folded into the tenant controller rather than the chart.

Pod consumption is drop-in: the fetcher env var keeps the name `FISSION_INTERNAL_AUTH_SECRET` but now sources `data.fetcherKey` instead of `data.secret` (`pkg/fetcher/config/config.go:56`).
The value is now a derived key, so the fetcher constructs its verifier with `VerifierFromKey(fetcherKey, …)` (no HKDF — it doesn't have the master).
Control-plane pods keep mounting `data.secret` = master and keep using the deriving constructors; for their outbound ns-scoped calls they call `ServiceSignerNS(master, svc, targetNS, …)` (free — they have the master).

### 5.5 Migration without a flag day

Empty-master pass-through is preserved (`internalAuth.enabled=false` behaves identically).
The danger window is mid-upgrade, when some tenant pods still mount the old master-scoped key while storagesvc begins expecting ns-scoped signatures.
Solution: the three ns-scoped verifiers **accept a bounded candidate-key set** during transition — both the ns-derived key *and* the legacy master-derived key.
This reuses the existing `OldSecret` rotation-overlap idea, generalized to "old key = previous *scoping scheme*'s key."
No `KeyVersion` bump is needed (info strings don't collide).
After all pods roll, a second upgrade drops the master-derived candidate.
This dual-accept is **mandatory**, not optional — without it the upgrade is a 401 storm.

Dual-accept handles *new verifier / old signer*.
The **reverse straddle** — new control plane signing while an **old fetcher pod** (adopted in place across the executor upgrade, old sidecar intact) verifies only the master key — is handled by **version-aware signing**: the executor/buildermgr sign `/specialize` and `/build` with the key the *target pod* expects (master-derived for pre-upgrade pods, ns-derived for post-upgrade pods), branching on a pod version annotation.
Old pods age out; no operator flag day; no 401s.

Crucially, the **master stays in tenant namespaces during the window**: the tenant Secret carries *both* `data.secret` (master, for old pods — whose env mount is `Optional:true` and fails *open*) and the derived keys (for new pods).
The master is removed from tenant namespaces only **after every function/builder pod has cycled**, so the full isolation benefit lands incrementally as pods turn over, never by forcing a restart.
See [backward-compatibility.md](./backward-compatibility.md) for the safe-upgrade runbook.

Master rotation still works: every ns-key is `HKDF(master, …)`, so rotating the master rotates all ns-keys atomically.

### 5.6 What this buys (attack-walk)

Assume tenant-A is fully compromised (RCE in a function pod) and reads its own `FISSION_INTERNAL_AUTH_SECRET` = `K(fetcher, A)` / `K(storagesvc, A)`, and NetworkPolicy is misconfigured.

- **Forge `/specialize` to tenant-B's fetcher** → needs `K(fetcher, B)`; A holds only `K(fetcher, A)` → **401, airtight.** This is the highest-value win: A cannot make B's pod fetch/run arbitrary code.
- **Forge to executor or router-internal as anyone** → those stay master-scoped, and the master never lands in a tenant namespace → A cannot sign them at all → **401.** Under today's scheme A would hold the master and forge all five channels; here A is reduced to "sign fetcher/builder/storagesvc for its *own* namespace only."
- **Reach the master / read another tenant's key** → A's `fission-fetcher` SA has `secrets: get` only in namespace A → **no RBAC path** to the release-ns master or to `ns-B/fission-internal-auth`.

**Honest residual hole:** per-ns storagesvc keys stop control-plane impersonation, but because archive IDs are bare UUIDs not namespace-partitioned, a tenant that *learns another tenant's archive UUID* can still download it (UUID-unguessability + NetworkPolicy remain that barrier).
True archive-content isolation requires namespace-prefixed archive IDs — a tracked follow-up (§6.4), not v1.

---

## 6. Pillar 3 — Declarative lifecycle & UX

### 6.1 FissionTenant CRD (Fission's first cluster-scoped CRD)

Cluster-scoped, `spec.namespace` as the join key.
A tenant *is* a namespace-level grant; its natural peer is the cluster-scoped `Namespace`, and the controller needs a single cluster-wide `List()` to compute the resource-namespace set.

```go
// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope="Cluster",shortName={ftenant}
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.namespace`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type FissionTenant struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata"`
    Spec              FissionTenantSpec   `json:"spec"`
    Status            FissionTenantStatus `json:"status,omitempty"`
}

type FissionTenantSpec struct {
    // Namespace this tenant onboards. Immutable join key (XValidation self==oldSelf).
    Namespace string `json:"namespace"`
    // Per-tenant function-pod namespace (generalizes the global FISSION_FUNCTION_NAMESPACE). Empty = spec.namespace.
    FunctionNamespace string `json:"functionNamespace,omitempty"`
    // Per-tenant builder-pod namespace. Empty = spec.namespace.
    BuilderNamespace string `json:"builderNamespace,omitempty"`
}

type FissionTenantStatus struct {
    ObservedGeneration int64              `json:"observedGeneration,omitempty"`
    Conditions         []metav1.Condition `json:"conditions,omitempty"`
}
```

Conditions: `RBACProvisioned`, `ServiceAccountsReady`, `Ready` written by the tenant controller; `AuthKeyProvisioned`, `WatchActive` constants reserved and written by the Pillar-2 / Pillar-1 paths into the same slice.

**YAGNI cuts (deliberate):** no `ResourceQuota`/`LimitRange` in spec (use native `ResourceQuota`, optionally owned by the tenant), no `defaultEnvironment` (it's just a follow-up `fission env create`), no `authKeyRef` in spec (status-only — don't couple two independently-versioned subsystems).

Codegen path (note the 10-step checklist in `types.go` is partially stale — `configureClient`/`EnsureFissionCRDs` no longer exist): add the type + markers → `make codegen` → `make generate-crds` → `make generate-webhooks` if markers change.

### 6.2 Label ↔ CRD: one-way ignition

CRD is canonical; the label `fission.io/enabled=true` is a one-way trigger that the controller *materializes* into a CR — never the reverse.

| Situation | Behavior |
|---|---|
| Label added, no CR | Controller creates a FissionTenant `name=<ns>`, ownerRef → Namespace, annotation `managed-by=label` |
| Label removed, CR is `managed-by=label` | **CR not auto-deleted** (too destructive); set `Ready=False` reason `OnboardingLabelRemoved`, warn event. Deleting the *Namespace* still GCs the CR via ownerRef |
| CR by `fission tenant enable` (`managed-by=user`), ns unlabeled | Fully honored — user/GitOps CRs are independent of the label |
| CR exists, namespace doesn't | `Ready=False` reason `NamespaceNotFound`, requeue (order-independent) |

Simplest coherent model: one direction of automatic creation, zero automatic deletion, ownerRef for the only safe auto-GC path.

### 6.3 CLI

New group `pkg/fission-cli/cmd/tenant/`, registered as "Administration Commands."
Because FissionTenant is cluster-scoped, the subcommands take the namespace as a **positional arg** and bypass the global `--namespace`/`--all-namespaces` resolution; they call `FissionClientSet.CoreV1().FissionTenants()` (no namespace arg — `genclient:nonNamespaced`).

| Command | Maps to | Notes |
|---|---|---|
| `fission tenant enable <ns> [--function-namespace X] [--builder-namespace Y] [--quota …]` | Create FissionTenant; if `--quota`, also create a native `ResourceQuota` owned by the tenant | Idempotent |
| `fission tenant list [-o wide\|json\|yaml]` | `List()` | Table: NAME, NAMESPACE, FUNCTION-NS, READY, SOURCE, AGE |
| `fission tenant status <ns>` | `Get()` + condition table | Reuses `util.PrintConditions` |
| `fission tenant disable <ns> [--force]` | Delete FissionTenant, **guarded** | See drain below |

**`disable` is safe by default.**
It preflight-counts Fission workloads (`Functions(<ns>).List()` + triggers); if the namespace is non-empty and `--force` is absent it refuses with a clear message.
With `--force` (or empty), the tenant controller **drains via finalizer**: set `Ready=False` reason `Disabling`, remove the per-namespace Roles/RoleBindings/SAs/derived-key Secret it provisioned, remove the namespace from the live set, release the finalizer.
User Functions/CRs are left in place (disabling onboarding ≠ deleting content) — they simply stop being served.

### 6.4 Cross-namespace governance (already mostly closed)

`FunctionReference` has no namespace field (`validation.go:489`), so HTTP/Time/MQ triggers resolve their function in their *own* namespace — cross-namespace trigger→function abuse is structurally impossible.
The one historical gap, `KubernetesWatchTrigger.Spec.Namespace` watching arbitrary namespaces, is **already hard-denied** at `pkg/webhook/kuberneteswatchtrigger.go:49-53` (GHSA-gc3j-79f2-7vvw) with test coverage.
Recommendation: keep the hard-deny as default; if a legitimate cross-namespace watch use case ever appears, gate it on the *target* namespace's FissionTenant (resource-owner consent), never a self-asserted flag on the trigger.
This is YAGNI for v1 — only add a cross-reference comment.

The webhook (not CEL) is the correct tool for all same-namespace checks: CRD CEL cannot reliably read `metadata.namespace` from spec-scoped rules (documented in `function.go:84-86`).

**Follow-up (not v1):** namespace-prefix archive IDs in storagesvc (`getUploadFileName` → `path.Join(ns, uuid)`, derive ns from the ID) for true archive-content isolation. This is a storage-layout + Package-URL migration, so it ships after the key-scoping lands.

---

## 7. Phased delivery

Security-first ordering; each phase is independently shippable and leaves the system correct.
Per-phase test scope is in [testing-plan.md](./testing-plan.md); per-phase perf gates are in [performance-benchmarking.md](./performance-benchmarking.md).

| Phase | Deliverable | Ships the value of |
|---|---|---|
| **0** | Make `NamespaceResolver` thread-safe (setter + change feed); remove direct field writes. No behavior change. | Enabler |
| **1** | `FissionTenant` CRD + `conditions.go` constants + `--tenantController` subsystem + dedicated SA, driving only the live resolver set. CLI `list`/`status`/`enable`/`disable` against manually/CLI-created CRs. | Declarative onboarding surface |
| **2** | Helm post-upgrade migration Job seeds one FissionTenant per `additionalFissionNamespaces` entry (always incl. `default`); env vars unchanged → **no restart**. Deprecation banners. | **Fixes #3298 disruption** |
| **3** | Move Tier-A types to cluster-wide + membership-predicate caches; add reconcile-entry guard. Verify non-tenant objects ignored + RBAC-blocked. | Dynamic discovery |
| **4** | Tier-B dynamic `cluster.Cluster` for Secret/ConfigMap (executor cms), onboard/offboard. Tenant-controller RBAC/SA provisioning (`escalate`/`bind`) replacing chart-rendered roles for dynamically-onboarded namespaces. | Fully dynamic, least-privilege |
| **5** | Per-namespace derived HMAC keys (hmac package ns constructors, fetcher/builder/storagesvc ns-aware verifiers, dual-accept migration, tenant-controller key provisioning). | **Tenant secret isolation** |
| **6** | Opt-in `tenancy.mode: cluster` trusted-cluster path (cluster-wide Tier B + ClusterRoles). | Single-tenant simplicity |
| **7 (follow-up)** | Namespace-prefixed archive IDs for storagesvc content isolation. | Archive isolation |

Phases 0–2 alone close issue #3298 with zero security regression.
Phases 3–5 deliver the isolation that makes Fission genuinely multi-tenant.

---

## 8. Critical files

**Pillar 1 (dynamic watch):**
- `pkg/utils/namespace.go` — singleton → thread-safe live set + change feed; generalize `GetFunctionNS`/`GetBuilderNS` from "only remap `default`" to per-tenant mapping when the source flips from env to the FissionTenant informer.
- `pkg/utils/crmanager/crmanager.go` (`FissionCacheOptions`, shared by 6 managers), `pkg/executor/start.go` (`executorCacheOptions`), `pkg/router/router.go` (`routerCacheOptions`/`sliceWatchNamespaces`/`checkSliceWatchRBAC`) — Tier-A cluster-wide + predicate, Tier-B dynamic.
- `cmd/fission-bundle/main.go` — new `--tenantController` subsystem.

**Pillar 2 (HMAC):**
- `pkg/auth/hmac/keys.go`, `verifier.go`, `signer.go` — ns constructors, `VerifierFromKey`/`SignerFromKey`, candidate-key set, optional `\n<namespace>` canonical suffix (storagesvc only).
- `pkg/storagesvc/storagesvc.go` + `client/client.go` — signed ns header, per-claimed-ns key.
- `cmd/fetcher/app/server.go`, `pkg/fetcher/fetcher.go`, `pkg/fetcher/config/config.go` — ns verifier + storagesvc signer + `fetcherKey` env source.
- `cmd/builder/app/server.go` — ns verifier.
- Signer call-sites that pass target ns: `pkg/executor/executortype/poolmgr/gp_specialize.go:127`, `pkg/buildermgr/common.go:66`.

**Pillar 3 (CRD/CLI/chart):**
- `pkg/apis/core/v1/types.go` + `conditions.go` — FissionTenant type + markers + conditions → `make codegen` + `make generate-crds`.
- `pkg/fission-cli/cmd/tenant/{command,enable,disable,list,status}.go` + `cmd/fission-cli/app/app.go` registration.
- `charts/fission-all/templates/tenant-migration-job.yaml` (new), `_function-access-role.tpl` (the Roles the controller reproduces), `internal-auth-secret.yaml` (release-ns master only; tenant ns gets derived keys from the controller), new tenant-controller Deployment/RBAC templates.
- `charts/fission-all/templates/router/networkpolicy.yaml` + executor function-pod policy — add the `--tenantController` `svc:` label to any internal-listener allowlist it needs (per the CLAUDE.md NetworkPolicy gotcha).

Reference, unchanged: `pkg/webhook/kuberneteswatchtrigger.go` (already done), `pkg/executor/api.go` + router internal listener (stay master-scoped).

---

## 9. Recommendations for the outstanding PR #3476

PR #3476 ("Watch All Namespaces + Cross-Namespace Invocation Isolation", ~269 files, no maintainer review yet) is well-intentioned and identifies real problems, but its mechanism conflicts with the security-first goal.
Concrete guidance:

### Keep / salvage
- **The problem framing and the dynamic-onboarding goal.** The PR correctly identifies that namespace config must not require a restart, and that cross-namespace invocation deserves attention. Credit and carry this forward.
- **On-demand ServiceAccount provisioning** is the right instinct — but move it into the dedicated tenant-lifecycle controller (with confined `escalate`/`bind`), not spread across executor/buildermgr.
- **The same-namespace invocation guard concept** — but re-home it: the actual gap (KubernetesWatchTrigger) is already closed at admission; FunctionReference is structurally same-namespace. There is no need for a runtime guard at all.

### Change / drop
- **Drop `FISSION_WATCH_ALL_NAMESPACES` + cluster-wide ClusterRole as the default.** Make watch-all an explicit opt-in `tenancy.mode: cluster` (§4.5). Default stays least-privilege per-namespace Roles with dynamic provisioning. Cluster-wide secret read across all namespaces is the single biggest regression to avoid.
- **Drop `internalAuth.enabled: false` as the default.** Disabling HMAC to accommodate unsigned KEDA/GraphQL connectors trades the GHSA-3g33 protection for convenience. Instead, keep auth on and handle unsigned connectors the documented way (NetworkPolicy-only acceptance for those specific channels, or signing-aware connector images) — the design already carves out the KEDA caveat without disabling auth globally.
- **Drop the podIP→namespace caller-attribution cache.** Pod IPs are reused and the cache fails closed on cold start (user-visible failures). It is an application-layer reimplementation of NetworkPolicy + admission. Replace with: admission (already done) + NetworkPolicy + the per-namespace HMAC keys (§5), which make impersonation cryptographically impossible rather than IP-heuristic.
- **Stop propagating the master secret into tenant namespaces.** This is the latent issue the PR inherits from today's chart; §5.4 replaces it with per-namespace derived keys.
- **Decompose the 269-file PR** into the phased slices in §7 so each is reviewable and bisectable. A single mega-PR touching RBAC, auth defaults, and watch scope is hard to review and risky to merge — which is partly why it has stalled.

### Net message to the contributor
The operational pain is real and worth fixing; the disruption fix (Phases 0–2) can land quickly and independently.
The security-sensitive parts (watch scope, auth defaults, isolation) should invert the PR's defaults — least-privilege and auth-on by default, with watch-all as opt-in — and lean on cryptographic per-namespace keys instead of IP heuristics.

---

## 10. Verification

Detailed test plan: [testing-plan.md](./testing-plan.md).
Detailed performance plan: [performance-benchmarking.md](./performance-benchmarking.md).

Headline acceptance gates:

- **Zero-restart onboarding** — onboarding a namespace restarts zero control-plane pods (hard gate, the #3298 fix).
- **Isolation attack-walk** — a tenant holding only `K(fetcher, A)` is rejected by tenant-B's fetcher and cannot sign executor/router-internal at all.
- **No cold-start regression** — poolmgr cold-start p99 within the agreed budget of baseline (the RFC-0002 byte-identical-path invariant must hold).
- **Bounded memory scaling** — control-plane heap grows sub-linearly with namespace count (Tier-A is one informer; only Tier-B scales).

---

## 11. Risks & open questions

1. **Tier-B dynamic `cluster.Cluster` teardown** on offboarding is not a first-class controller-runtime operation — mitigate with a per-tenant `context.CancelFunc` + idempotent reconcile-entry no-op; cover with the onboard→offboard→re-onboard test. (Highest-risk new code.)
2. **`escalate`/`bind` grant** is genuinely privileged even confined to one SA — consider a `ValidatingAdmissionPolicy` restricting the tenant controller to the fixed fetcher/builder Role shapes, and audit-log its RBAC writes.
3. **storagesvc archive-content isolation** is only half-solved by key-scoping until Phase 7 (ns-prefixed IDs) — set expectations; UUID-unguessability + NetworkPolicy remain the interim barrier.
4. **CM/Secret pod-recycle (RFC-0004)** forces a per-namespace Secret *watch* — open question whether operators would accept degrading to periodic resync in multi-tenant mode to avoid even per-namespace Secret watches. Default: keep the watch (Tier B).
5. **Canonical-string change must be surgical** — appending `\n<namespace>` for storagesvc only, leaving the 4-field form for all master-scoped channels, or every in-flight signature on unchanged channels breaks. Gate strictly behind the ns-scoped constructors.
6. **`preupgradechecks` / `archivePruner`** read the static set as one-shot jobs — confirm they consume the live set or are explicitly scoped, or they silently skip new tenants.

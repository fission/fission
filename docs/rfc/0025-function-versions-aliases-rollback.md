# RFC-0025: Function versions, aliases, and instant rollback

- Status: Proposed (revised 2026-07-19, pre-implementation: design review against the shipped `pkg/router/routetable`, `functionReferenceResolver`, `pkg/executor/fscache`, `pkg/canaryconfigmgr`, and `pkg/webhook` code — the resolver no longer caches, the warm-pool reaper drains non-latest generations, the zero-drift "CI bar" is documentation-only, and a TLA+-checked alias/GC-race spec)
- Tracking issue: TBD
- Supersedes: — (long-term it absorbs `CanaryConfig`, kept working via a shim through a deprecation window)
- Targets: Fission v1.N+1
- Requires: nothing hard; OCI package delivery (RFC-0001/0012) makes version pins content-addressed, and the RFC-0013 route table provides the zero-rebuild pointer-swap path this rides on (`routetable.ApplyTrigger` → `HandlerSwapped`, verified present).

## Summary

Make deploys safe: every runtime-affecting `fn update` snapshots an immutable **`FunctionVersion`**, and named **aliases** (`prod`, `staging`) are movable pointers to versions, with optional weighted splits between two versions.
Triggers reference `name:alias`; `fission fn rollback --alias prod` repoints one pointer and propagates as an atomic route-table handler swap — no mux rebuild, no pod churn for warm versions.
This is the Lambda versions+aliases model, the backbone of production CI/CD on every mature FaaS.

## Motivation

Fission functions are mutable in place: a bad `fn update` has no one-command undo — recovery means re-applying an older spec from wherever the user kept it, then waiting out specialization.
CanaryConfig exists but shifts traffic between **two separately named functions**, so teams must clone `orders` into `orders-v2`, duplicate triggers' weight maps, and hand-manage cleanup; it automates the shift, not the versioning.
Meanwhile the substrate for doing this properly has landed: OCI delivery gives content-addressed immutable artifacts (a version pin is a digest pin), the RFC-0002 data plane already stamps pods with `fission.io/function-generation`, and RFC-0013 made per-trigger handler updates a 21µs atomic pointer swap.
What is missing is only the user-facing model.

## Goals

- Automatic immutable version snapshots on runtime-affecting updates; explicit `fission fn publish` also supported.
- Named aliases as version pointers; triggers (HTTP, MQ, timer, kubewatcher) can reference `function:alias`.
- Weighted alias (two versions, integer weights) subsuming the canary use case; `CanaryConfig` keeps working against aliases via a shim.
- One-command rollback with warm-target guarantees when the previous version's pods still exist.
- Bounded storage: retain-last-N GC with ownerRef-tied package snapshots.

## Non-goals

- Versioning Environments or Packages independently (a FunctionVersion captures the resolved digest of what it needs).
- More than two versions per weighted alias (Lambda's limit too; simplicity wins).
- Automatic progressive rollout logic in this RFC (that remains the canary controller's job, retargeted onto aliases).
- Git-style history/diff tooling (versions are recoverable specs, not a VCS).

## Design

### Data model

`FunctionVersion` is a new namespaced CRD (10-step checklist, codegen, generate-crds):

```go
type FunctionVersionSpec struct {
    FunctionName string       `json:"functionName"`
    Sequence     int64        `json:"sequence"`          // v1, v2, ... per function; name = "<fn>-v<seq>"
    Snapshot     FunctionSpec `json:"snapshot"`          // deep copy at publish time (versioning config zeroed to avoid recursion)
    // PackageDigest pins content: the OCI digest (RFC-0001/0012) or the
    // storagesvc archive checksum for legacy packages.
    PackageDigest string      `json:"packageDigest"`
    PublishedAt   metav1.Time `json:"publishedAt"`
}
```

Immutability is enforced by the validating webhook, not CEL (spec immutability is one of the known CRD-CEL limits). The template already ships: `WorkflowRun.ValidateTransition(old, new)` rejects any `!equality.Semantic.DeepEqual(old.Spec, new.Spec)` via the `GenericWebhook[T]` `UpdateValidator` facet (`pkg/webhook/generic.go`, `pkg/webhook/workflowrun.go`) — `FunctionVersion` immutability is the same `ValidateTransition` five-liner, and it catches every mutation verb including patches (the risk V1 names).
`FunctionVersion` carries an ownerRef to its Function (cascade delete) and, when the package was snapshotted, owns the snapshot Package CR.

Aliases live in a separate small CRD rather than inside `FunctionSpec` — separate objects avoid alias-edit vs fn-update conflicts and give aliases their own RBAC (deploy tooling may move aliases without function write access):

```go
type FunctionAliasSpec struct {
    FunctionName string  `json:"functionName"`
    Version      string  `json:"version"`               // FunctionVersion name
    Weight       *int    `json:"weight,omitempty"`      // nil = 100%
    SecondaryVersion string `json:"secondaryVersion,omitempty"` // receives 100-Weight
}
```

### Publishing

- The Function webhook computes whether an update is runtime-affecting (package ref/digest, env, resources, entrypoint, podspec — not annotations/labels); if so and `spec.versioning.mode: auto` (default for opted-in functions), the function controller creates the next `FunctionVersion` after the referenced package reaches `succeeded` (so a version never points at a build that failed).
- `fission fn publish --name orders` forces a snapshot regardless; `--description` lands in annotations.
- Functions without opt-in (`spec.versioning` nil) behave exactly as today — zero-cost for existing users.
- Version resolution of `$LATEST`: the bare function name keeps meaning "the live mutable spec", Lambda-style; only alias/version references pin.

### Reference resolution

`FunctionReference` (used by all trigger types) gains an optional `alias` (and `version`) field next to `name`; `functionReferenceResolver.resolve` learns a `name:alias` case that reads the alias CR → FunctionVersion → the snapshot spec.
The resolver **does not cache** (its trigger-RV-keyed result cache was removed in RFC-0014 phase 3 — resolution now runs only during a mux rebuild), so there is no resolver-side cache key to extend; the Generation-keyed change detection lives in the route table, not the resolver.

An alias is a **separate CRD**, so moving it does not bump the referencing trigger's Generation. The router therefore adds a `FunctionAlias` informer that, on an alias change, enqueues the triggers referencing it for re-resolution and re-apply. The repoint then rides the shipped `routetable.ApplyTrigger` path: the new alias target resolves to a different FunctionVersion whose snapshot Generation lands in `RouteSpec.FnGens` (the per-backend `name → Generation` map RFC-0013 already keys handler swaps on), so `ApplyTrigger` finds the route **shape-equal** with changed `FnGens` and returns `HandlerSwapped` — one atomic `HandlerRef.Swap`, explicitly **not** a `ShapeChanged`, so the debounced materializer never runs.
Weighted aliases resolve to the same two-target `resolveResultMultipleFunctions` shape the canary path produces today (`FunctionWeights` prefix-sum distribution), so the per-request weighted pick reuses that machinery unchanged.

### Executor

- Cache keying: poolmgr's cache already keys by `crd.CacheKeyURG` = `(UID, ResourceVersion, Generation)` (`pkg/executor/fscache/poolcache.go`), and pods carry `fission.io/function-generation` (`gp_pod.go`, also on the headless Service selector); the router's `functionServiceMap`, by contrast, keys by `metadataKey{Name, Namespace, ResourceVersion}` (`pkg/router/functionServiceMap.go`) with no Generation and must gain a version dimension.
  A specialize request for `orders-v3` carries the snapshot spec, and its pods are labeled with that generation, so two versions can key side by side.
- **Warm rollback needs a reaper-policy change — this is the load-bearing correction.** The RFC's "instant rollback when the old version's pods still exist" does **not** hold against the shipped idle reaper: `poolcache.ListAvailableValue` computes `latestFuncGen` per UID and sets `svcRetain = 0` for **every non-latest generation**, so only the newest generation keeps warm idle pods — an older version's pool is drained to zero even while an alias could still need it. Warm rollback therefore requires teaching the reaper that "referenced by a live alias" is an independent retain reason: retain `minPoolSize` warm pods for any generation an alias (primary or weighted secondary) currently points at, not just the latest. Without this change, "rollback to the previous version" always pays a cold start. This is a new phase-3 design element, not a free property of the existing pools.
- Rollback to a version with no alias-retained pods pays one ordinary cold start (~100ms poolmgr budget), still incomparably better than re-deploying.
- Newdeploy versions map to per-version Deployments with the version label; the known live-object reconcile race on `fn update` (coalesced specialization) actually *shrinks*, because versioned specs never mutate.

### CanaryConfig absorption

Phase-gated: the canary controller learns to operate on a weighted alias (increment `Weight`, watch the same Prometheus failure signal, roll back by repointing) when its HTTPTrigger references an alias; existing function-pair canaries keep working unchanged through the deprecation window.
Docs steer new users to aliases; removal is a separate future decision.

### CLI

```
fission fn publish --name orders [--description "..."]
fission fn versions --name orders                       # list, with digests + ages
fission alias create --function orders --name prod --version orders-v3
fission alias update --name prod --version orders-v4 [--weight 90]   # weighted rollout
fission fn rollback --name orders --alias prod          # repoint to previous version (one CRD patch)
fission fn gc-versions --name orders --keep 10          # manual; auto policy in spec.versioning.retain
```

`fission spec` (declarative apply) treats aliases as first-class objects so GitOps flows pin versions explicitly.

### GC

`spec.versioning.retain` (default 10): the function controller deletes the oldest unaliased versions beyond N; versions referenced by any alias are never GC'd (webhook blocks alias→missing-version at admission, controller re-checks at delete).
Package snapshots ride ownerRefs; OCI artifacts follow the RFC-0012 reaper's retention rules keyed on referenced digests.

## Invariants & verification

**Invariants.**

- V1 *(immutability)*: a published `FunctionVersion`'s spec never changes — enforced by the validating webhook, tested against every mutation verb including patches.
- V2 *(no dangling aliases)*: an alias always resolves — it can never point at a version that does not exist (webhook at admission, controller re-check at version delete).
- V3 *(GC safety)*: retention GC never deletes a version any alias references.
- V4 *(classifier determinism)*: the runtime-affecting-field classifier is a pure function — deterministic, and `classify(spec, spec) = false` (no self-triggered snapshots).
- V5 *(weight sanity)*: a weighted alias's effective weights always sum to 100, and resolution distributes accordingly.
- V6 *(rollback atomicity)*: a repoint is one CRD patch propagated as one handler-pointer swap — requests observe either the old or the new version, never an error window.

**Verification.** Most of the surface is validation logic serialized through the apiserver, and repoint rides the already-verified RFC-0013 pointer swap. But there is exactly one genuinely concurrent claim — V2/V3, retention GC racing a concurrent alias create — and it is **TLC-checked**, not just interleaving-tested: `aliasgc.tla` models the two-phase GC sweep against alias repoint, and its negative config (`RecheckGuard = FALSE`) produces the dangling-alias trace. It is the design source of truth for "GC must re-check alias references *inside* the delete (or gate delete on an alias-held finalizer/ownerRef), never act on the start-of-sweep snapshot," and it shows the webhook admission check alone is insufficient (admission passing does not keep the version alive against a concurrent sweep).

- V1: envtest webhook matrix (update/patch/delete-spec attempts) against `ValidateTransition`.
- V2/V3: the `aliasgc.tla` model above, plus an envtest controller test driving the modeled interleaving (alias-create committing between the GC scan and the GC delete) to confirm the implementation matches the guarded model.
- V4: `pgregory.net/rapid` properties over generated spec pairs — idempotence, symmetry of "not affecting", and a golden table for each classified field.
- V5/V6: integration — statistical distribution assertion for 90/10 splits (reusing the canary test's tolerance approach), and a repoint-under-load test asserting zero non-2xx responses during rollback (respecting the coalesced-specialization race: assert on served responses, not live Deployment specs).
- Zero-drift gate: alias operations must not increment `fission_router_route_resync_drift_total` and must not trigger the materializer. Note this "bar" is documentation-only today (`CLAUDE.md`, RFC-0013) — there is no code-level `== 0` assertion — so this RFC's integration test must **add** the metric assertion to make it a real gate, not merely inherit it.

## Alternatives considered

- **Aliases inside `FunctionSpec`** — one object fewer, but every alias move is a function update (churning Generation, waking function watchers, racing user edits) and RBAC cannot separate "may deploy" from "may edit function"; rejected.
- **Versions as annotations/ConfigMaps** — invisible to `kubectl`, no ownerRefs/RBAC/webhook immutability; CRDs are exactly the right tool at these (low) write rates.
- **Reuse CanaryConfig as the rollout primitive** — backwards: canary automates weight movement, versions/aliases define what the weights point at; the shim direction (canary atop aliases) preserves the investment.
- **Snapshot into the statestore (RFC-0021)** — versions are control-plane metadata at human rates: CRDs give free auditability and GitOps; statestore is for data-plane state.
- **Git as the version store ("just use GitOps")** — real teams do both; GitOps recovers *specs* but not the platform-side instant-rollback path (warm pools, one-patch repoint, weighted splits) nor safety for `fission fn update` users.

## Backward compatibility

Fully additive: functions without `spec.versioning` and triggers without `alias` behave byte-identically.
The `FunctionReference` extension is optional-field-only (existing `name`/`function-weights` types untouched).
CanaryConfig unaffected until the opt-in shim phase.

## Rollout phases (one PR each, bisectable)

1. `FunctionVersion` + `FunctionAlias` CRDs, codegen, webhook immutability + reference validation, `fn publish` / `fn versions` / `alias` CLI (no router integration — versions are inert but inspectable).
2. Resolver + router: `name:alias` references, weighted alias via the HandlerRef path, `fn rollback`; integration tests.
3. Executor version-keyed caching + the **reaper retention change** (retain warm pods for alias-referenced non-latest generations, not just the latest — the `ListAvailableValue`/`latestFuncGen` policy); rollback-warmth integration test asserting no cold start on rollback to an alias-retained version.
4. Auto-publish on runtime-affecting update + retention GC.
5. Canary-on-aliases shim + deprecation docs.

## Verification / test plan

- Webhook: immutability rejection matrix; alias→missing-version rejection; runtime-affecting-field classifier table test.
- Integration: publish → alias → invoke-by-alias; weighted 90/10 split distribution assertion (statistical bounds, reusing the canary test's tolerance approach); rollback repoint latency (< 1s to first correct response); warm-rollback — after the phase-3 reaper-retention change, an alias-retained older version serves rollback with **no** cold start observed (this test is meaningless before that change, since the reaper drains non-latest pools).
  Respect the known coalesced-specialization race: assert on served responses, not live Deployment specs.
- Route-table: alias weight tick and repoint produce zero `fission_router_route_resync_drift_total` and no materializer runs. This test must **assert** the metric (the "bar" is documentation-only today — see Verification), turning the RFC-0013 convention into an enforced gate for alias ops.
- GC: `aliasgc.tla` (V2/V3 model) plus an envtest retain-N sweep that never deletes an aliased version and survives the modeled alias-create-vs-delete interleaving.

## Open questions

- Whether `mqtrigger`/`timer`/`kubewatcher` references support aliases in v1 or phase 2 ships HTTP-only first (leaning: all trigger types at once — the resolver is shared, and partial support confuses).
- Auto-publish default: opt-in (`versioning.mode: auto` required, as drafted) vs on-by-default with retain-N; opt-in first, flip later with data.
- ~~Interaction with `fission spec apply` pruning when versions are auto-created objects the spec never declared.~~ **Resolved by review:** `spec apply --delete` only prunes objects carrying the spec's `FISSION_DEPLOYMENT_UID_KEY` annotation and absent from the desired set (`ownedByDeployment`, `pkg/fission-cli/cmd/spec/resourcetype.go`). Controller-created `FunctionVersion`s carry no such annotation, so they are never pruned. The design requirement is therefore simply: the auto-publish controller must **not** stamp the deployment-UID annotation on versions it creates (and there is no `applyFunctionVersions` closure, so versions are not a spec-managed kind). Aliases, if made spec-first for GitOps, *would* get an `applyFunctionAliases` closure and be pruned normally — which is the intended GitOps behavior.

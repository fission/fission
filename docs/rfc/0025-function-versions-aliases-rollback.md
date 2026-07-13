# RFC-0025: Function versions, aliases, and instant rollback

- Status: Proposed
- Tracking issue: TBD
- Supersedes: — (long-term it absorbs `CanaryConfig`, kept working via a shim through a deprecation window)
- Targets: Fission v1.N+1
- Requires: nothing hard; OCI package delivery (RFC-0001/0012) makes version pins content-addressed, and the RFC-0013 route table provides the zero-rebuild pointer-swap path this rides on.

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

Immutability is enforced by the validating webhook (spec updates rejected), the pattern CEL cannot cover for us anyway per the known CRD-CEL limits.
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

`FunctionReference` (used by all trigger types) gains an optional `alias` (and `version`) field next to `name`; `pkg/router/functionReferenceResolver.go` resolves `name:alias` → alias CR → FunctionVersion → the snapshot spec, caching by (alias UID, alias Generation) consistently with the Generation-keyed change detection the router reconcilers already use.
Weighted aliases resolve to the same two-target shape the canary path produces today, so the per-request weighted pick reuses the existing RFC-0013 `HandlerRef` machinery — an alias weight change or repoint is a handler-pointer swap, explicitly **not** a route-shape change, so the debounced materializer never runs.

### Executor

- Cache keying: poolmgr's cache already keys by `(function UID, Generation)` (`pkg/executor/fscache/poolcache.go`), and pods carry `fission.io/function-generation`; the router's `functionServiceMap`, however, keys by `(name, namespace, ResourceVersion)` today and must gain a version dimension.
  Versions make the executor's keying explicit — a specialize request for `orders-v3` carries the snapshot spec, and its pods are labeled with the version name, so two versions run side by side with independent warm pools (that is what makes rollback instant when the old version is still warm).
- Idle versions reap normally; rollback to a cold version pays one ordinary cold start (~100ms poolmgr budget), which is still incomparably better than re-deploying.
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

**Verification.** No model checking here — every mutation is serialized through the apiserver and the router consumes it via the already-verified RFC-0013 pointer-swap machinery; the risk profile is validation logic, not concurrency.

- V1/V2/V3: envtest webhook + controller matrix (update/patch/delete attempts; GC sweep racing an alias create is the one genuine race — covered with a deliberate interleaving test).
- V4: `pgregory.net/rapid` properties over generated spec pairs — idempotence, symmetry of "not affecting", and a golden table for each classified field.
- V5/V6: integration — statistical distribution assertion for 90/10 splits (reusing the canary test's tolerance approach), and a repoint-under-load test asserting zero non-2xx responses during rollback (respecting the coalesced-specialization race: assert on served responses, not live Deployment specs).
- Zero-drift gate: alias operations must not increment `fission_router_route_resync_drift_total` and must not trigger the materializer (same CI bar as RFC-0013).

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
3. Executor version-keyed caching + side-by-side warm pools; rollback-warmth integration test.
4. Auto-publish on runtime-affecting update + retention GC.
5. Canary-on-aliases shim + deprecation docs.

## Verification / test plan

- Webhook: immutability rejection matrix; alias→missing-version rejection; runtime-affecting-field classifier table test.
- Integration: publish → alias → invoke-by-alias; weighted 90/10 split distribution assertion (statistical bounds, reusing the canary test's tolerance approach); rollback repoint latency (< 1s to first correct response); warm-rollback (old version pods alive → no cold start observed).
  Respect the known coalesced-specialization race: assert on served responses, not live Deployment specs.
- Route-table: alias weight tick and repoint produce zero `fission_router_route_resync_drift_total` and no materializer runs (same CI bar as RFC-0013).
- GC: retain-N sweep never deletes an aliased version.

## Open questions

- Whether `mqtrigger`/`timer`/`kubewatcher` references support aliases in v1 or phase 2 ships HTTP-only first (leaning: all trigger types at once — the resolver is shared, and partial support confuses).
- Auto-publish default: opt-in (`versioning.mode: auto` required, as drafted) vs on-by-default with retain-N; opt-in first, flip later with data.
- Interaction with `fission spec apply` three-way diffs when versions are auto-created objects the spec never declared (likely: versions are excluded from spec-apply pruning by kind).

# RFC-0025: Function versions, aliases, and instant rollback

- Status: Proposed (revised 2026-07-19, pre-implementation: design review against the shipped `pkg/router/routetable`, `functionReferenceResolver`, `pkg/executor/fscache`, `pkg/canaryconfigmgr`, and `pkg/webhook` code — the resolver no longer caches, the warm-pool reaper drains non-latest generations, the zero-drift "CI bar" is documentation-only, and a TLA+-checked alias/GC-race spec; re-revised 2026-07-22 after RFC-0023 stateful functions (#3593) and the `CacheKeyUG` re-key (#3596) merged — adds the shipped-feature interaction matrix (name-scoped keyed state, sticky-vs-weighted pick, version-blind async/workflow targets), corrects the executor keying facts, resolves the trigger-coverage open question, and grounds the CanaryConfig deprecation path)
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
    // Environment observation at publish time (observational, not pinning —
    // see "Environment & Package changes across the version boundary").
    EnvObservedGeneration int64  `json:"envObservedGeneration,omitempty"`
    EnvRuntimeImage       string `json:"envRuntimeImage,omitempty"`
    PublishedAt   metav1.Time `json:"publishedAt"`
}
```

Immutability is enforced by the validating webhook, not CEL (spec immutability is one of the known CRD-CEL limits). The template already ships: `WorkflowRun.ValidateTransition(old, new)` rejects any `!equality.Semantic.DeepEqual(old.Spec, new.Spec)` via the `GenericWebhook[T]` `UpdateValidator` facet (`pkg/webhook/generic.go`, `pkg/webhook/workflowrun.go`) — `FunctionVersion` immutability is the same `ValidateTransition` five-liner, and it catches every mutation verb including patches (the risk V1 names).
`FunctionVersion` carries an ownerRef to its Function (cascade delete) and, when the package was snapshotted, owns the snapshot Package CR.

Aliases live in a separate small CRD rather than inside `FunctionSpec` — separate objects avoid alias-edit vs fn-update conflicts and give aliases their own RBAC (deploy tooling may move aliases without function write access):

```go
type FunctionAliasSpec struct {
    FunctionName string  `json:"functionName"`
    Version      string  `json:"version,omitempty"`       // FunctionVersion name; XOR with PackageDigest
    PackageDigest string `json:"packageDigest,omitempty"` // declarative/GitOps pinning: resolved to the version that pinned this digest
    Weight       *int    `json:"weight,omitempty"`      // nil = 100%
    SecondaryVersion string `json:"secondaryVersion,omitempty"` // receives 100-Weight
}
```

`FunctionAliasStatus` records `ResolvedVersion` (the digest→version resolution result) and a bounded `History` of previous targets — the substrate for `fn rollback` and pipeline audit (see the CI/CD section).

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

### Per-trigger versioning semantics (added 2026-07-22)

Every trigger type's versioning behavior is determined by one question — **when is the alias pointer read** — plus whether per-invocation weighted randomness suits that invocation shape.
Because all invokers ultimately resolve through the router (see the resolved trigger-coverage question below), the mechanism is uniform; only the timing and the recommended usage differ.

| Trigger | Pointer read at | Weighted alias | The friendliness story |
|---|---|---|---|
| HTTP | route-apply (RFC-0013 swap) | yes; sticky = deterministic hash pick | Blue/green + canary + instant rollback per route; different routes can target different aliases of the *same* function (`/api`→`prod`, `/beta`→`staging`) — no more function cloning |
| MQ | per delivery | yes, with an at-least-once caveat | Rollback retroactively fixes poison-message handling (redeliveries hit rolled-back code); consumers upgrade without topic re-subscription |
| Timer | per firing | legal but discouraged | Deploys land atomically *between* firings, never mid-job; a version-pinned timer freezes a scheduled job until explicit promotion |
| Kubewatcher | per event | legal but discouraged | Cluster-event handlers ride aliases: a bad deploy during an event burst is one repoint away from recovery |
| MCP tools | per `tools/call` | discouraged (agent-visible variance) | Alias-addressed tools = stable tool names with upgradeable backends; version pins = reproducible agent behavior |
| Async destinations | stamped at enqueue (alias refs) | n/a per envelope | Retries are deterministic across repoints; each pipeline hop (onSuccess/onFailure) is independently rollback-able |
| Workflows | per step today; run-start pinning is the open question | no — incoherent within a run | Once pinned at RunStarted, a long-running run is version-consistent: no Frankenstein runs spanning a deploy |

Semantics this table encodes:

- **Delivery-time readers (MQ/timer/kubewatcher/MCP) get upgrade-without-redeploy for free** — the publisher never changes, only the pointer moves; this is the payoff of resolving in the router rather than per publisher.
- **Weighted aliases are mechanically available everywhere the router resolves, but recommended only where per-invocation variance is harmless** (HTTP request/response).
  For MQ the docs must state that an at-least-once redelivery may hit a different version than the first attempt during a split; for timer/kubewatcher/MCP a split is a per-firing coin flip and the guidance is "use repoint, not weights".
- **MCP needs one extra hook:** `FunctionSpec.Tool` (including `InputSchema`) lives in the snapshot, so an alias repoint can change the advertised tool schema — the MCP registry must watch `FunctionAlias` and emit `tools/list_changed` on repoint, and reconcile the tool entry from the *resolved version's* snapshot rather than the live Function.

### Executor

- Cache keying: poolmgr's cache keys by `crd.CacheKeyUG` = `(UID, Generation)` (`pkg/executor/fscache/poolcache.go`) — #3596 dropped ResourceVersion and renamed the type from `CacheKeyURG`, precisely because RV churns on status-only writes while Generation moves only on spec changes; that fix independently confirms Generation is the right change-detection axis for versions.
  Two executor caches still key by `crd.CacheKeyUR` = `(UID, ResourceVersion)` — `FunctionServiceCache.byFunction` (`functionServiceCache.go`) and gpm's `functionEnv` env cache (`gpm.go`) — and the router's executor-dispatch dedup key (`resolver_executor.go`, `CacheKeyURFromMeta`) is UR-keyed too; phase 3 should migrate these to UG/version keying for the same status-churn reason #3596 fixed.
  The router's `functionServiceMap` still keys by `metadataKey{Name, Namespace, ResourceVersion}` (`pkg/router/functionServiceMap.go`) with no Generation and must gain a version dimension (unchanged by #3596, which only re-keyed the executor side).
  Pods carry `fission.io/function-generation` (`gp_pod.go`), so a specialize request for `orders-v3` carries the snapshot spec and its pods key side by side.
- **The generation gate lives in the Service selector, not the endpoint index.** `functionServiceSelector` (`gp_service.go`) includes `fission.io/function-generation`, so the per-function headless Service's EndpointSlices only ever select ONE generation's pods; the router's endpoint index (`pkg/router/endpointcache`) keys `FnKey{Namespace, Name}` and never reads the generation label.
  Side-by-side warm pools therefore need either (a) per-version headless Services with distinct names surfacing as distinct index entries, or (b) a version dimension added to `FnKey` and the slice-label contract — a phase-3 design element alongside the reaper change, not a free property of the shipped index.
- **Warm rollback needs a reaper-policy change — this is the load-bearing correction.** The RFC's "instant rollback when the old version's pods still exist" does **not** hold against the shipped idle reaper: `poolcache.ListAvailableValue` computes `latestFuncGen` per UID and sets `svcRetain = 0` for **every non-latest generation**, so only the newest generation keeps warm idle pods — an older version's pool is drained to zero even while an alias could still need it. Warm rollback therefore requires teaching the reaper that "referenced by a live alias" is an independent retain reason: retain `minPoolSize` warm pods for any generation an alias (primary or weighted secondary) currently points at, not just the latest. Without this change, "rollback to the previous version" always pays a cold start. This is a new phase-3 design element, not a free property of the existing pools.
- Rollback to a version with no alias-retained pods pays one ordinary cold start (~100ms poolmgr budget), still incomparably better than re-deploying.
- Newdeploy versions map to per-version Deployments with the version label; the known live-object reconcile race on `fn update` (coalesced specialization) actually *shrinks*, because versioned specs never mutate.

### Interactions with shipped stateful/async/workflow features (added 2026-07-22)

RFC-0022 (durable workflows), RFC-0023 (keyed state + sticky routing), and RFC-0024 (async invocation) all merged after this RFC was drafted; each intersects the version model.

**Keyed state is name-scoped, not version-scoped (RFC-0023).**
`StateConfig.EffectiveKeyspace(fnName)` defaults the keyspace to the function *name*, and the state token is derived from `(namespace, keyspace)` only (`pkg/auth/hmac/keys.go` `DeriveStateKeyspaceKey`) — no UID, Generation, or version appears anywhere in the key.
Two side-by-side versions therefore share one keyspace and one derived token.
This is the **intended default**: state must outlive deploys (the whole point of RFC-0023), and rollback rolls back *code, never state* — docs must say this explicitly.
Two consequences the design must own: (a) the `FunctionVersion.Snapshot` zeroes only *versioning* config, never `spec.state`, so both versions inherit the same keyspace deliberately; (b) a weighted split runs two code versions against one keyspace, so state-schema-incompatible releases must either use an explicit per-version `StateConfig.Keyspace` (the escape hatch, at the cost of starting cold on state) or not use weighted rollout — a documented operational rule, not a mechanism.

**Sticky routing vs weighted alias needs a deterministic pick (RFC-0023).**
The weighted pick (`getCanaryBackend`, random per request) runs *upstream* of the endpoint index's `Admit` sticky-HRW pick, and per-version pools surface as distinct index entries — so a random per-request version pick would bounce a sticky session between version pools on every request.
Design element: when the resolved backend is a weighted alias AND the trigger has sticky routing, derive the version pick deterministically from the sticky key (hash the sticky key into `[0,100)` and compare against the weight split) so a given session stays on one version for the lifetime of the split; a weight change migrates only the bounded fraction of sessions whose hash crosses the new boundary.
Unkeyed requests keep the random pick.

**Async invocations are version-blind at delivery (RFC-0024).**
The envelope (`pkg/router/asyncinvoke/envelope.go`) stamps policy and destinations at enqueue but records the target as bare `(namespace, name)`; the deliverer re-resolves by name at delivery time.
A rollback between enqueue and a retry silently changes which code runs the retry.
Design element: when the enqueue-time reference is an alias, stamp the *resolved FunctionVersion* into the envelope so retries are deterministic across repoints (bare-name references keep live resolution, consistent with `$LATEST` semantics).
`DestinationRef` (onSuccess/onFailure) gains the same optional alias/version fields as `FunctionReference`.

**Environment & Package changes across the version boundary (added 2026-07-22).**
The two dependency axes behave asymmetrically, and the RFC must say so.
*Packages are already inside the boundary:* `PackageRef` carries `ResourceVersion` (`types.go`), and `fission pkg update` fans out `UpdateFunctionPackageResourceVersion` to every referencing function (`pkg/fission-cli/cmd/package/update.go`) — a package content change IS a function spec change, so the runtime-affecting classifier sees it and auto-publish mints one version per dependent function (correct: each dependent's runtime changed), all sharing the new `PackageDigest` as the cross-correlation key.
The version-owned snapshot Package CR / OCI digest makes published versions immune to later rebuilds.
*Environments bypass the boundary:* `FunctionSpec` references the env by name only, and an env update drives `reconcileEnvPool` → `updatePoolDeployment` (`gpm.go`), recycling pools and specialized pods with the new runtime image under **every version** of every dependent function — no Generation bump anywhere, so no auto-publish, no version-history entry, and rollback cannot restore the old runtime.
"Instant rollback" is therefore scoped to code+config, not runtime, unless the design accounts for it.
Design elements (phase 4): the publish path records an **environment observation** in `FunctionVersionSpec` — `EnvObservedGeneration` + the resolved runtime image — purely observational, not enforcement; `fn versions` and the alias status surface **env drift** (`EnvDrift` condition when the live env's Generation ≠ the target version's observation), and `fn rollback` warns on a drifted target; `fission env impact --name <env>` lists dependent functions/aliases and which alias targets were published under older env generations — the cross-correlation query, answerable entirely from recorded observations.
Full env *pinning* (per-version runtime images) stays a non-goal: it requires per-env-generation pools, which is environment versioning through the back door — Lambda solves this by making runtimes/layers themselves immutable-versioned objects, a separate future RFC if ever warranted.
Whether env changes should additionally fan out auto-publish (minting new versions for opted-in dependents so env upgrades appear in version history) is an open question below.

**Workflow steps invoke live by name (RFC-0022).**
A run pins its step *graph* (spec snapshot in the event stream at RunStarted) but each step calls its target function by name at execution time (`pkg/workflow/invoker.go`), so a mid-run rollback means later steps run different code than earlier steps.
Whether a run should resolve alias→version at RunStarted and pin all step targets for the run's lifetime is an open question below — it changes replay/determinism semantics and is separable from this RFC's phases.

### CanaryConfig absorption

Phase-gated: the canary controller learns to operate on a weighted alias (increment `Weight`, watch the same Prometheus failure signal, roll back by repointing) when its HTTPTrigger references an alias; existing function-pair canaries keep working unchanged through the deprecation window.
Docs steer new users to aliases; removal is a separate future decision.

**Grounded deprecation path (2026-07-22 survey of the shipped canary machinery).**
The canary controller is already modern — controller-runtime + workqueue + `RequeueAfter`, opt-in leader election, `/status` conditions (`pkg/canaryconfigmgr/`) — so the shim reuses a healthy reconcile loop, not legacy code.
Its failure signal is a hard Prometheus coupling (`fission_function_calls_total` / `fission_function_errors_total` with offset-window math; the controller refuses to start without a Prometheus URL) — the shim keeps this unchanged, only retargeting the weight writes from `HTTPTrigger.FunctionWeights` to `FunctionAlias.Weight`.
The router's weighted-pick path (`function-weights`, `pkg/router/canary.go`) is independent of CanaryConfig and is exactly what weighted aliases reuse — router code is re-targeted, not removed.
What full deprecation removes: the `CanaryConfig` CRD + generated clients, `pkg/canaryconfigmgr`, the `--canaryConfig` fission-bundle head, the Helm `canaryDeployment.*` surface, and the `fission canary` CLI; nothing in preupgradechecks or conversion webhooks references CanaryConfig, so no migration machinery is needed beyond the shim.
End state (a future phase, kept out of this RFC's non-goals): fold the rollout policy into the alias itself — `FunctionAliasSpec.Rollout {Step, Interval, FailureThreshold}` — so the canary controller's loop drives `Weight` from the alias's own spec and `CanaryConfig` becomes a deprecated alias-generating shim; at that point the CRD can be frozen (webhook warning on create), then removed a release later.
Notably CanaryConfig today has essentially no server-side validation (no webhook, minimal CRD schema — guardrails live in the reconciler and CLI), so alias-native rollout with webhook validation is a strict UX upgrade, not just parity.

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

### CI/CD & declarative spec experience (added 2026-07-22)

The version/alias model is only a CI/CD upgrade if a pipeline can drive it without scraping output or keeping external state; five design points make it spec-friendly:

- **Aliases are spec-first; versions are controller-owned.**
  `fission spec` gains an `applyFunctionAliases` closure so aliases live in the Git repo, carry the deployment-UID annotation, and prune normally; `FunctionVersion`s are never spec-declared (see the resolved pruning question below) — the repo declares *pointers*, the cluster owns *history*.
- **The GitOps naming tension is solved by digest pinning, not by guessing sequence numbers.**
  A Git repo cannot know the cluster-assigned `orders-v12` name at commit time.
  `FunctionAliasSpec` therefore also accepts `packageDigest` as the target selector (mutually exclusive with `version`): the pipeline computes the OCI digest at build time (RFC-0001/0012 — content-addressed already), commits `prod → sha256:…`, and the alias controller resolves digest → the FunctionVersion that pinned it.
  Name-based pinning stays for imperative use; digest-based pinning is the declarative path.
- **Publish is idempotent, and its output is machine-readable.**
  Retried pipelines must not mint duplicate versions: `fn publish` (and auto-publish) is a no-op returning the existing version when the live spec already equals the newest snapshot (V4's classifier gives this for free — `classify(spec, spec) = false`).
  `fission fn publish -o name` / `-o json` emits the version name/digest on stdout so a pipeline captures it in one line and feeds the alias step without parsing human text.
- **Alias status carries its own history, so rollback needs no external bookkeeping.**
  `FunctionAliasStatus.History` records the last K targets with timestamps (bounded ring); `fn rollback` reads it, pipelines audit it, and `kubectl get functionalias` shows current + previous at a glance.
  This is also what makes rollback safe to run from CI: the pipeline does not need to remember what it deployed last.
- **One waitable gate per step.**
  `fn publish --wait` returns only after the referenced package is `succeeded` and the version exists; `alias update --wait` / `fn rollback --wait` return after the repoint has propagated to serving routes (first 2xx via the new target, bounded by the < 1s repoint-latency bar in the test plan).
  A pipeline is then three synchronous commands — apply, publish, repoint — each with a meaningful exit code, and the progressive-shift variant swaps the repoint for an `alias update --weight` sequence (or, with phase 6, a single rollout-policy alias the platform drives).

Environment promotion falls out of the same primitives: staging → prod is repointing a second alias at the *same* version (same digest, same artifact, no rebuild) — the property CI/CD teams actually want from "promote what you tested".

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
2. Resolver + router: `name:alias` references, weighted alias via the HandlerRef path, the `/fission-function/<ns>/<name>:<alias>` internal-listener route grammar (covers MQ/timer/kubewatcher/MCP publishers uniformly), the deterministic sticky-key version pick for weighted aliases, async-envelope version stamping for alias-referenced enqueues, `fn rollback`; integration tests.
3. Executor version-keyed caching (including migrating the remaining `CacheKeyUR` caches — `byFunction`, `functionEnv`, the router dispatch dedup key — to UG/version keying), the per-version endpoint-index dimension (per-version Services or a versioned `FnKey`), and the **reaper retention change** (retain warm pods for alias-referenced non-latest generations, not just the latest — the `ListAvailableValue`/`latestFuncGen` policy); rollback-warmth integration test asserting no cold start on rollback to an alias-retained version.
4. Auto-publish on runtime-affecting update + retention GC + environment observation at publish (`EnvObservedGeneration`/`EnvRuntimeImage`), env-drift surfacing (`fn versions`, alias `EnvDrift` condition, rollback warning), and `fission env impact`.
5. Canary-on-aliases shim + deprecation docs.
6. (Optional, post-deprecation-window) Alias-native rollout policy (`FunctionAliasSpec.Rollout`) + CanaryConfig freeze — see CanaryConfig absorption.

## Verification / test plan

- Webhook: immutability rejection matrix; alias→missing-version rejection; runtime-affecting-field classifier table test.
- Integration: publish → alias → invoke-by-alias; weighted 90/10 split distribution assertion (statistical bounds, reusing the canary test's tolerance approach); rollback repoint latency (< 1s to first correct response); warm-rollback — after the phase-3 reaper-retention change, an alias-retained older version serves rollback with **no** cold start observed (this test is meaningless before that change, since the reaper drains non-latest pools).
  Respect the known coalesced-specialization race: assert on served responses, not live Deployment specs.
- Route-table: alias weight tick and repoint produce zero `fission_router_route_resync_drift_total` and no materializer runs. This test must **assert** the metric (the "bar" is documentation-only today — see Verification), turning the RFC-0013 convention into an enforced gate for alias ops.
- GC: `aliasgc.tla` (V2/V3 model) plus an envtest retain-N sweep that never deletes an aliased version and survives the modeled alias-create-vs-delete interleaving.
- Shipped-feature interactions (2026-07-22 additions): sticky-session stability under a weighted split — a fixed sticky key lands on one version for the lifetime of a 90/10 split, and a weight change migrates only hash-crossing sessions; async retry determinism — enqueue via alias, roll back, assert the retry ran the enqueue-time version (envelope stamp) while a bare-name enqueue tracks live; keyed-state continuity — rollback preserves state (same keyspace serves both versions).
- CI/CD surface: publish idempotence (double `fn publish` with unchanged spec yields one version), digest-pinned alias resolution (`packageDigest` → correct version, unknown digest → admission rejection), `--wait` exit-code contracts, and alias `History` round-trip through `fn rollback`.
- Dependency axes: `pkg update` on a shared package mints one version per dependent function, each recording the new digest; env update flips the alias `EnvDrift` condition and `fn rollback` to a drifted target prints the warning (assert on CLI output + condition, not pod internals).

## Open questions

- ~~Whether `mqtrigger`/`timer`/`kubewatcher` references support aliases in v1 or phase 2 ships HTTP-only first.~~ **Resolved by review (2026-07-22):** the premise "the resolver is shared" is false — MQ (`kafka/consumer.go`, `statestore/subscription.go`), timer (`timer.go`), and kubewatcher (`kubewatcher.go`) never resolve specs at all; they hard-reject non-`name` reference types and just build `UrlForFunction(name, ns)` URLs POSTed to the router's internal `/fission-function/` listener, where the router does the real resolution.
  So alias support for every non-HTTP trigger reduces to ONE change: the router registers `/fission-function/<ns>/<name>:<alias>` routes (driven by the same FunctionAlias informer phase 2 adds), and each publisher appends `:<alias>` when its `FunctionReference` carries one.
  Weighted aliases then work uniformly on all trigger types for free, because the weighted pick happens router-side — no per-publisher weighted-resolution code exists or is needed.
  MCP (`pkg/mcp/proxy.go`) rides the same URL path.
  Decision: all trigger types in v1, via the URL grammar, not via per-publisher resolvers.
- Whether runtime-affecting Environment changes (image/builder) should fan out auto-publish to opted-in dependent functions, so env upgrades appear in each function's version history with a fresh env observation — versus observation-plus-drift-warning only (as drafted); fan-out gives auditability but mints versions the user never asked for, and a busy shared env multiplies that across every dependent.
- Whether a workflow run (RFC-0022) should resolve alias→version at RunStarted and pin all step targets for the run's lifetime — version-consistent runs vs live-tracking steps changes replay semantics; deferred to a workflows follow-up, but `WorkflowState.Function` must not grow alias fields until it is decided.
- Auto-publish default: opt-in (`versioning.mode: auto` required, as drafted) vs on-by-default with retain-N; opt-in first, flip later with data.
- ~~Interaction with `fission spec apply` pruning when versions are auto-created objects the spec never declared.~~ **Resolved by review:** `spec apply --delete` only prunes objects carrying the spec's `FISSION_DEPLOYMENT_UID_KEY` annotation and absent from the desired set (`ownedByDeployment`, `pkg/fission-cli/cmd/spec/resourcetype.go`). Controller-created `FunctionVersion`s carry no such annotation, so they are never pruned. The design requirement is therefore simply: the auto-publish controller must **not** stamp the deployment-UID annotation on versions it creates (and there is no `applyFunctionVersions` closure, so versions are not a spec-managed kind). Aliases, if made spec-first for GitOps, *would* get an `applyFunctionAliases` closure and be pruned normally — which is the intended GitOps behavior.

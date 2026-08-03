# RFC-0030: Per-function environment variables and flexible Secret/ConfigMap mapping

- Status: Partially implemented ([#3630](https://github.com/fission/fission/pull/3630)/[#3633](https://github.com/fission/fission/pull/3633) phase 1, [#3638](https://github.com/fission/fission/pull/3638) phases 3+4; merged 2026-07-31–2026-08-02) — **phase 1**: `FunctionSpec` literal env vars, `valueFrom` key references into Secrets/ConfigMaps, and `envFrom` whole-object projection with prefix, injected on **newdeploy** and **container** and propagated on update. **Phase 3**: `MountPath` for secret/configmap file projection with semantics identical across all three executors — the fetcher redirects for poolmgr/newdeploy, the container executor reproduces the same layout with native projected volumes — closing [#2407](https://github.com/fission/fission/issues/2407). **Phase 4**: `fission function run` local parity via `--secret-mount NAME=PATH`.
  **Phase 2 (poolmgr env vars) is NOT shipped**, which is why [#1545](https://github.com/fission/fission/issues/1545) and [#1556](https://github.com/fission/fission/issues/1556) stay open: env vars are rejected at admission for poolmgr, the default executor. It needs a coordinated `fission/environments` release per language — the executor adds `envVars` to the specialize request and the runtime **acknowledges what it applied**. The acknowledgment is load-bearing: `/v2/specialize` returns 200 with an empty body on every runtime today, so its absence unambiguously means "old runtime" and a function declaring env vars against one is refused **naming the environment**. An old runtime silently ignoring `envVars` would yield an empty-but-valid value — the exact silent misconfiguration this RFC exists to prevent. Both wire additions are additive; rollout is per-language (python and node first), not a flag day. Per-envset warm pools were explicitly rejected: they would deliver this with no runtime change but fragment the pool and cost a cold start per distinct env set.
- Previous: Proposed (draft for discussion)
- Tracking issue: [#3628](https://github.com/fission/fission/issues/3628) (members: [#1545](https://github.com/fission/fission/issues/1545) env vars from configmaps/secrets, [#1556](https://github.com/fission/fission/issues/1556) flexible secret/configmap mapping, [#2407](https://github.com/fission/fission/issues/2407) custom secret/configMap file path; [#3241](https://github.com/fission/fission/issues/3241) insecure `secretKeyRef` exposure informs the security section. **Not** #1721 — that asks for `fission env update` to change an Environment's CPU/memory, unrelated to function env vars)
- Supersedes: —
- Targets: newdeploy/container phases next minor; poolmgr phase gated on env-image releases
- Requires: nothing hard in-repo; the poolmgr phase requires coordinated `fission/environments` runtime releases (capability-gated, see Design §3); composes with RFC-0023's specialize-payload precedent, RFC-0025 auto-publish, RFC-0026 make-before-break, and RFC-0018 run-local's existing `-e`/`--env-from` local bridges.

## Summary

Give a Function first-class, per-function configuration: literal environment variables, key-level references into Secrets/ConfigMaps (`valueFrom`), whole-object projection (`envFrom`, with prefix), and — for the file path that already exists — a configurable mount path.
Today the only per-function configuration channel is a pair of name-only references (`FunctionSpec.Secrets`/`ConfigMaps`) whose meaning *differs by executor*: poolmgr **and newdeploy** both run a fetcher that writes them as files under a fixed `/secrets/<ns>/<name>` path, while the container executor — the one executor with no fetcher — injects them as whole-object `EnvFrom` (`ConvertConfigSecrets`) — and there is no way to set a plain `DATABASE_URL=...` at all without owning the Environment's podspec.
This is table-stakes 12-factor DX (four separate issues since 2020 ask for pieces of it), it is the first thing every evaluation hits, and the design below makes the semantics **identical across executors** instead of adding a fifth per-executor behavior.

## Motivation

- A 12-factor app expects configuration in the environment (`DATABASE_URL`, `LOG_LEVEL`), not a file at a platform-invented path the code must know to parse; every mainstream FaaS (Lambda, Cloud Functions, OpenFaaS) ships per-function env vars, and their absence reads as a platform gap in the first ten minutes of an evaluation (#1545).
- The existing references are all-or-nothing: no key selection, no renaming, no prefixing (#1556), and the file path is hardcoded (`fetcher.go` writes `sharedSecretPath/<ns>/<name>`), so code written for any other platform's layout must change (#2407).
- The per-executor divergence is worse than a gap — it is a trap: moving a function between the container executor and poolmgr/newdeploy silently changes its configuration from env vars to files.
- The workaround — env vars via the Environment's `Runtime.PodSpec` merge — is per-*environment*, so secrets leak across every function sharing the environment, and it is unavailable to anyone not already deep in podspec surgery.
- Every ingredient exists: the container executor already builds `EnvFromSource` from the references (`pkg/executor/util/util.go` `ConvertConfigSecrets`), the fetcher already fetches Secrets/ConfigMaps with per-namespace RBAC at specialize time, the specialize payload already carries per-function extensions (RFC-0023's `StateKeyspace`), and run-local already exposes `-e`/`--env-from` locally — cluster-side is the missing half.

## Goals

- `FunctionSpec` carries literal env vars, key-level Secret/ConfigMap references, and whole-object projection with optional prefix — with **identical observable semantics on poolmgr, newdeploy, and container executors**.
- Configurable mount path for the existing file-projection mode (#2407), defaulting to the current path.
- CLI parity: `fission fn create/update --env-var`, `--env-from-secret`, `--env-from-configmap`, mirrored by `fission spec` and the run-local loop (RFC-0018).
- Changes propagate: an env/reference edit is a runtime-affecting update (generation bump, pod recycle/roll); rotation of a *referenced* Secret/ConfigMap recycles exactly the functions that reference it, via the existing watcher machinery.
- Admission-time safety: unsupported combinations (see Non-goals and §3's capability gate) are rejected with a reason, never silently dropped.

## Non-goals

- `fieldRef`/`resourceFieldRef` (downward API) env sources in v1 — poolmgr's specialize-time injection cannot honor pod-level field refs portably; explicitly deferred.
- Cross-namespace references — the same-namespace rule is enforced today (webhook + `ConvertConfigSecrets`) and stays; multi-namespace tenancy owns any future change.
- Per-key file projection (`items:`-style KeyToPath) — `MountPath` covers the filed ask (#2407); key-to-path ships later if demand appears.
- Hot-reload of env vars into a *running* process — process environment is set at specialization; changes recycle pods (matching every other FaaS).
- Encrypting or brokering secret values — Kubernetes Secrets remain the source of truth; RFC-0023's keyed state and the statestore are unrelated channels.

## Design

### 1. CRD surface

```go
// FunctionSpec gains (all optional; absent = today's behavior exactly):
Env     []apiv1.EnvVar        `json:"env,omitempty"`     // literals + valueFrom (secretKeyRef/configMapKeyRef only, webhook-enforced)
EnvFrom []apiv1.EnvFromSource `json:"envFrom,omitempty"` // whole Secret/ConfigMap, optional Prefix

// The existing references gain an optional path (files mode, #2407):
SecretReference    { Namespace, Name string; MountPath string `json:"mountPath,omitempty"` }
ConfigMapReference { Namespace, Name string; MountPath string `json:"mountPath,omitempty"` }
```

Using the upstream `corev1` types keeps `kubectl explain`, GitOps schemas (RFC-0029), and user intuition aligned; the webhook narrows them (only `secretKeyRef`/`configMapKeyRef` in `valueFrom`, same-namespace by construction since `LocalObjectReference` has no namespace, no duplicate names, `MountPath` constrained per §4).

**Reserved names are denied at admission.** There is no env-name validation in the codebase today (`validation.go`, `podspec_safety.go` cover host namespaces, SA override, hostPath and SecurityContext — not env), and the platform sets `RESOURCE_VERSION_COUNT` plus RFC-0023's `FISSION_STATE_URL`/`FISSION_STATE_TOKEN_PATH` (`pkg/executor/util/stateenv.go`) on the user container.
The denylist has two tiers, for two different reasons:

- **Platform contract** — `FISSION_*` and `RESOURCE_VERSION_COUNT`, the vars the user container actually receives. The load-bearing case is `FISSION_STATE_URL`: redirecting it exfiltrates the RFC-0023 state token to an attacker-chosen endpoint. (`FISSION_STATE_TOKEN_PATH` redirection fails closed — the SDK reads a file the user already controls — so it is denied for hygiene, not danger.)
- **Interpreter/proxy hijack** — `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`, `LD_PRELOAD`, `NODE_OPTIONS`, `PYTHONPATH`. These are the vars that make the §3 cross-pod injection scenario a *code-execution and traffic-interception* primitive rather than a configuration nuisance, so naming the threat without denying them would be incoherent. Denying them is also the conservative default: a function legitimately needing a proxy sets it in its own code or environment image.

`OTEL_*` is deliberately **not** reserved: OTel env is applied to the fetcher sidecar only, so there is no platform value on the user container to protect, and reserving it would block a legitimate user knob.

**Enforcement happens at injection, never at admission — and the webhook gains no new permissions.** A webhook can reject reserved names in literal `Env`, but `EnvFrom`-expanded names are the referenced object's **data keys**: unknowable at admission, mutable afterwards (create a benign ConfigMap, get admitted, then add a `FISSION_STATE_URL` key), and readable only if the webhook ServiceAccount — which today reads `fission.io` CRDs and nothing else — were granted Secret read across every Fission namespace, cluster-wide under dynamic tenancy. That would turn a validating webhook into a cluster-wide secret reader: a *new* escalation surface created by the fix. Rejected.

Instead, the guarantee comes from injection order and resolution-time filtering:

- **newdeploy / container**: the platform emits its own vars **after** user `Env`/`EnvFrom` in the container spec, so the kubelet's last-wins duplicate resolution makes platform names unconditionally win. No validation required for the guarantee.
- **poolmgr**: the fetcher **drops reserved keys while expanding `EnvFrom`** into the resolved map, logging the dropped name (never the value). This matters concretely here — the runtime applies the map into an environment that already contains `StateAPIEnvVars`, so without the filter an `EnvFrom`-supplied key really would override them.
- The webhook keeps the literal-`Env` denylist purely as fast, friendly feedback.

Same two-layer shape as `MountPath`: admission for UX, runtime for the guarantee.

Auth material — `FISSION_INTERNAL_AUTH_SECRET`, `FISSION_FETCHER_KEY`, `FISSION_STORAGE_KEY` — lands on the *fetcher sidecar* only, never the user container, so it is out of reach of these fields by construction; the denylist protects the user-container contract, not the keys.

Merge precedence over *user-declared* keys, defined once and tested: environment podspec `Env`/`EnvFrom` (the existing merge allowlist) < function `EnvFrom` (in list order, k8s semantics: later keys win) < function `Env` literals/`valueFrom` — the function always wins over its environment, matching user expectation and k8s container semantics.
**Platform-injected vars sit above all of them and are not part of this ordering** — they are protected by the denylist below, which is what makes the ordering safe to state simply. This distinction is load-bearing because the two executor families genuinely differ: on newdeploy/container the kubelet gives container `env` precedence over `envFrom`, whereas on poolmgr the runtime applies the resolved map into an already-populated process environment. Identical *observable* semantics are therefore guaranteed for the user's declared keys, which is what E1 asserts — not for keys that collide with platform names, which are rejected outright.
Note this is a **deliberate trade**: `MergeContainer` currently errors on duplicate env names (`pkg/executor/util/merge.go`), and "function wins" requires appending after that merge. The RFC trades that accidental guard for the explicit denylist above; the implementation must state where user `Env` is appended relative to `MergeContainer` so the trade is visible in review rather than silently lost.

### 2. newdeploy and container executors — native injection

Per-function pods make this the easy half: the executor sets `Env`/`EnvFrom` directly on the function container in the Deployment spec.

- The container executor's existing behavior — whole-object `EnvFrom` synthesized from the name-only references — is **frozen as legacy** and preserved byte-for-byte when the new fields are absent; when `EnvFrom` is present it is authoritative and the legacy synthesis is skipped (one source of truth, no double-injection).
- Rolling on change: `ReferencedResourcesRVSum` (already stamped into the pod template so Deployments roll when a referenced object changes) extends to objects referenced via `Env[].ValueFrom` and `EnvFrom`, so secret rotation rolls exactly the referencing Deployments.

### 3. poolmgr — specialize-time injection behind a capability gate

Generic pool pods exist before any function is assigned, so per-function values cannot ride the pod spec; they ride specialization, the same seam RFC-0023 used for the state token:

**The wire contract, stated precisely — this is the security-critical part of the RFC.**
`FunctionLoadRequest` is built by the **executor** (`pkg/fetcher/config/config.go`) and, on the newdeploy path, is marshalled into the fetcher container's `command` args (`AddSpecializingFetcherToPodSpec`), i.e. into the Deployment pod template — readable by anyone with `get pods/deployments` in the namespace, and stored in etcd.
Therefore:

- The **executor-side payload carries references only** (`EnvSources`: the reference list, non-secret, useful for observability). It never carries a resolved value.
- The **fetcher populates resolved values pod-locally**, on a distinct type that the pod-args serializer cannot reach, immediately before its `127.0.0.1` `/v2/specialize` POST. The fetcher already fetches these objects under its per-namespace RBAC (namespaced Role in static tenancy, namespaced RoleBinding in dynamic — `_function-access-role.tpl`, `pkg/tenant/provision.go`), so resolution is same-namespace by RBAC construction as well as by type.
  Two implementation pins, or the type boundary leaks: the resolved type must **not be reachable from `FunctionSpecializeRequest`** (the pod-args marshal serializes the whole parent, so a field hung off `LoadReq` is exposed through it); and the run-local loop constructs its own `FunctionLoadRequest`s (`pkg/fission-cli/cmd/function/run.go`, `run_local.go`) that must stay on the reference-only type. E3's grep gate covers **five** serialization/construction sites: the pod-args marshal (`pkg/fetcher/config`), the fetcher client's signed POST (`pkg/fetcher/client`), the B-direct load-only marshal (`poolmgr/oci_specialize.go`), and the two run-local constructors.
  A third pin for phase 2: the fetcher's specialize retry loop reuses a single `bytes.Reader` across attempts. That is safe only because retries fire on dial errors where the body was never consumed — once the payload carries env references, a retry after a partially-consumed body would silently mis-specialize with an empty payload. Rebuild the reader per attempt.
- The env runtime applies the map to the process environment **before importing user code** (valid precisely because specialization precedes user-code load), and reports support via a bumped env interface version.

**The specialize channel must be authenticated before `Env` rides it.**
The env runtime's port 8888 is reachable across the pod network, and the executor's own OCI Path-B "B-direct" call (`pkg/executor/executortype/poolmgr/oci_specialize.go`) POSTs `/v2/specialize` over plain HTTP **with no HMAC signer** — unlike the fetcher's `:8000/specialize`, which is `ServiceFetcher`-verified. `networkPolicy.enabled` defaults to `false`, and the executor's one-shot check is pod-*selection* bookkeeping (`gp_pod.go`), not enforcement.
Today an attacker replaying that endpoint can only reload code the victim already owns; adding `Env` would upgrade it to cross-tenant credential theft (inject `FISSION_STATE_URL`/`HTTPS_PROXY`, exfiltrate the victim's RFC-0023 token). So:

- **Path-B eligibility must account for the new fields**: `getFunctionOCIPool` computes `fetcherVariant` from `len(Secrets) > 0 || len(ConfigMaps) > 0` (`oci.go`), and image-volume delivery is on by default — a function using *only* `Env`/`EnvFrom` would land on a **fetcherless** pool whose only specialize path is that unsigned cross-network POST. The predicate extends to `len(Env) > 0 || len(EnvFrom) > 0 ⇒ fetcherVariant = true`. Resolved values must never be an option for the executor to send.
- **The poolmgr phase declares `networkPolicy.enabled: true` a prerequisite — and the policy must actually authenticate the executor.** The `functionpods` policy admits 8888 from `svc: executor` / `svc: router` (correctly keeping the RFC-0012 Path B direct-specialize call working), but both `from` rules paired the pod label with `namespaceSelector: {}` — **every** namespace. Any tenant who can create a pod in their own namespace and label it `svc: executor` regains full access to every function pod's 8888 and 8000. **The fix is in flight as PR #3629** (release-namespace `kubernetes.io/metadata.name` selector, AND-shape, both rules); this phase depends on that PR having merged rather than re-authoring it. For reference, the shape it lands:
  ```yaml
  - from:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: {{ .Release.Namespace }}
        podSelector:
          matchLabels:
            svc: executor
  ```
  (same shape for the router rule — note the two selectors must be entries of **one** `from` element to AND rather than OR).
- **Dynamic/cluster tenancy gap, which decides the phase's scope**: the policy renders into a single namespace (`functionNamespace`/`defaultNamespace`), while under dynamic/cluster tenancy function pods live in runtime-onboarded namespaces where the tenant controller provisions ServiceAccounts, RoleBindings and derived-key Secrets — **but no NetworkPolicy**. The prerequisite is therefore unenforceable in exactly the multi-tenant mode whose threat model motivates it. Either the tenant controller provisions this policy per onboarded namespace (preferred; it is the same provisioning path), or §3 states that poolmgr `Env`/`EnvFrom` is unsupported under dynamic/cluster tenancy until it does.
- **One-shot specialize already exists and is the backstop, not the control.** Published env runtimes reject a second `/v2/specialize` on an already-specialized container (RFC-0018 recorded this as resolved: the node env answers `Not a generic container`), so no cross-repo change is needed and E7's replay clause is a regression test, not new work. It is also compatible with the existing retry paths, which fire only on dial/connection-refused errors and never after an HTTP response.
  What one-shot does **not** close: an attacker specializing a **still-generic** pool pod before the executor does. First-writer-wins is not authentication — it merely makes the race winnable exactly once, converting a poisoning into a denial of the victim's specialize. With `Env` in the payload, winning it means a pod running attacker-chosen configuration inside the function namespace.
  So the ordering is explicit: **the NetworkPolicy (as corrected above) is the control; one-shot is the backstop.**

**Capability gating — admission alone is not sufficient, and the enforcement point is the executor.**
A Function using `Env`/`EnvFrom` on an environment whose runtime predates support is rejected at admission, but that check **fails open**: the RFC-0023 infinite-env precedent (`pkg/webhook/function.go`) fails open on environment-lookup miss and cannot see an Environment mutated *after* the function was admitted.
RFC-0023's authoritative backstop is `stateKeyspace`, and locating it correctly matters: `pkg/fetcher/config` is imported by the **executor** (and buildermgr), not by `cmd/fetcher` — so that enforcement runs executor-side, where the live `*fv1.Environment` is already in hand when the specialize request is built.
This RFC mirrors it there: **the executor refuses to build a specialize request** carrying `Env`/`EnvFrom` for an environment that does not support it, failing the specialization loudly instead of loading a function with a silently empty `DATABASE_URL`. That closes the post-admission-mutation case with no new wire contract, which is why it — not a fetcher-side check — is the phase-2 deliverable.
A fetcher-side check is *not* implementable on today's contract: the fetcher receives only `FunctionLoadRequest`, whose sole environment signal is `EnvVersion`, and there is no runtime capability endpoint to query. If the explicit capability advertisement in Open questions lands later, a fetcher-side check becomes possible as defense in depth — but nothing in this RFC depends on it.
Signal quality: `Environment.Spec.Version` is user-declared, bounded 1..3, and **immutable** (CEL `self == oldSelf`), so it cannot be mutated post-admission — but it also cannot express "this image supports env injection", and v1 environments receive an empty specialize body so the payload cannot ride v1 at all. Hence the explicit advertisement question below.

- **`allowedFunctionsPerContainer: infinite` environments are rejected** for these fields, same rule and same reason as RFC-0023's token exclusion: many functions share one process, so per-function process env cannot isolate.
- Upgrade skew note: warm specialized pods keep their old fetcher (RFC-0028 exempts them from image convergence), but they also never re-specialize, so they can never receive-and-drop the new payload; generic pool pods roll to the new fetcher image via the pool Deployment on upgrade, so every *new* specialization has a capable fetcher.

### 4. Files mode and `MountPath` (#2407)

`MountPath`, when set, redirects the fetcher's existing per-object write from `sharedSecretPath/<ns>/<name>` to the given path; absent, the legacy path is written, unchanged.
Because poolmgr **and newdeploy** both deliver these files via the fetcher, both take the same redirect — only the container executor (no fetcher) translates `MountPath` to a native volume mount.

Two constraints the implementation must honor, neither of which is an admission-only rule:

- **Scope**: generic pool pods share exactly three fixed emptyDirs between fetcher and runtime container — `/userfunc`, `/secrets`, `/configs` — and the volume set is frozen at pool creation, so the fetcher physically cannot materialize a file at an arbitrary path like `/etc/app/`. `MountPath` is therefore **relative to the `/secrets` and `/configs` roots**, not a free absolute path; #2407's literal ask (any absolute path) is satisfiable only on the container executor, and the RFC deliberately chooses cross-executor consistency over that. Docs must say so plainly.
- **Reserved paths and inter-object shadowing**: the final path segment is the Secret/ConfigMap **data key**, written verbatim (`writeSecretOrConfigMap`), and keys are mutable *after* admission — so a webhook that only sees `MountPath` cannot constrain them.
  The platform-file case is already safe and should not be overstated: the secret writer opens an `os.Root` at the per-object directory under `/secrets`, so it structurally cannot address `/userfunc/.fission-state-token` or the code mount — disjoint roots plus `os.Root` confinement, not incidental ordering. Keeping `MountPath` out of `/userfunc` (above) preserves that property rather than creating it.
  The genuine residual risk is **inter-object key shadowing**: two references pointing at the same `MountPath` where a later-added key in one object overwrites a file the other wrote. The webhook rejects duplicate `MountPath` values across a function's references (the part it *can* see), and the fetcher refuses to overwrite a file it did not write in this specialization pass (the part it cannot). The existing `RootJoin`/`RootMkdirAll` traversal guards stay.
  The container executor's native-volume-mount variant needs the same care from a different angle: `ValidatePodSpecSafety`/`ValidateContainerSafety` have no `mountPath` rules today, so a user-chosen path materialized as a native volumeMount could shadow `/var/run/secrets/kubernetes.io/serviceaccount` or anything in the user's own image. Self-scoped, hence low severity — but resolved here rather than left open: container-executor `MountPath` is constrained to the same `/secrets` and `/configs` roots (which §4 already chooses for cross-executor consistency), the mount is constructed by the executor from the validated value rather than passed through from podspec input, and `ValidateContainerSafety` gains a reserved-prefix `mountPath` check.
- **`allowedFunctionsPerContainer: infinite` is excluded from `MountPath` too**, for the same reason §3 excludes it from `Env`/`EnvFrom`: one shared `/secrets` tree serves many functions, so one function's redirect can overwrite another's files. (No isolation exists in that mode today, so this is consistency rather than new exposure — but it belongs in the same place as the other exclusion.)
  This matters doubly under RFC-0029's instance-class webhook partition, where a namespace could fall outside every release's selector and skip validation entirely.

### 5. Change propagation

- Spec edits (`Env`, `EnvFrom`, `MountPath`, references) are runtime-affecting: generation bump → poolmgr re-specializes on new-generation pods (the poolmgr reconciler needs no diff extension — any update with `old != nil` refreshes function pods regardless of which field changed; note it is no longer diff-free in general, RFC-0026 added an old/new `ProvisionedConcurrency` branch, but that branch is orthogonal), newdeploy/container roll via the RVSum stamp; RFC-0026 provisioned floors make-before-break as shipped.
- Alias-pinned pods: the cms rotation path (`RefreshFuncPods`) recycles **every generation's** pods, while the spec-edit path threads RFC-0025 alias retention (`refreshFuncPodsFiltered`). Phase 1 adopts the cms semantics for `Env[].valueFrom`/`EnvFrom` rotation — a rotated Secret recycles alias-pinned warm pods too, because a version pins the *reference*, not the resolved value (per the snapshot-semantics note below), and serving stale credentials to a pinned version is the failure mode this section exists to prevent.
- **Referenced-object content rotation must ship in phase 1, not phase 3.** The chain is: the `pkg/executor/cms` Secret/ConfigMap watcher calls `RefreshFuncPods`, and `ReferencedResourcesRVSum` is recomputed *only inside that refresh* — its function index and the RVSum signature both read `fn.Spec.Secrets`/`ConfigMaps` only.
  Kubernetes does not refresh `env`/`envFrom` in a running container, so until the watcher index **and** the RVSum signature both cover `Env[].valueFrom`/`EnvFrom`, rotating a Secret referenced only via the new fields propagates to **nothing** — silently stale credentials, which is worse than the gap this RFC closes. The index/RVSum extension therefore moves into phase 1 alongside the fields themselves; E4 is unsatisfiable before it lands.
  The known tenancy gap (#3504 — the watcher doesn't follow runtime-onboarded namespaces) is inherited, not widened, and noted on that issue.
- **RFC-0025 classifier extension, phase 1**: the auto-publish runtime-affecting classifier is an *enumerated per-field switch* (`pkg/versioning/classifier.go`) whose `default` returns false — new top-level `Env`/`EnvFrom` fields would be treated as **not** runtime-affecting and mint no version until explicitly added. (`MountPath` is already covered, since it lives inside `SecretReference`/`ConfigMapReference`, which the classifier compares by `DeepEqual`.) The classifier's coverage test will force the decision; phase 1 names it so it is a choice, not a discovery.
- RFC-0025 snapshot semantics, stated in docs: auto-publish snapshots the *references and literals* (the spec), not resolved secret values — a `FunctionVersion` pins which secret is read, not what it contained at publish time; value pinning is deliberately out (secrets rotate; versions are specs).

### 6. CLI and local loop (RFC-0018)

**Flag naming is constrained by two existing flags** — the RFC must not reuse either:

- `--env` already means the **Environment name** on `fn create`/`fn update` (`FnEnvironmentName` in `pkg/fission-cli/flag/key/key.go`). Per-function variables therefore use **`--env-var KEY=VAL` / `-e`**, matching run-local's existing precedent.
- `--env-from` already exists on `fission fn run-local`/`run-container` meaning a KEY=VALUE **file**. The new flags are `--env-from-secret` / `--env-from-configmap`, which share the prefix but not the semantics; the UX review (Open questions) should confirm this is tolerable or rename.
  (`-e` is free on `fn create`/`fn update` — it is bound only on the run-local family — so the short form is available.)

Surface: `fission fn create/update --env-var KEY=VAL --env-from-secret name[/key][:ENV] --env-from-configmap name[/key][:ENV] --secret name[:mountPath] --configmap name[:mountPath]` — update is a normal spec update (generation bump, recycle).
`fission function run-local` maps the same flags onto its existing local `Env` bridge, so local and cluster behavior match by construction.

## Backward compatibility

- All new fields are optional; a Function without them behaves byte-identically on every executor, including the container executor's legacy whole-object `EnvFrom` synthesis and poolmgr's legacy file paths.
- The wire change is additive JSON on the pod-local specialize payload; old env runtimes never see the new fields for old functions, and new-field functions are admission-blocked from old runtimes rather than degraded.
- No CLI flag or spec field changes meaning — specifically, `--env` keeps meaning the Environment name (new variables use `--env-var`/`-e`), and `--secret`/`--configmap` keep today's semantics with an optional `:mountPath` suffix.
- The reserved-name denylist and `MountPath` root constraint apply only to the **new** fields; no existing function can be made invalid by them, since neither field exists on any current object.
- The `networkPolicy.enabled: true` prerequisite (§3) gates the **poolmgr phase only** — newdeploy/container injection (phase 1) has no such dependency, and existing installs with the policy disabled keep working unchanged until they adopt poolmgr env injection.
- The same-namespace rule, the merge allowlist for environment podspecs, and `ENV_DISABLE_OWNER_REFERENCES`-style gates are untouched.
- Secrets-as-env-vars carries the standard Kubernetes caveat (visible to `kubectl exec` env and crash handlers); docs keep files mode as the recommendation for high-sensitivity material — an informed default, not a behavior change.

## Rollout phases (bisectable)

1. CRD fields + webhook validation (source narrowing, dedup, **reserved-name denylist**, capability + infinite-env gates stubbed to always-reject-poolmgr initially) + newdeploy/container native injection + **RVSum signature and `pkg/executor/cms` watcher-index extension to `valueFrom`/`EnvFrom`** + **RFC-0025 classifier extension** + CLI/spec surface — ships value on day one with zero env-image dependency, and with rotation working rather than silently stale.
2. poolmgr: references-only executor payload + fetcher-local resolution + **executor-side refuse-to-build** capability enforcement (per §3 — a fetcher-side check is not implementable on today's contract) + **Path-B `fetcherVariant` predicate extension**; `networkPolicy.enabled: true` prerequisite and the one-shot-specialize requirement land with the runtime contract; python/node support PRs in `fission/environments`; flip the poolmgr gate from always-reject to capability-check as runtimes release.
3. `MountPath` files mode (fetcher redirect for poolmgr/newdeploy, native mount for container) + fetcher-side reserved-path enforcement.
4. run-local flag parity + docs (12-factor guide, security caveats incl. the Kubernetes env-visibility note, RFC-0025 versioning note); remaining runtimes.

## Invariants & verification

- **E1** — a function with the same `Env`/`EnvFrom` spec observes identical values for **every key it declared** on poolmgr, newdeploy, and container executors. Scoped to declared keys by design: platform-injected vars are excluded from the comparison (they differ by executor and are denylist-protected, per §1). Integration-tested with a fixture that echoes its environment, one assertion per executor.
- **E2** — merge precedence (env podspec < `EnvFrom` in order < `Env`) is deterministic; `rapid` property: random layered sources ⇒ the spec'd winner wins.
- **E3** — no resolved secret value ever appears in executor logs, pod annotations/**args**, or Kubernetes events (grep-gate in the integration suite, the RFC-0023 discipline). The pod-args case is the load-bearing one: the newdeploy fetcher's `command` embeds the executor-built request, so this asserts the executor-side/fetcher-side type split of §3 actually holds.
- **E4** — rotating a referenced Secret recycles exactly the referencing functions within the watcher period — **including references made only via `Env[].valueFrom`/`EnvFrom`** — while non-referencing functions' pods are untouched (UID check). Requires the phase-1 watcher-index + RVSum extension; unsatisfiable without it.
- **E5** — a new-field Function against an old runtime is **never deployed-and-empty**: rejected at admission where the environment is visible, and refused by the **executor when it builds the specialize request** where it is not (the fail-open admission path is why both exist; §3 locates the authoritative check executor-side). Covered by the capability-gate unit matrix, a test mutating the Environment *after* function admission, plus an integration case pinning an old env image.
- **E6** — with no new fields set, pod specs and specialize payloads are byte-identical to pre-RFC output (golden tests over the executor pod-spec builders and fetcher config). Pin the JSON tags: every new payload field carries `omitempty`, or the golden bytes shift for every existing function.
- **E7** — `Env`/`EnvFrom` are honored only on an authenticated specialize channel. Three assertions, the third being the one that matters: (a) a function using them never lands on a fetcherless (Path-B direct) pool — unit test over the `fetcherVariant` predicate; (b) a second `/v2/specialize` to an already-specialized pod is rejected — integration regression test; (c) **a pod in another namespace cannot reach a function pod's 8888 or 8000 — tested twice, once plain and once carrying a hostile `svc: executor` label**, the labelled case being what catches the `namespaceSelector: {}` hole. The kind-ci profile already sets `networkPolicy.enabled: true` and kindnet enforces policy, so (c) is runnable in CI today.

## Open questions

- Capability signaling: `Environment.Spec.Version` is user-declared and CEL-bounded 1..3, so bumping it to 4 is both a weak signal and a change unrelated runtimes must claim. An explicit capability list the *runtime* advertises (queried once at specialize, cached) is cleaner and is what would make a **fetcher-side** check possible as defense in depth behind §3's authoritative executor-side refusal — but it is a new contract with `fission/environments`. Decide before phase 2 starts, since the advertisement shape feeds the runtime PRs.
- Does the one-shot-specialize requirement belong to this RFC or its own hardening PR? It fixes a pre-existing surface (`/v2/specialize` is replayable today), and gating this RFC on a cross-repo runtime change may be the wrong coupling — but shipping `Env` without it is what turns that surface into a credential-theft primitive.
- Should `EnvFrom` prefix-collision (two sources yielding the same final name) be an admission warning or an error? k8s silently last-wins; matching that is consistent but hides mistakes.
- Does `--env-from-secret name/key:ENV` syntax survive UX review, given it sits beside `fission fn run`'s existing `--env-from` (a KEY=VALUE file) with a shared prefix and different semantics — or should key selection be spec-file-only with the CLI covering the common whole-object case?
- Is there appetite to fold #3241's concern (auditing insecure `secretKeyRef` usage patterns) into this RFC's docs/security section, or track it separately?
- Should `MountPath` being `/secrets`-and-`/configs`-relative (rather than a free absolute path, per §4) be revisited by giving poolmgr pools a configurable extra volume — a bigger change that would satisfy #2407 literally on every executor?

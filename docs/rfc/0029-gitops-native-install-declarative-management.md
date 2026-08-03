# RFC-0029: GitOps-native install and declarative management

- Status: Implemented ([#3632](https://github.com/fission/fission/pull/3632) phase 1, [#3643](https://github.com/fission/fission/pull/3643) phases 3+5, [#3645](https://github.com/fission/fission/pull/3645)/[#3646](https://github.com/fission/fission/pull/3646) phase 2, [#3649](https://github.com/fission/fission/pull/3649) phase 1b/2/4 + follow-ups; merged 2026-07-31–2026-08-03) — `crds.mode` (hook default; the CRD bundle is **server-side-applied** by the pre-upgrade Job, because Helm's native `crds/` installs but never upgrades and four Fission CRDs exceed the 262KB client-side-apply limit) closing [#2382](https://github.com/fission/fission/issues/2382); `internalAuth.existingSecret` + in-cluster `autoGenerate` provisioning with per-tenancy namespace fan-out (a GitOps renderer's `helm template` leaves `lookup` empty, so a template-generated master is re-minted on every sync); `nameFormat: legacy|standard` defaulting to **standard** closing [#2906](https://github.com/fission/fission/issues/2906), with a fixed-name consumer inventory and a NetworkPolicy `svc:`-selector guard (a selector naming no workload does not error — it silently drops traffic); the GitOps golden path with content-hash rebuild keying re-keyed to package **content change** rather than build success (OCI/deploy-only packages never build); `fission spec apply` idempotency, so a reapply of an unchanged spec no longer rewrites package stamps and mints spurious RFC-0025 versions; and Terraform/Pulumi examples closing [#1907](https://github.com/fission/fission/issues/1907).
  Remaining members: [#2383](https://github.com/fission/fission/issues/2383) (inline file contents in `values.yaml` — **not** addressed; the Kafka MQT TLS path still uses chart-relative `.Files.Get` and hard-fails), [#2835](https://github.com/fission/fission/issues/2835) (naming half done; the remaining piece is an instance-class filter scoping each control-plane component's informers), [#2742](https://github.com/fission/fission/issues/2742) (declarative management covered; no operator built).
- Previous: Proposed (draft for discussion)
- Tracking issue: [#3626](https://github.com/fission/fission/issues/3626) (members: #2382, #2383, #2906, #2835, #1687, #2928, #1907, #2742, plus the `fission spec` batch #543/#551/#555/#623/#1037/#1491/#1574/#3230/#3296)
- Supersedes: —
- Targets: packaging phases next minor; golden path + spec contract the minor after
- Requires: RFC-0001/0012 OCI package delivery (shipped, default cold-start path) — the enabler that makes a fully declarative package possible; RFC-0028 ordering contract (CRDs-before-controllers) composes with the delivery mechanism chosen here.

## Summary

Make the declarative path the primary path: `helm install fission` (or one Argo/Flux sync) yields a complete working install **including CRDs**, and every Fission object — functions, packages, triggers, workflows, aliases — is manageable as plain CRDs from Git with no imperative side-channel.
`fission spec` is repositioned as a scaffolding/DX layer over those CRDs with three enforced guarantees (idempotent, complete, deterministic), instead of a parallel state-management system.
The strategic unlock is already shipped: with OCI-image packages (RFC-0001/0012) the archive-upload step — the one genuinely un-GitOps-able part of Fission — is optional, so this RFC is mostly *completion, packaging, and blessing*, not new architecture.
This targets the largest reaction cluster in the backlog (~40 reactions across #2382, #1907, #2383, #1687, #2906, #2742, #2928).

## Motivation

A 2026 platform team's first three commands are `helm install` via Argo, `kubectl apply` from a pipeline, and `terraform plan`.
Fission currently fails or frictions all three:

1. CRDs are installed out-of-band (`kubectl create -k crds/v1`), so a single-shot chart install breaks (#2382) and CRD versions skew silently against the chart.
2. The documented management path is the imperative CLI; `fission spec` exists but manages state through its own apply engine with known idempotency and lifecycle gaps (#3296, #1491, #543, #740's `--set`).
3. A Package's archive historically required an upload to storagesvc — un-declarable by construction — and package *updates* still do not converge from Git alone. The gap is narrower than it looks, and precisely locating it is what makes the fix small:
   - A freshly applied Package **does** build controller-side (`setInitialBuildStatus`, `pkg/buildermgr/package_reconciler.go`); only a *source change* needs the CLI's status→pending poke to re-enter the build queue (the enqueue predicate).
   - Re-stamping referencing Functions on build success **already exists** in buildermgr (`package_reconciler.go` writes `fn.Spec.Package.PackageRef.ResourceVersion` for every referencing Function before marking the build Succeeded) — so no new stamp controller is needed. The other two stamp sites are `spec apply` and `package update`.
   - The real hole is **OCI / deploy-only packages, which never build at all**: a non-empty `Spec.Deployment` settles at `BuildStatusNone` (`package_reconciler.go`), and `OCIArchive` is only legal on `PackageSpec.Deployment` (`pkg/apis/core/v1/types.go`). So on exactly the golden path this RFC blesses — digest-pinned OCI packages in Git — there is no build, therefore no build-success restamp, therefore no pod recycle (#2928, #2836).


4. Chart hygiene blocks common enterprise patterns: file contents can't be supplied via values (#2383), resource names don't respect `.Release.Name` (#2906), and two installs can't share a cluster (#2835).
5. There is no IaC surface at all (#1907, 12 reactions since 2021).

Each issue is old, popular, and individually small; together they are the difference between "evaluated Fission, install was weird, moved on" and adoption.

## Goals

- One-shot install: fresh cluster + `helm install` (or GitOps sync of the chart) → working Fission including CRDs, with CRD **upgrades** handled on `helm upgrade`.
- A blessed, documented **golden path**: functions/environments/triggers as plain CRDs in Git, packages as OCI images built in the user's CI, synced by Argo/Flux with correct health status — zero `fission` CLI invocations required in the pipeline.
- Content-driven convergence: changing a package's image digest or archive checksum in Git triggers rebuild/redeploy without CLI involvement (#2928).
- `fission spec` contract: **idempotent** (reapply of unchanged specs is a no-op), **complete** (removal from Git can remove from cluster), **deterministic** (spec-file defaulting identical to CLI/webhook defaulting), plus `--set` templating for the simple cases.
- Chart hygiene: values-supplied file contents, release-name prefixing, documented multi-instance coexistence.
- A supported IaC story for Terraform/Pulumi users.
- Published machine-readable schemas so offline validation (`kubeconform`, server-side dry-run, CI linting) works.

## Non-goals

- A `FissionInstance` operator that re-implements what Helm+GitOps controllers already do (#2742) — explicitly deferred; revisit only if OLM/marketplace distribution becomes a goal.
  The operator's real jobs here are done by the chart (install), Argo/Flux (sync), and RFC-0028 (upgrade safety).
- A full hand-written Terraform provider in v1 (see IaC section — start with schemas + modules; a generated provider is a later call).
- Replacing `fission spec` outright — it remains the fast local-DX tool; this RFC constrains its semantics, it does not grow its scope.
- Git integration inside Fission (no repo-watching controller; the GitOps controller owns Git).

## Design

### 1. CRD delivery and upgrade (#2382)

Options considered:

- **(a) Helm `crds/` directory** — installs but never upgrades; rejected as the primary mechanism (it recreates today's skew problem one release later).
- **(b) Templated CRDs** in the chart, gated by a values flag, annotated `helm.sh/resource-policy: keep`.
- **(c) A separate `fission-crds` chart** — two artifacts to version-sync; rejected.
- **(d) A pre-install/pre-upgrade hook Job that server-side-applies the CRD bundle.**
  Packaging note: the bundle cannot ride the existing hook image as files — `preUpgradeChecksImage` is a `chainguard/static` single-binary image with no filesystem to carry YAML — so the CRDs are **embedded in the checker binary** (`go:embed` of `crds/v1`), which also guarantees binary and schema ship as one versioned artifact.

Recommendation: **(d) as the default writer, (b) as an alternative writer, never both** — governed by a **single-writer principle**, because two mechanisms SSA-ing the same CRD objects under different field managers (Helm, the hook, and Argo would make three) is a field-ownership fight nobody wins.
Note the hook-timing reality that kills the "hook verifies, Helm applies" hybrid: `pre-upgrade` hooks run **before** the release manifests are applied, so a hook that checks "in-cluster CRDs match the chart" would inspect the *old* CRDs and fail precisely on every CRD-changing upgrade (today's `pre-upgrade-checks` job is `helm.sh/hook: pre-upgrade` only, and its CRD check is a presence probe of the manually-applied CRDs — `cmd/preupgradechecks/checks.go`).

- `crds.mode: hook` (default): the hook Job — extending the existing `preupgradechecks` job — server-side-applies the chart-version CRD bundle *before* manifests apply, giving the RFC-0028 CRDs-before-controllers ordering for free; the chart templates no CRDs.
  Three chart facts this depends on and must change: the Job is annotated `pre-upgrade` **only** (needs `pre-install` too), its SA/ClusterRole/ClusterRoleBinding are likewise `pre-upgrade`-only and must gain `pre-install`, and the whole Job is gated by `preUpgradeChecks.enabled` — so `crds.mode: hook` must either force that gate on or fail render, never silently no-op.
- `crds.mode: manifests`: for teams that want CRDs visible in the rendered manifest set (Argo diffing), the chart templates the CRDs with `keep`.
  **The hook's check in this mode must be presence-and-tolerance only, never "in-cluster ≥ chart"** — that comparison reproduces exactly the incoherence that killed the hybrid: the hook still runs before the manifests it is validating against. (The "Argo applies CRDs in wave 0 first" escape does not save it either: Argo maps `helm.sh/hook: pre-install,pre-upgrade` to a **PreSync** hook, which completes *before* sync wave 0.) If a real post-apply assertion is wanted, it belongs in a separate `post-upgrade` hook.
- `crds.mode: none`: bring-your-own (today's behavior); hook does the presence check only, as it does now.

**RBAC scoping is part of the design, not an implementation detail.** No Fission component holds CRD *write* today (every clusterrole is `get/list/watch`), so the hook's grant is a genuine step up: cluster-wide CRD patch permits attaching a `spec.conversion.webhook` to any CRD — interposing on all reads and writes of that type — or stripping schema/CEL constraints from Fission's own CRDs.
Scoping has one sharp constraint that dictates the design: **RBAC `resourceNames` does not restrict `create`, `list`, or `watch`** — the object name is unknown at authorization time. A rule combining `resourceNames` with `create` does not merely fail to scope, it grants *nothing* for create; and server-side apply compounds this, since SSA is a PATCH that additionally requires `create` when the target does not exist. A `resourceNames`-only grant would therefore fail on a fresh cluster and break G1.

The grant is two rules, and the scoping for `create` deliberately lives outside RBAC:

```yaml
- apiGroups: ["apiextensions.k8s.io"]
  resources: ["customresourcedefinitions"]
  verbs: ["get", "list", "watch", "patch", "update"]
  resourceNames: [functions.fission.io, packages.fission.io, ...]   # fences the mutating verbs
- apiGroups: ["apiextensions.k8s.io"]
  resources: ["customresourcedefinitions"]
  verbs: ["create"]                                                  # cannot be name-scoped
```

The ValidatingAdmissionPolicy is therefore load-bearing, not belt-and-braces: bound on `customresourcedefinitions`, with `matchConditions` on `request.userInfo.username == "system:serviceaccount:<ns>:<hook-sa>"` and validations `object.spec.group.endsWith("fission.io")` plus `!has(object.spec.conversion) || object.spec.conversion.strategy == "None"`. That is what constrains `create`.
No `delete`, no wildcards. (Note the tenant-controller precedent is cited for *shape* only — its `resourceNames`-scoped grant is on `bind`, one of the special verbs where `resourceNames` does work, so it is not evidence that CRD write can be name-scoped.)
The RBAC objects also need an explicit `helm.sh/hook-weight` below the Job's, or Helm may order the ServiceAccount and binding after the Job that needs them.
The ClusterRole/ClusterRoleBinding/ServiceAccount are annotated `hook-delete-policy: hook-succeeded,before-hook-creation` so the privilege does not outlive the hook — today's pre-upgrade RBAC objects are `before-hook-creation` only and therefore persist indefinitely between upgrades.
A ValidatingAdmissionPolicy forbids that ServiceAccount from setting `spec.conversion`, following the tenant-controller precedent already in the chart (a `resourceNames`-scoped `bind` grant fenced by a VAP).
`crds.mode: none` remains the escape hatch for orgs that will not grant CRD write at all.

Two known sharp edges get first-class handling: **four** of the CRD YAMLs exceed the 262,144-byte client-side-apply annotation limit (`environments` ~1.4 MB, `functionversions` ~696 KB, `functions` ~657 KB, `messagequeuetriggers` ~573 KB), so every apply path in this design — the hook, the docs, the Argo examples — mandates **ServerSideApply** (Argo `syncOptions: [ServerSideApply=true]`, Flux does SSA natively); and `keep` means uninstall leaves CRDs (and therefore user data) behind — documented as intentional, with an explicit `make delete-crds` escape hatch.
**Adoption of hand-applied CRDs needs `--force-conflicts`, stated plainly.** Existing CRDs carry managedFields owned by `kubectl-create` as an *Update* operation (the documented `kubectl create -k crds/v1` flow), so a first SSA from a new field manager conflicts on every field whose value the chart changes — i.e. precisely on a CRD-changing upgrade, the case that matters. The hook therefore applies with `--force-conflicts` on first adoption, and the docs state what that may stomp: any hand-edit made directly to a Fission CRD (added printer columns, relaxed validation) is reverted to the chart's schema. `crds.mode: none` is the path for anyone who needs those edits preserved.
`manifests` mode additionally documents the one-time Helm ownership-metadata adoption for pre-existing CRDs.

### 2. Chart hygiene (#2383, #2906, #2835)

- **File contents via values**: every chart input that is a file path today (Kafka TLS certs are the filed case) gains a `*Content`/`existingSecret` variant — inline value (`--set-file` friendly) or reference to a pre-created Secret; no more unpacking the chart.
- **Naming**: resources adopt a `.Release.Name`-prefixed fullname convention behind `nameFormat: legacy|standard`, defaulting to `legacy`, flipping only after RFC-0028's deprecation window.
  The blast radius is bigger than the chart: fixed names are load-bearing outside it — `router-internal.<namespace>` is composed in `_helpers.tpl` and consumed by every internal publisher, the integration framework routes by fixed names (`router.fission`, `mcp.fission`, the `fission-internal-auth` Secret), the router NetworkPolicy allowlists callers by fixed `svc:` labels, and the existing `fullname` helper truncates at 24 characters and cannot be adopted as-is.
  Phase 2 therefore starts with an explicit inventory of fixed-name consumers (helpers, NetworkPolicies, integration framework, docs) and a widened helper, and `standard` mode is not offered until that inventory is green in CI.
  The per-release webhook configs this needs must also survive regeneration: `webhooks.yaml` is generated from kubebuilder markers (`make generate-webhooks`), so prefixing/`namespaceSelector` injection happens in the generation pipeline (and honors the existing 3-place webhook-manifest sync), not by hand-editing output.
- **Multi-instance — honestly scoped**: CRDs are *not* the only shared surface.
  `FissionTenant` CRs are cluster-scoped, each release's tenant controller holds a lease in its own namespace (so two releases are concurrent leaders over the same CRs), and the crmanager-based trigger managers watch Fission CRDs cluster-wide filtered only by the shared tenant set — two instances would both fire the same TimeTriggers and both consume the same MQ topics.
  Scope note: the double-fire is **conditional on tenancy mode**, not universal — the cluster-wide cache is taken only when `CrdWatchClusterWide()` is true (dynamic/cluster modes); in the default `static` mode the cache is scoped to `FISSION_RESOURCE_NAMESPACES`, so two static-mode releases over disjoint namespaces already coexist without double-firing. The instance-class requirement below therefore applies to dynamic/cluster mode, and `#2835` for static-mode users is closer than for the rest.
  Real coexistence in dynamic/cluster mode needs an **instance-class mechanism**: an `fission.io/instance` class (label on namespaces or a field on `FissionTenant`) that every watching manager filters on and the webhook scopes by `namespaceSelector` — the ingressClass pattern.
  The class rides the existing privileged channel: namespace labels (`fission.io/enabled`) and `FissionTenant` CRs are both cluster-scoped, so only a cluster-level principal can set them — it must **never** be settable on a tenant-writable CR, or a tenant could reassign its own namespace between instances.
  **The webhook partition must be exhaustive.** The chart renders 11 configurations (2 mutating — `mpackage`, `mworkflow` — and 9 validating), all `failurePolicy: Fail`, so every CR write *of a covered type* is validated. (Coverage is not total today: HTTPTrigger, TimeTrigger, CanaryConfig and FissionTenant have no webhook at all — worth knowing, but not this RFC's problem to fix.) Partitioning by `namespaceSelector` makes even the covered types conditional, and a namespace matching *no* release's selector would be admitted with **no webhook at all** — silently losing `ValidatePodSpecSafety`/`ValidateContainerSafety` and, once RFC-0030 lands, its env-name and `MountPath` checks. (The `merge.go` sanitizers still hold the sandbox line, so this is a validation bypass, not a container escape — but validation-only rules are exactly what this RFC's siblings rely on.)
  A *rendered* exhaustiveness assertion is impossible — the chart cannot enumerate cluster namespaces at render time, and under `helm template` definitively cannot. So exhaustiveness comes from structure instead, the ingressClass `is-default-class` pattern: **one release is the default instance** (`instance.default: true`) and its webhook `namespaceSelector` matches namespaces *lacking* the class label — `{key: fission.io/instance, operator: DoesNotExist}` — plus its own class value; every non-default release selects only `In [its class]`. That is a partition by construction, and the chart fails render when a release is neither default nor classed.
  Ordering constraint: the ten webhook configurations are cluster-scoped with fixed names, so two releases rendering identical names is itself a partition failure — §2's prefixing must land **before** the partition, not alongside it.
  v1 of this RFC ships the contract and validation for *disjoint-namespace* instances gated on that filter; until it lands, the documented support statement is one instance per cluster (#2835 stays open against the instance-class work, not the naming work).
  Leader-election leases are already namespace-scoped automatically (crmanager detects the pod namespace), so they are not a blocker.

### 3. The golden path: plain CRDs + OCI packages (#1687, #2928, #2836)

- **No auth material may be generated in the templating layer — a hard prerequisite, not a recommendation.** Two separate templates mint secrets at render time, and the second is worse than the first:
  - `internal-auth-secret.yaml` preserves the HMAC master via `lookup` with a `randAlphaNum` fallback, and its own comment warns that regeneration "would break every running pod that was signed with the previous secret."
  - `router/secret.yaml` mints `password` and `jwtSigningKey` with **no `lookup` at all**, under a `pre-install,pre-upgrade` hook — so the router's JWT signing key (the same key that gates MCP's `allowed_namespaces` authz) already rotates on *every* `helm upgrade`, not just under GitOps, unless `authentication.jwtSigningKey` is set explicitly.

  Argo CD renders with `helm template`, where `lookup` always returns empty, so the blessed path would mint a new master on every sync — rotating every derived key (per-service, per-namespace tenancy, RFC-0023 per-keyspace tokens) underneath running pods, with no `OldSecret` dual-accept window, across fail-closed channels. The predictable operator response — switching the verifier to empty-secret pass-through to stop the 401 storm — silently removes the GHSA-3g33-6vg6-27m8 control.

  **"Fail render when a lookup-backed secret would be minted blind" is not expressible and is dropped**: `lookup` is empty for `helm template` *and* for a genuine first `helm install`, `.Release.IsInstall` is true in both (Argo sets it on every sync), and `.Capabilities` carries no renderer signal. Corrected design:

  > The chart never generates auth material in a template. Each secret has two supported inputs — inline (`internalAuth.secret`, `authentication.jwtSigningKey`) or a reference (`internalAuth.existingSecret`, `authentication.existingSecret`, following the `statestore.external.existingSecret` naming precedent). For the zero-config install path, generation moves **out of templating into an idempotent in-cluster action**: a `pre-install` hook Job that creates each Secret only if absent, so the value survives any renderer, including repeated `helm template` syncs. Render fails when a secret is required, neither input is set, and `autoGenerate` is false; the GitOps docs require `autoGenerate: false`.

  The same rule covers the webhook serving cert, which RFC-0028 §2 defers to this section as one chart-wide policy.
- **Docs + example repo** (`examples/gitops/`): Argo and Flux layouts managing Environments, Functions (OCI-image packages), HTTPTriggers, Workflows, aliases; CI builds the function image, bumps the digest in Git, the controller syncs — the CLI never runs.
  This answers #2836 ("build outside Fission, deploy to Fission") with a definitive yes-and-here's-how.
- **Health**: audit all CRDs for a standard `Ready`-style condition (most gained conditions through RFC-0004/0013/0025 work); ship Argo Lua health checks for the remainder so a sync waits on *actual* readiness (package built, route admitted, alias resolved).
- **Content-driven rebuilds (#2928)**: buildermgr keys build state on a content hash of the package source (archive checksum / image digest) rather than requiring the CLI's status→pending poke on updates (fresh packages already build controller-side); a GitOps-applied Package whose source changed rebuilds automatically, and an unchanged reapply provably does not (the no-op invariant below).
  Upgrade seeding rule: a package with **no recorded hash** (every package, on the first reconcile after this ships) seeds the hash from its current state without rebuilding — absent-hash must never read as "changed", or the upgrade triggers a cluster-wide rebuild storm.
  The CLI's status→pending poke keeps working as a second, explicit trigger (force-rebuild), so existing scripts and muscle memory don't break.
- **Content-driven redeploys** — the half a rebuild fix alone misses: today new package content reaches running Functions only because `spec apply` writes the package's new ResourceVersion into each referencing Function's `PackageRef.ResourceVersion`; with no CLI in the loop, pods would never recycle.
  Fix: **change the trigger, don't add a controller.** buildermgr already re-stamps every referencing Function on build success; what is missing is a path for packages that never build. So the restamp is re-keyed to **package content change** (archive checksum / OCI digest) rather than build completion, which covers deploy-only and OCI packages on the same code path. The CLI stamp sites stay valid and converge to the same value (idempotent, no controller-vs-CLI fight).
  This is a deliberate behavior change — functions now *follow* their package's builds without a CLI step — so it carries a per-function opt-out annotation for anyone relying on manual roll timing, and the docs point at the real pinning mechanism: RFC-0025 `FunctionVersion` snapshots and aliases, which exist precisely so "stay on the old code" is a first-class object rather than a stale ResourceVersion stamp.
  (Long-term direction: key the executor's function generation on package content digest directly and delete the stamp; explicitly out of scope here.)
- **Ownership chains — runtime artifacts only**: ensure ownerReferences flow from Git-declared objects to their *runtime* children (Package→built artifacts, Function→pods/services) so `kubectl tree` (#1487) and Argo's resource tree render topology; deliberately do **not** add ownerRefs between Git-declared objects (Trigger→Function) — deleting an owner would garbage-collect a child Git still declares, fighting the sync loop.
  Today's ownerRef usage is runtime-only and globally gated by `ENV_DISABLE_OWNER_REFERENCES` (`pkg/utils/utils.go`); this work extends coverage within that gate.
- **Schemas**: publish per-version JSON schemas (generated from the CRDs — the OpenAPI machinery behind `make generate-swagger-doc` is the reuse point — attached to releases + a stable URL) for kubeconform/editor validation; `fission spec validate` gains `--offline` using the same schemas (today it unconditionally requires a cluster connection).

### 4. The `fission spec` contract (#543, #551, #555, #623, #1037, #1491, #1574, #3230, #3296, #740)

`fission spec` is redefined as: *a generator and applier of exactly the CRDs the golden path uses*, with three tested guarantees:

- **Idempotent** — `spec apply` twice is a no-op the second time: fix the false-positive duplicate detection (#3296), the misleading already-exists error (#1491), and suppress rebuilds when the archive checksum is unchanged (#2188's neighbor; enabled by the content-hash keying in §3).
- **Complete** — the mechanism already exists and is kept: `spec apply --delete` prunes cluster objects that carry this spec's deployment-uid but are absent from the local specs, and `spec destroy` removes everything; this RFC hardens rather than invents — document it as the blessed lifecycle, add the missing tests, and define rename (#543) as delete+create under `--delete` with the data implications stated.
  One factual constraint shapes the hardening: the deployment-uid is an **annotation** (`SetAnnotations` in `pkg/fission-cli/cmd/spec/resourcetype.go`), so prune can never use a server-side label selector; either it stays a client-side filter (status quo, acceptable) or a label migration gets its own compatibility step — decided in design review, not assumed.
- **Deterministic** — spec-file paths get identical defaulting to CLI paths by extracting a **shared defaulting layer** and routing both through it; this is mostly construction, not reuse — today only `Package` and `Workflow` have webhook Defaulters, while Function/Environment/trigger defaults live scattered in CLI create paths (#3230's class dies only when that layer exists and also covers raw-kubectl writers); validation covers empty `ArchiveUploadSpec`s (#623) and duplicate archive names (#551) at `validate` time, not apply time.
  Mechanism constraint for the GitOps path: prefer **CRD structural defaults** (`+kubebuilder:default` markers, visible to server-side dry-run) over a mutating webhook wherever the default is a static value — a webhook-mutated field that isn't in the Git manifest is a permanent Argo/Flux diff unless every user configures `ignoreDifferences`; only defaults that genuinely need computation go through the webhook, and those get named in the docs' `ignoreDifferences` snippet.
  The extracted layer must be golden-tested against today's CLI defaults so extraction changes no existing object (byte-identical output for every current CLI create path).
- **Templating (`--set`)** — minimal `{{ .Values.key }}` substitution, satisfying #740's surviving half; substitution runs **only when `--set`/`--values` is passed**, so existing spec files containing literal `{{ }}` (or none) parse exactly as today; an unresolved placeholder under `--set` is a validation error, not silent passthrough.
  **Substitution is YAML-node-level, never text-level.** Specs are read as raw bytes, split on `\n---`, then unmarshalled per document (`pkg/fission-cli/cmd/spec/validate.go`), so text substitution lets a value containing a newline inject sibling keys — and one containing `\n---\n` inject an entire additional Fission object (an Environment pointing at an attacker image, a Package with an attacker digest), applied with the pipeline's kubeconfig. A CI job doing `--set ref=$PR_BRANCH` on an untrusted branch name is the concrete path.
  Therefore: parse first, substitute into scalar nodes only, decode straight from the node tree (do not re-serialize and re-split; if re-serializing is unavoidable, emit through the YAML encoder with `Style: DoubleQuotedStyle`, never string concatenation). Keep the newline / leading-sigil rejection as belt-and-braces.
  Three consequences to document rather than discover:
  - The file must parse as YAML *before* substitution, so a placeholder in **leading** position (`env: {{ .Values.env }}`) is not a scalar — YAML reads `{` as a flow mapping — and must be quoted (`"{{ .Values.env }}"`). Mid-scalar placeholders (`name: fn-{{ .Values.ref }}`) are plain scalars and work unquoted.
  - Placeholders in keys require a quoted key; placeholders in non-scalar positions (injecting a list or mapping) are unsupported **by construction** — desirable, but say so.
  - The compatibility claim above narrows accordingly: existing files containing literal `{{ }}` parse as today *while `--set` is unused*; adopting `--set` requires quoting leading-position placeholders.
  Costing: the spec reader uses `sigs.k8s.io/yaml` (YAML→JSON), which has no Node API — node-level substitution means introducing `yaml.v3` Node parsing into this path.
  Anything beyond simple substitution is deliberately punted to kustomize/Helm with a documented pattern, to keep spec from growing a template language.
- `--watch`/builder conflicts (#555) and scaffolding refresh (#1037, #1574) ride along as the DX batch.

### 5. IaC surface (#1907)

Phased by maintenance cost:

1. **Now**: published JSON schemas (§3) + a `terraform/` examples directory using `kubernetes_manifest` with typed `for_each` modules for Environment/Function/HTTPTrigger; Pulumi gets CRD types generated from the same schemas via `crd2pulumi` — both documented on fission.io.
2. **Later, demand-gated**: a generated Terraform provider (from the OpenAPI schemas) if `kubernetes_manifest` friction proves real; explicitly not hand-maintained.

## Backward compatibility

The rule for this RFC: **every existing workflow keeps working unchanged**; the declarative path is added and blessed, never forced.

- **CRD install paths all survive.** `crds.mode: none` preserves today's out-of-band flow exactly (`kubectl create -k crds/v1` / `make create-crds` are retained and stay in the docs); the default `hook` mode SSAs *over* hand-applied CRDs without requiring an adoption step, and nothing in any mode ever deletes a CRD (`keep` on the templated variant, apply-only hook) — user data cannot be lost by an install-mechanism choice.
  The hook does require a new ClusterRole grant (CRD write) in the chart's RBAC — named in the values docs, and avoidable via `crds.mode: none` for orgs that restrict it.
- **Chart values are additive.** Every `*Content`/`existingSecret` variant lands beside the existing path-based value, with documented precedence; no existing value is removed or repurposed.
- **Naming never changes out from under an install.** `nameFormat: legacy` is the default indefinitely; `standard` is opt-in, documented as a **recreate migration** (Deployments/Services are renamed, which is a brief control-plane outage — pointed at RFC-0028's upgrade guarantees for sequencing), and any future default flip follows RFC-0028's deprecation window.
- **The instance-class filter defaults to "no filter".** Absent a class, every manager watches exactly what it watches today; single-instance clusters see zero behavior change.
- **Rebuild/redeploy convergence is guarded against upgrade shock.** The hash-seeding rule above prevents a rebuild storm on first upgrade; the CLI's pending-poke and `spec apply`'s ResourceVersion stamping both keep working (the controller converges to the same values); auto-follow has a per-function opt-out, and deliberate pinning is redirected to RFC-0025 versions/aliases.
- **`fission spec` files are format-stable.** No new required fields; `--set` substitution activates only when flags are passed; `--delete` semantics are hardened, not changed; the deployment-uid stays an annotation unless a compat-stepped label migration is explicitly decided (§4).
- **Defaulting extraction is provably non-mutating.** Golden tests pin the extracted layer to today's CLI output byte-for-byte; structural CRD defaults only encode values that were already the universal default, so existing objects re-applied through any path are unchanged.
- **ownerReference additions respect the existing gate.** Runtime-children ownerRefs extend under `ENV_DISABLE_OWNER_REFERENCES` exactly as today's do; setting the gate disables the new ones too, and no ownerRefs are added between Git-declared objects (so no GitOps GC surprises on upgrade).
- **Schemas and IaC artifacts are pure additions** — published files and example directories; nothing existing depends on them.

## Rollout phases (bisectable)

1. **Packaging**: `crds.mode` single-writer mechanism (hook SSA default, CRDs `go:embed`-ed in the checker binary; the hook Job **and** its SA/ClusterRole/ClusterRoleBinding gain `pre-install`; scoped RBAC + VAP; `--force-conflicts` adoption); values file-contents; **the auth-material prerequisite — `internalAuth.existingSecret` + the explicit fail-render guard — since G3 asserts it and no later phase delivers it**; schema publishing off the OpenAPI machinery. Closes #2382, #2383.
2. **Naming/multi-instance groundwork**: fixed-name consumer inventory + widened fullname helper behind `nameFormat` (default `legacy`); webhook prefix/`namespaceSelector` support in the generation pipeline; instance-class filter design. Closes #2906 when `standard` flips; #2835 closes on the instance-class filter, not before.
3. **Golden path**: examples/gitops + Argo/Flux docs + health checks + runtime-ownerRef audit; content-hash rebuild keying in buildermgr **and** the build-success ResourceVersion-stamp controller (redeploy propagation). Closes #1687, #2928, #2836, #1487.
4. **Spec contract**: idempotency batch (#3296, #1491), the shared defaulting layer (#3230, #623, #551), `--delete` hardening + docs, `--set`. Closes the spec batch + #740's remainder.
5. **IaC**: terraform/ + pulumi examples on published schemas. Closes #1907 (provider decision deferred, criteria stated).

## Invariants & verification

- **G1** — fresh kind cluster + `helm install` alone (no `make create-crds`) yields a working install; same via an Argo Application with SSA. CI leg.
- **G2** — `helm upgrade` from N−1 upgrades CRDs before any controller pod rolls (shared assertion with RFC-0028's upgrade leg).
- **G3** — syncing the example GitOps repo twice produces zero writes, zero rebuilds, zero pod restarts on the second sync (the idempotency invariant, asserted via audit log + resourceVersion snapshot in an integration test), **and the data of `Secret/fission-internal-auth` *and* `Secret/router` (JWT signing key, password) and the webhook serving cert is byte-identical across both syncs** — the explicit guard against the render-time generation paths above, which a write-count assertion alone can miss.
- **G4** — reverting the Git commit that bumped a package digest converges the cluster back **including running pods** (old digest live, referencing Functions re-stamped and re-specialized). Asserted for **both** package shapes: a source package (rebuild triggered exactly once) and a deploy-only/OCI package (no build at all, restamp still happens) — the OCI case is the one that fails today and the reason the trigger is content-change, not build-success.
- **G5** — `spec apply` twice is byte-for-byte no-op the second time; `spec apply --delete` after removing a spec file removes exactly that object. Integration-tested.
- **G6** — two releases with disjoint namespace sets under the instance-class filter serve traffic independently — no double-fired timers, no double-consumed topics; deleting one leaves the other and the CRDs intact. Serial-suite test, gated on the phase-2 filter.
- **G7** — every Fission-enabled namespace is matched by exactly one release's webhook `namespaceSelector`: no namespace is unvalidated, none is double-validated. Two parts: (a) a rendered check that the default release's selector carries the `DoesNotExist` term (the structural guarantee), and (b) a runtime test creating a namespace with no instance class, which must be caught by the default instance and never silently admitted unvalidated.
- **G8** — the hook ServiceAccount cannot patch a non-Fission CRD, **cannot create one** (the case RBAC cannot hold, so this is the admission policy's assertion, not a formality), and cannot set `spec.conversion` on any CRD.
- Property tests (`rapid`) for the spec defaulting parity (§4): random valid spec ⇒ CLI-path object == spec-path object.

## Open questions

- `crds.mode` default for existing installs: `hook` needs no adoption step, but is silently taking SSA field ownership of hand-applied CRDs acceptable default behavior, or should the first upgrade require an explicit `crds.mode` choice?
- `--delete` default: off forever (explicit opt-in, Argo-style) seems right — confirm nobody expects Helm-style implicit pruning.
- Instance-class mechanism shape: namespace label vs `FissionTenant` field — and does the webhook `namespaceSelector` partition ship in the same phase or after?
- Schema publishing venue: release assets only, or also a `schemas.fission.io` stable URL (nicer for kubeconform, one more thing to host)?
- Should `fission spec` gain a `spec.fission.io/v2` format version field while we're constraining its semantics, so future format changes are detectable?

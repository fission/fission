# RFC-0001: OCI-Native Package Delivery

- Status: Accepted (implementation in progress; see Amendments)
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.(N+1)
- Requires: Kubernetes 1.32 (current floor) for the fetcher-pull path; Kubernetes 1.33+ (KEP-4639 image volumes) as an opt-in for the image-volume path. Self-contained — does **not** depend on RFC-0003.

## Summary

Deliver Fission function code as OCI artifacts in a container registry instead of tarballs in `storagesvc`, as an opt-in alternative path.
A Package whose `Deployment` archive carries the new `OCI` field gets image-native delivery: the function's code lives in an OCI image, distributed by a registry and cached by the kubelet/registry, instead of a tarball fetched and unzipped per pod.

The environment **runtime image stays the pod's main container**; only how the code reaches `/userfunc` changes.
Poolmgr is the priority executor for this RFC, with a hybrid of two delivery paths (below).
`storagesvc` and the tarball path remain the default and are not deprecated.

## Motivation

Today's cold start for a fresh pod on a fresh node is dominated by:

1. Fetcher HTTP GET of a full tarball from `storagesvc` (no layer cache, no dedup, no streaming).
2. Unzip into an `emptyDir`.
3. Fetcher HTTP POST to the environment runtime's `/v2/specialize` endpoint.
4. Environment runtime loads the code.

Steps 1–2 are pure overhead that Kubernetes + a registry handle natively for container images.
The tarball path also precludes:

- **Cross-node caching** — each node re-downloads; OCI gives this for free via the kubelet/containerd layer cache (image-volume path).
- **Deduplication across functions** — two functions sharing a base + deps ship two full tarballs today; OCI layers dedup by digest.
- **Lazy loading** — nydus/estargz snapshotters can stream image layers on first read (future, out of scope here).
- **Standard tooling** — `docker push`, `crane`, `cosign`, registry RBAC, vulnerability scanners, retention policies.
- **Supply-chain signing** — `cosign`-style signatures, SBOM attestations.

## Goals

- New optional field `Archive.OCI` (`*OCIArchive`) referencing an OCI image that holds the deployment code. Backward compatible and additive.
- **Poolmgr** OCI delivery via a hybrid of two paths:
  - **Path A (universal baseline)** — the in-pod `fetcher` sidecar pulls the OCI image and extracts its filesystem into the shared `/userfunc` volume, then the existing `/v2/specialize` load proceeds. Works on the current Kubernetes 1.32 floor; generic warm pools are preserved.
  - **Path B (opt-in optimization, K8s 1.33+)** — poolmgr pre-warms pods per `(environment + image)` that mount the code image read-only at `/userfunc` via a KEP-4639 `ImageVolumeSource`; specialization becomes a load-only signal (no fetch/unzip). The kubelet pulls and caches layers and resolves pull secrets.
- CLI: `fission package create --oci <image>` and the same flag on `fission fn create` — opt-in, additive, referencing a **pre-built** image.
- Backward compat: all existing literal/url packages keep working unchanged.

## Non-goals

- Removing the tarball / `storagesvc` path. It stays the default.
- Forcing any existing user to migrate.
- ~~OCI delivery for the `newdeploy` and `container` executors — deferred to a follow-up RFC.~~
  **Amended 2026-06-10:** `newdeploy` is now IN scope as rollout Phase 4 (maintainer decision — all-in-one delivery).
  Path A is free for newdeploy (it shares `NewSpecializeRequest` and the fetcher binary); Path B keeps the specializing fetcher and mounts the image volume at the fetcher's storePath so the existing early-exit makes specialization load-only.
  `container` stays out of scope: it never consumes Package archives (`fn.Spec.PodSpec` carries the image directly).
- A BuildKit-based builder that turns user **source** into an OCI image and pushes it (`--oci-push`). Deferred to a follow-up RFC; this RFC consumes **pre-built** images only.
- Building or bundling a registry — users bring their own (Harbor, GHCR, ECR, Artifact Registry, Docker Hub).
- Lazy-loading snapshotters (nydus/estargz). Plain OCI already wins; snapshotters are an optional later multiplier.

## Design

### Artifact shape

The `OCI.Image` reference is an **OCI image whose flattened filesystem contains the deployment code** at `SubPath` (default the image root, i.e. what a tarball would have unzipped to).
A trivial producer is `FROM scratch` + `COPY deploy/ /`.
In both delivery paths the environment runtime image remains the pod's main container, and the runtime's `/v2/specialize` load contract is unchanged — only the code-acquisition step differs.

### CRD additions (self-contained in this RFC)

Add an optional `OCI` field to the existing `Archive` type in `pkg/apis/core/v1/types.go`.
`Source` and `Deployment` are both the `Archive` type, so the field is structurally shared; semantically only `Deployment.OCI` is consumed (function code is a deployment artifact, not source to be built).

```go
// Archive, extended. Note: the package imports k8s.io/api/core/v1 as `apiv1`.
type Archive struct {
    // Existing fields (Type, Literal, URL, Checksum) unchanged.
    // Type enum marker extended to: "";literal;url;oci

    // OCI references an OCI image holding the deployment code.
    // Mutually exclusive with Literal / URL.
    // +optional
    OCI *OCIArchive `json:"oci,omitempty"`
}

type OCIArchive struct {
    // Image is a fully qualified OCI reference: registry/repo:tag[@digest].
    // +kubebuilder:validation:MinLength=1
    Image string `json:"image"`
    // ImagePullSecrets are resolved when pulling the image. Path A passes
    // them to the in-fetcher keychain; Path B sets them on pod.Spec.ImagePullSecrets.
    // +optional
    ImagePullSecrets []apiv1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
    // SubPath points at the deployment root inside the image filesystem.
    // "" or "/" means the image root.
    // +optional
    SubPath string `json:"subPath,omitempty"`
    // Digest is an optional content hash; if set, it is validated on pull.
    // +optional
    // +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
    Digest string `json:"digest,omitempty"`
}
```

New constant in `pkg/apis/core/v1/const.go`:

```go
ArchiveTypeOCI ArchiveType = "oci"
```

**Validation (CEL).**
Validation in this repo is split: field-level invariants live as `+kubebuilder:validation:XValidation` CEL markers in `types.go`; cross-object / pod-spec / size checks that CEL cannot express stay in the webhook (`pkg/webhook/`).
The mutual-exclusivity rule is "**at most one**" of literal/url/oci, not "exactly one" — an empty `Archive{}` (e.g. a Package with only a Deployment, or only a Source) is a valid first-class state today and must stay valid.
Add an `Archive`-level marker, mirroring the existing Checksum CEL style:

```
+kubebuilder:validation:XValidation:rule="(has(self.literal) && size(self.literal) > 0 ? 1 : 0) + (has(self.url) && self.url != '' ? 1 : 0) + (has(self.oci) ? 1 : 0) <= 1",message="at most one of literal, url, or oci may be set"
```

CEL cost is trivial (three `has()`/`size()` ops summed) — nowhere near the pod-spec budget concerns that keep other checks in the webhook.

**Validation (Go, `pkg/apis/core/v1/validation.go`).**

- Add `ArchiveTypeOCI` to the `Archive.Validate()` type switch, plus a Go-level mutual-exclusivity backstop.
- Add `OCIArchive.Validate()` (image non-empty; digest format) mirroring `Checksum.Validate`.
- Change the `PackageSpec.Validate()` loop guard from "URL or Literal non-empty" to `!r.IsEmpty()` so an OCI archive is validated.

**`Archive.IsEmpty()`** must also return false when `OCI != nil`.
This single change makes an OCI-only deployment default to `BuildStatusNone` in both consumers — `pkg/webhook/package.go` `ApplyDefaults` and `pkg/buildermgr/package_reconciler.go` `setInitialBuildStatus` — so **no webhook code change is needed** and no builder runs for an OCI package.

**Codegen** (never hand-edit generated output):

- `make codegen` — deepcopy for the new pointer + `ImagePullSecrets` slice, new `OCIArchive` deepcopy, and applyconfigurations (new `applyconfiguration/core/v1/ociarchive.go`, `WithOCI` on `archive.go`, registration in `utils.go`/`internal.go`).
- `make generate-crds` — `crds/v1/fission.io_packages.yaml` gains the `oci` enum value, the `oci` object, and the Archive-level `x-kubernetes-validations`.
- `make generate-swagger-doc`; `make license` for the new generated file.
- `make generate-webhooks` is **not** required (no `+kubebuilder:webhook` marker changes).

### Poolmgr delivery — Path A (fetcher pulls the OCI image)

The universal baseline. Generic per-environment warm pools are unchanged; on a cache miss a warm pod is chosen and specialized as today (`pkg/executor/executortype/poolmgr/gp.go:specializePod` POSTs to the pod's `fetcher` at `:8000/specialize`).
The only change is inside the fetcher's fetch step.

- **`pkg/fetcher/fetcher.go` `Fetch()`** — inside the existing `FETCH_DEPLOYMENT` case, branch first on the live package (the fetcher already re-reads the live `pkg` via `getPkgInformation`, so the OCI fields ride along and **no wire-request change is needed**).
  **Amended 2026-06-10:** the branch extracts into a tmp dir under the `os.Root`-confined shared volume and then renames to `storePath` (`fetcher.rename(tmpDir, storePath)`) — NOT the volume root as originally sketched — because `LoadReq.FilePath` is `<sharedMountPath>/<targetFilename>` and the early-exit at the top of `Fetch()` keys idempotency on `storePath`.

- **`pkg/oci` (new package; amended from `pkg/fetcher/oci.go`)** — `ExtractImage(ctx, imageRef, destRoot, destDir, opts)`:
  - `name.ParseReference` → `remote.Image(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(kc))`; if `Digest` is set, compare `img.Digest()`.
  - `mutate.Extract(img)` returns a single, whiteout-resolved, flattened rootfs tar.
  - Extract under `destDir` reusing the **same zip-slip safety posture as `utils.UnarchiveInRoot` (`pkg/utils/zip.go`)**: `os.OpenRoot(destRoot)` + per-entry `filepath.Clean`, rejecting absolute paths, `..`, and symlink/hardlink entries; mask modes to `0o777`; cap total extracted bytes (decompression-bomb guard). Apply `SubPath` by keeping only entries under the prefix and re-rooting them.
  - Use `github.com/google/go-containerregistry` (`remote`, `mutate`, `name`, `authn`).
  - It lives in a standalone `pkg/oci` so newdeploy (Phase 4) and the future BuildKit-push RFC reuse it without importing fetcher.

- **Credentials** — **Amended 2026-06-10:** use `pkg/authn/kubernetes` (`kauth`), NOT `pkg/authn/k8schain` — k8schain is a separate module that hard-depends on AWS/GCP/Azure credential helpers and would bloat the static fetcher image. `kauth.New(ctx, kubeClient, Options{Namespace, ServiceAccountName: fv1.FissionFetcherSA, ImagePullSecrets: names})` resolves the pod ServiceAccount's `imagePullSecrets` plus the explicit `OCIArchive.ImagePullSecrets`, chained with `authn.DefaultKeychain`. Cloud-ambient credentials (node IAM → ECR) remain a documented Path B advantage.

- **RBAC** — the `fission-fetcher` Role (`charts/fission-all/templates/_function-access-role.tpl`) already has `secrets: get`; add `serviceaccounts: get` so `kauth` can read the SA's own `imagePullSecrets`.

- **Insecure registries** — to support plain-HTTP in-cluster registries (needed by the integration test), gate plain-HTTP behind `FETCHER_ALLOW_INSECURE_REGISTRIES` — **amended:** a comma-separated host allowlist (stricter than a boolean), default empty (off).

- **New dependency** — add `github.com/google/go-containerregistry` to `go.mod`; `make tidy` (no vendor directory). It also provides `pkg/registry` (in-memory registry) and `crane` for tests.

### Poolmgr delivery — Path B (image-volume per-function pools, K8s 1.33+ opt-in)

Pre-warm pods per `(environment + image)` that already mount the code image read-only at `/userfunc`; specialization is then a load-only signal with no fetch/unzip.
`corev1.ImageVolumeSource` is `{Reference, PullPolicy}` (no SubPath — that goes on the `VolumeMount`), and the **kubelet** assembles pull secrets from node creds + the pod's `imagePullSecrets`, so Path B needs no userspace credential code or extra RBAC.

- **Capability gate** — **amended location:** `pkg/executor/util/imagevolume.go` (shared with newdeploy Phase 4, not poolmgr-private). `ImageVolumeSupported(disco)` (server GitVersion ≥ 1.33, parsing minor with a `+`-suffix trim) AND `OCIImageVolumeEnabled()` (env `ENABLE_OCI_IMAGE_VOLUME`). Evaluate once in `MakeGenericPoolManager` → `gpm.imageVolumeOK`. Expose the env flag in the executor chart Deployment.

- **Pool keying (`gpm.go`)** — change `pools` from `map[UID]` to a string-keyed map via `poolKey(envUID, ociImageHash)`, where an empty image yields `string(envUID)` — i.e. **byte-for-byte the current behavior** for non-OCI / Path A.
  **Amended 2026-06-10 (gaps found against code):** the change also touches `readyPodQueues` (keyed by env UID today) and the pod reconciler (`reconciler.go` routes purely on the `ENVIRONMENT_UID` pod label) — per-image pools need a `POOL_OCI_IMAGE_HASH` pod label and composite-key queue routing; and `CLEANUP_POOL`/`reconcileEnvPool`/`cleanupEnvPool` assume one pool per env and must iterate every pool whose key has the env-UID prefix.

- **Per-function eligibility (amended 2026-06-10):** Path B pods have no fetcher, so functions referencing Secrets/ConfigMaps and env-v1 functions CANNOT use Path B (the fetcher does `FetchSecretsAndCfgMaps` and the v1 load). `GetFuncSvc` decides per function: fall back to the generic pool (Path A) when `len(fn.Spec.Secrets)+len(fn.Spec.ConfigMaps) > 0` or `env.Spec.Version < 2`.

- **Deployment spec (`gp_deployment.go`)** — when `gp.ociImage != ""`: keep the env runtime image as the main container; append a `Volume` with `VolumeSource.Image = &apiv1.ImageVolumeSource{Reference: gp.ociImage, PullPolicy: IfNotPresent}` named after the shared userfunc volume; add a **read-only** `VolumeMount` at the shared mount path with `SubPath: gp.ociSubPath` on the main container; set `pod.Spec.ImagePullSecrets` from the OCI pull secrets; and **skip** `AddFetcherToPodSpec` + `MountFetcherSATokenOnFetcher` (there is no fetcher container). The pod-level `AutomountServiceAccountToken=false` invariant still holds. `getPoolName` appends a short image hash to disambiguate per-image pools (keep under 63 chars).

- **Specialize (`gp.go`)** — when `gp.ociImage != ""`, POST a load-only `FunctionLoadRequest` directly to the chosen pod's `:8888/v2/specialize` with `FilePath` = the shared mount root (code already mounted). Otherwise the existing fetcher `/specialize` path is unchanged.

- **Cost** — per-`(env, image)` pools multiply warm pods (N images for one env ⇒ N pools at `poolsize` each). Document keeping `poolsize` low for OCI environments, or rely on `AllowedFunctionsPerContainerInfinite` (which forces `poolsize=1`). This is the explicit trade for a near-zero-latency specialization with no download. No idle per-image-pool reaper exists yet — documented follow-up.

### Newdeploy delivery (Phase 4, amended 2026-06-10 — was a non-goal)

- **Path A needs no executor change.** Newdeploy embeds the same `NewSpecializeRequest` output in the fetcher's `-specialize-on-startup` arguments; the fetcher re-reads the live Package, so the Phase 2 OCI branch serves newdeploy automatically. Package-change rollouts already work through `PackageRef.ResourceVersion` → deployment annotation comparison. Phase 4A is verification + integration tests only.
- **Path B keeps the fetcher** (it still fetches Secrets/ConfigMaps and sends the load signal). `getDeploymentSpec` mounts the image volume read-only at exactly the fetcher's storePath (`<sharedMountPath>/<targetFilename>`) on both the fetcher and main containers; the fetcher's existing early-exit (`RootStat(storePath)`) skips the pull and proceeds to secrets + load — zero fetcher code change. `pod.Spec.ImagePullSecrets` gains the OCI pull secrets for the kubelet. Secrets/ConfigMaps and env-v1 are fine here, unlike poolmgr Path B.

### Path selection

- Non-OCI package (`Deployment.OCI == nil`) ⇒ `ociImage == ""` everywhere ⇒ unchanged behavior; the only added cost on the hot path is an O(1) nil check.
- OCI package + `imageVolumeOK == false` (cluster < 1.33 or flag off) ⇒ generic env pool, fetcher present ⇒ **Path A** (fetcher pulls). This is the default everywhere.
- OCI package + `imageVolumeOK == true` ⇒ per-image pool with the image volume, no fetcher ⇒ **Path B** (poolmgr; subject to the per-function eligibility above), or image-volume + fetcher load-only (newdeploy).

Path A's behavior is decided by the fetcher from the live package, independent of the capability gate; the gate only flips pool keying for Path B. The two paths are cleanly separated.

### CLI additions

Minimal, additive, referencing a pre-built image (no build/upload). Source-build + push (`--oci-push`, BuildKit) is a separate future RFC.

```
fission package create --name hello --env node --oci ghcr.io/myorg/hello-code:v1
fission fn create --name hello --pkg hello --entrypoint 'handler'
```

- New flag key `PkgOCI = "oci"` (`pkg/fission-cli/flag/key/key.go`) and flag struct `PkgOCI` (`pkg/fission-cli/flag/flag.go`); registered in the create + update commands of `cmd/package` and `cmd/function` (fn create reuses the same `Pkg*` structs).
- **Amended 2026-06-10:** the OCI archive is built inline in `CreatePackage` (which gains a trailing `ociImage` parameter), NOT short-circuited inside `CreateArchive` — `CreateArchive` is also the spec-mode upload path and globs its inputs immediately. A shared `ValidateArchiveSources` helper rejects `--oci` combined with `--code`/`--src`/`--deploy` in both `package create` and `fn create`. The CLI-set `BuildStatusNone` is cosmetic (the `/status` subresource strips client-set status on create); `Archive.IsEmpty` + `setInitialBuildStatus` is the load-bearing mechanism.
- Spec mode needs no change: `cmd/spec/apply.go:applyArchives` only rewrites `url`+`archive://` and `literal` archives; an `oci` archive is serialized into the spec YAML and applied verbatim (the regenerated CRD enum makes the server-side apply succeed).
- `SubPath` / `Digest` / `ImagePullSecrets` are available to spec-YAML users now; dedicated `--oci-subpath` / `--oci-pull-secret` flags are deferred.

### Helm / chart changes

- `charts/fission-all/templates/_function-access-role.tpl` — add `serviceaccounts: get` to the `fission-fetcher` Role (Path A).
- Executor Deployment — expose `ENABLE_OCI_IMAGE_VOLUME` (Path B opt-in).
- Optional fetcher env `FETCHER_ALLOW_INSECURE_REGISTRIES` (host allowlist) for plain-HTTP registries (default off).
- No `packageRegistry` Helm block is introduced in this RFC (it belonged to the deferred BuildKit builder).

### Observability

- Emit OTel span events around the OCI pull/extract and specialization (the fetcher already has spans on the fetch path — extend them; see `otelUtils` usage in `pkg/fetcher`).
- The `fission_function_coldstart_seconds` histogram is deferred: the fetcher binary carries no Prometheus registry today and adding a metrics stack to the sidecar is out of proportion for this RFC.

## Alternatives considered

1. **Extend `storagesvc` with layer caching.** Rejected: reinvents OCI badly, no cross-node sharing, no ecosystem tooling.
2. **Ship a Fission-bundled registry.** Rejected: operational burden, not our competence; users already have registries.
3. **Image-volume only (no fetcher-pull path).** Rejected as the sole mechanism: it requires K8s 1.33+ and makes pools per-function. The fetcher-pull baseline keeps the feature usable on the 1.32 floor and preserves generic warm pools.
4. **Fetcher-pull only (no image-volume path).** Insufficient: the fetcher pulls in userspace, so it gains registry tooling/dedup but not the kubelet cross-node layer cache — the headline cold-start win. The hybrid keeps both.
5. **Full runnable image as `containers[0].image`** (runtime + code baked into one image). Rejected for poolmgr: it discards the generic warm-pool model and the environment runtime image, and is essentially what the `container` executor already does.
6. **Nydus/estargz from day one.** Deferred: requires a snapshotter on every node, implicitly raising the floor. Plain OCI already wins; snapshotters are a later multiplier.

## Backward compatibility

Additive. `Archive.OCI` is a new optional field; existing literal/url packages behave exactly as today.
Existing CLI flags are unchanged.
The non-OCI hot path gains only an O(1) `OCI == nil` check (and `poolKey(uid, "")` is byte-identical to today's pool key).
Helm values for OCI support default off; the tarball path stays the out-of-the-box default.
The tarball / `storagesvc` path is **not** deprecated.

## Rollout phases

Each phase is an independently shippable, green PR. (Branches are pushed; PRs are opened by the maintainer.)

1. **Phase 1 — RFC + CRD foundation + CLI.** Update this RFC + `rfc/README.md`; add `OCIArchive`, `ArchiveTypeOCI`, CEL + Go validation, `IsEmpty`, codegen; add the `--oci` CLI flag. Unit tests + a registry-free `TestOCIPackageReconciles` integration test. No executor data-path change.
2. **Phase 2 — Poolmgr Path A.** Add go-containerregistry; `pkg/oci` + fetcher OCI branch; `kauth` creds + RBAC; insecure-registry allowlist; registry test infra + the live `TestOCIPackagePoolmgr` integration test. Works on K8s 1.32.
3. **Phase 3 — Poolmgr Path B.** Shared capability gate; pool keying by `(env, image)` incl. ready-pod queue routing; `ImageVolumeSource` deployment spec; load-only specialize; per-function eligibility fallback; unit tests + a 1.36-leg-gated integration test.
4. **Phase 4 — Newdeploy (amended in-scope).** Path A verification + `TestOCIPackageNewdeploy`/`TestOCIPackageNewdeployUpdate`; Path B image volume at the fetcher storePath with load-only via the fetcher early-exit; unit + gated integration tests.

Future RFCs (out of scope here): `container` executor OCI semantics (none needed); BuildKit source-build + `--oci-push`; lazy-loading snapshotters; per-image idle-pool reaper.

## Verification / test plan

Tests use the Go integration framework under `test/integration/` (build tag `//go:build integration`, testify, `framework.Connect(t)`, `ns.CLI(...)`, `f.Router(t).GetEventually(...)`) and fake-clientset unit tests — the legacy bash suite `test/tests/` was retired in 2026-05.

**Unit.**
- `pkg/apis/core/v1/validation_validators_test.go` — table-driven: `Archive.Validate()` accepts `oci`; rejects literal+oci / url+oci; `OCIArchive.Validate()` empty-image + bad-digest; `IsEmpty()` true/false; empty `Archive{}` still valid (backward-compat guard).
- `pkg/buildermgr/package_reconciler_test.go` — OCI deployment ⇒ `BuildStatusNone`; round-trip through the `newFissionFake` clientset.
- `pkg/fission-cli/cmd/package/package_test.go` — `ValidateArchiveSources` table; `CreatePackage` with `--oci` ⇒ `{Type: oci, OCI.Image}` with no file I/O.
- `pkg/oci/extract_test.go` + `keychain_test.go` — in-memory `go-containerregistry/pkg/registry`: files land; SubPath re-roots; digest mismatch errors; path-traversal + symlink/hardlink entries rejected; size cap; insecure default-off; SA+explicit pull-secret resolution.
- `pkg/fetcher/oci_test.go` — `Fetch()` end-to-end against the in-memory registry; idempotent second call; bad digest ⇒ 500.
- `pkg/executor/util/imagevolume_test.go` + poolmgr `gpm_test.go`/`gp_deployment_test.go` — capability table (1.32 false / 1.33+ true / `"33+"`); pool-key parity; Path B spec invariants; Path A parity guard.
- `pkg/executor/executortype/newdeploy/newdeploy_test.go` — Phase 4B image-volume spec + gate-off parity.

**Integration** (`test/integration/suites/common/oci_test.go`).
- `TestOCIPackageReconciles` (Phase 1, no registry) + `TestOCIPackageCELMutualExclusion`.
- `TestOCIPackagePoolmgr` + `TestOCIPackagePoolmgrDigestMismatch` (Phase 2, env-gated on the test registry).
- `TestOCIPackagePoolmgrImageVolume` + `TestOCIPathBFallbackWithSecrets` (Phase 3, additionally gated on `FISSION_TEST_IMAGE_VOLUME`, 1.36 CI leg).
- `TestOCIPackageNewdeploy`, `TestOCIPackageNewdeployUpdate`, `TestOCIPackageNewdeployImageVolume` (Phase 4).
- **Registry infra** (Phase 2) — test-only `registry:2` Deployment + Service in `.github/workflows/push_pr.yaml`, host pushes via port-forward, Packages store the in-cluster DNS reference. Env vars `FISSION_TEST_REGISTRY` / `FISSION_TEST_REGISTRY_INCLUSTER`; unset ⇒ skip. `framework.RequireRegistry(t)` + `framework/oci.go PushCodeImage` (crane). **Amended:** Phase 3's kubelet pulls additionally need `containerdConfigPatches` in `kind.yaml` (HTTP-registry trust) on the Path B leg — the original "no kind.yaml change" claim held only for Phase 2.

**Gates.** `make codegen && make generate-crds` clean (no diff); `make code-checks`; `make license-check`; `make test-run`. Existing integration suite stays green (two port-forwards + `FISSION_INTERNAL_AUTH_SECRET` per CLAUDE.md).

**CEL.** Apply a Package with two of literal/url/oci set and confirm the API server rejects it with the "at most one" message.

## Amendments (2026-06-10, pre-implementation review against the actual code)

1. Fetcher extraction goes to a tmp dir + rename to `storePath`, not the volume root (`LoadReq.FilePath` contract; idempotent early-exit).
2. Path B pool keying also touches `readyPodQueues` and the pod reconciler label routing — new `POOL_OCI_IMAGE_HASH` pod label.
3. `CLEANUP_POOL`/`reconcileEnvPool`/`cleanupEnvPool` must iterate all per-image pools of an env, not one.
4. Path B per-function eligibility fallback (Secrets/ConfigMaps, env-v1) — those functions stay on Path A.
5. CLI: OCI archive built inline in `CreatePackage` (new trailing param), not in `CreateArchive`; shared `ValidateArchiveSources`.
6. CLI-set `BuildStatus` is cosmetic; `IsEmpty()` is load-bearing.
7. ~~Phase 3 CI needs `kind.yaml` `containerdConfigPatches`~~ — DISPROVEN empirically (2026-06-10, kind v1.36.1): expose the test registry as NodePort 30500 and reference images as `localhost:30500/...`; containerd's built-in localhost exception allows plain HTTP and kube-proxy makes the NodePort node-resolvable.
    No kind.yaml change at all; ImageVolume also works out of the box on kindest/node v1.36.1 (no featureGates).
    Found at the same time: image-volume `subPath` must be a directory — kubelets reject file subpaths ("only directory subpath is supported"); `OCIArchive.SubPath` documents this.
8. Path B integration test gated to the 1.36 CI leg via `FISSION_TEST_IMAGE_VOLUME` + `FISSION_TEST_REGISTRY_NODE`.
    ~~(1.34 ships ImageVolume beta off-by-default)~~ — DISPROVEN empirically (kind v1.34.8, 2026-06-10): image volumes admit and mount with no feature gates there too, so the executor's ≥1.33 gate threshold is right; the single-leg gating is just coverage economy.
9. Read-only `/userfunc` may break runtimes that write beside code (`__pycache__`, JVM) — node fixture for tests; per-env compatibility documented.
10. Credentials via `pkg/authn/kubernetes` (kauth), not `k8schain` (cloud-SDK bloat in the static fetcher image).
11. `FETCHER_ALLOW_INSECURE_REGISTRIES` is a host allowlist, not a boolean.
12. Newdeploy moved from Non-goals into scope as Phase 4; capability gate relocated to shared `pkg/executor/util`; `pkg/oci` is a standalone package for cross-executor reuse.
13. (found during Phase 1) The Archive CEL rule cannot reference the byte-format `literal` field at all — even `has(self.literal)` makes the apiserver base64-convert the value, which rejects standard base64 containing `/` or `+`, breaking every inlined literal archive (proven by `test/e2e/fetcher` under envtest 1.32).
    The shipped CEL rule covers only url+oci; combinations involving `literal` are enforced by the webhook's Go `Archive.Validate()` with the same error message.
14. (found in the first full CI run, 2026-06-10) The function-pods NetworkPolicy admitted only the router on port 8888, silently dropping the executor's Path B load-only specialize call — the executor ingress rule now covers 8888 alongside the fetcher port 8000.
15. (same CI run) The digest pin was a no-op on Path B (no fetcher to verify it): the image-volume reference now embeds the digest (`repo:tag@sha256:...`) so the kubelet enforces the pin.
    Operational consequence worth documenting for users: enabling `executor.enableOCIImageVolume` switches every eligible OCI function from fetcher pulls (cluster-DNS resolvable, allowlist-governed) to kubelet pulls (node-resolver, containerd config) — registries reachable only via cluster DNS will stop resolving for those functions.
16. (second CI round) Poolmgr Path B must mount the image volume at the fetcher STORE path (`<sharedMountPath>/deployarchive`), not the shared mount root — `LoadReq.FilePath` names the store path and the env 500s on the missing directory.
    Corollary: `AllowedFunctionsPerContainerInfinite` envs are Path B-ineligible (their store path is per-function UID; one shared mount can't serve it) and fall back to the fetcher path.
17. (multi-agent review round, pre-merge) Five fixes landed:
    (a) pool identity now hashes ALL pod-spec-affecting archive fields (`ociPoolHash`: reference+digest, subPath, pull secrets) — keying by image alone let same-image/different-subPath functions alias to one pool and serve the wrong code root;
    (b) per-image pool deployments are adopted on executor restart (instanceID annotation patch) so the post-adopt reaper no longer destroys Path B warm pools;
    (c) both executors' `getFunctionOCIArchive` now return errors — only NotFound falls back to Path A; transient apiserver errors fail the reconcile/cold-start instead of silently downgrading the delivery mode (newdeploy would have rolled every pod onto a fetcher-path spec and reported success);
    (d) `package getdeploy` errors cleanly on OCI packages (was a nil-reader panic) and `package update --oci` resets a stale failed/pending build status via the /status subresource;
    (e) validation rejects Digest+`image@digest` conflicts, non-clean/absolute SubPath, and OCI on Source archives; `AddImageVolume` errors on zero-match containers/duplicate volumes; `loadOnlySpecialize` gained a per-attempt timeout and ctx-aware backoff.
    Documented follow-ups (not in this PR): per-image stale-pool reaper after `package update --oci` on Path B; serial-suite executor-restart test with a live Path B pool; spec save/apply round-trip test for OCI packages.

## Open questions

- Default tag policy: `:latest` vs digest-required. Lean digest-required in production (the `Digest` field exists), warn-only in dev.
- The runtime loader's exact expected code path inside `/userfunc` for each environment beyond node (drives Path B fixtures) — confirm per environment during Phase 2/3.
- Whether to add `--oci-subpath` / `--oci-pull-secret` CLI flags now or defer (deferred; the CRD fields already serve spec-YAML users).
- Per-image idle-pool reaper for Path B pools (documented follow-up).

# RFC-0001: OCI-Native Package Delivery

- Status: Proposed
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.(N+2)
- Requires: RFC-0003 (Package.Spec.Deployment.OCIRef field), Kubernetes 1.33+

## Summary

Deliver Fission function code as OCI artifacts in a container registry
instead of tarballs in storagesvc, as an opt-in alternative path. Functions
whose Package uses the new `OCIRef` field get image-native delivery: the
kubelet pulls and caches layers, nodes share layer cache, registry handles
distribution, and pod specialization becomes an OCI volume mount (KEP-4639)
or a direct image reference — no fetcher HTTP dance.

## Motivation

Today's cold start for a fresh pod on a fresh node is dominated by:

1. Fetcher HTTP GET of a full tarball from storagesvc (no layer cache,
   no dedup, no streaming).
2. Unzip into emptyDir.
3. Fetcher HTTP POST to the environment runtime's `/specialize` endpoint.
4. Environment runtime loads the code.

Steps 1–3 are pure overhead that Kubernetes + a registry handle natively
for container images. The tarball path also precludes:

- **Cross-node caching**: each node re-downloads. OCI gives us that for free
  via containerd's layer cache.
- **Deduplication across functions**: two functions on the same base +
  shared deps today ship two full tarballs. OCI layers dedup by digest.
- **Lazy loading**: nydus/stargz snapshotters can stream image layers on
  first read, turning a 200MB archive into a ~100ms cold start.
- **Standard tooling**: `docker push`, `crane`, `cosign`, registry RBAC,
  vulnerability scanners, retention policies.
- **Supply chain signing**: `cosign`-style signatures, SBOM attestations.

`storagesvc` remains for the legacy tarball path; it is not deprecated in
this RFC.

## Goals

- New optional field `Package.Spec.Deployment.OCIRef` referencing an OCI
  artifact.
- Executor paths for `newdeploy` and `container` that use the OCI image
  directly in the Pod spec — no fetcher sidecar.
- Executor path for `poolmgr` that mounts the OCI artifact via the
  `image` volume source (KEP-4639 beta 1.33) into pool pods — no fetcher
  call.
- A `buildermgr` path that produces an OCI artifact via BuildKit and pushes
  to a configurable registry, for users who supply source rather than a
  pre-built image.
- CLI: `fission package create --oci <image>` and
  `fission fn create --oci <image>` — opt-in, additive.
- Backward compat: all existing tarball-based packages keep working
  unchanged.

## Non-goals

- Removing the tarball / storagesvc path.
- Forcing any existing user to migrate.
- Building a new image registry (users bring their own: Harbor, GHCR, ECR,
  Artifact Registry, Docker Hub).
- Native image-volume support on K8s < 1.33 (declared out of scope via our
  1.33 floor).

## Design

### CRD additions (delivered in RFC-0003; listed here for reference)

```go
// Archive, extended
type Archive struct {
    // Existing fields (Type, Literal, URL, Checksum) unchanged.
    // ...
    // OCI references an OCI artifact holding the deployment.
    // Mutually exclusive with Literal / URL.
    // +optional
    OCI *OCIArchive `json:"oci,omitempty"`
}

type OCIArchive struct {
    // Image is a fully qualified OCI reference: registry/repo:tag[@digest].
    Image string `json:"image"`
    // ImagePullSecrets is passed through to the function's pod spec.
    // +optional
    ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
    // SubPath points inside the mounted OCI volume at the deployment
    // root. Defaults to "/".
    // +optional
    SubPath string `json:"subPath,omitempty"`
    // Digest is an optional content hash; if set, validated on pull.
    // +optional
    Digest string `json:"digest,omitempty"`
}
```

Validation (CEL, see RFC-0003):

```
// Package.Spec.Deployment: exactly one of Literal, URL, OCI must be set.
self.literal != '' ? (self.url == '' && !has(self.oci)) : true
```

### Executor paths

**newdeploy + container** — straightforward. The Deployment's pod spec
sets `containers[0].image = OCIRef.Image` directly. No fetcher sidecar.
`ImagePullSecrets` propagated from OCIRef. Keep fetcher sidecar only when
Package uses the legacy tarball path.

File: `pkg/executor/executortype/newdeploy/newdeploymgr.go`
      `pkg/executor/executortype/container/containermgr.go`

**poolmgr** — pool pods are generic per-environment. On specialization
today, fetcher downloads the tarball and unzips into a shared emptyDir.
Replacement: add an **image volume source** (`corev1.ImageVolumeSource`,
KEP-4639, beta in 1.33) to the pool pod's volumes, mounted read-only at the
same path the environment runtime expects. Specialization becomes a
one-liner: add the volume + mount when the pod is claimed for a specific
function, via the existing "specialize" pathway but without the HTTP fetch.

Key subtleties:

- Image volumes are immutable after pod creation. We either (a) recreate
  the pool pod per specialization (regresses cold start) or (b) run the
  function code from an **ephemeral container** with an image volume
  mount — ephemeral containers can reference image volumes too.
- Cleaner alternative: the pool pod declares an image volume at **pod
  creation** but with an *unresolved reference* — not supported today,
  which means option (a) with pre-warmed pods per known function is
  effectively what this becomes.
- Recommended path for poolmgr v1 of this RFC: **do not** use image volumes
  in poolmgr. Instead: OCI-based packages are served exclusively by a new
  `oci-poolmgr` variant that pre-creates pool pods *per function* but uses
  image layer cache + zero unzip as the cold-start win. For the MVP,
  keep poolmgr on tarballs and ship OCI delivery for newdeploy/container
  first. Revisit poolmgr in a follow-up RFC once OCI volume sources gain
  mutable-mount capability.

### buildermgr path

When a user submits source (not an OCI ref), buildermgr today runs the
per-env builder sidecar to produce a deployment tarball. Add a new
**BuildKit-based builder variant** that:

1. Runs a BuildKit daemon (rootless, KEP-compliant) in the builder pod.
2. Reads source from the existing fetch path.
3. Produces an OCI image layered on the environment's runtime image.
4. Pushes to a configurable registry (Helm value:
   `packageRegistry.url`, `packageRegistry.imagePullSecret`).
5. Writes the resulting reference into `Package.Spec.Deployment.OCI.Image`
   and `Package.Status` condition `OCIBuildReady=True`.

Backward compat: users who want tarballs keep the current builder. BuildKit
variant is opted into by setting `Environment.Spec.Builder.Kind = "buildkit"`
(new enum field).

File: `pkg/buildermgr/`, new subpackage `pkg/buildermgr/buildkit/`.

### CLI additions

```
fission package create --name hello --env node \
    --oci ghcr.io/myorg/hello:v1

fission fn create --name hello --pkg hello \
    --entrypoint 'handler'
```

Source-based create (BuildKit path) with an automatic push:

```
fission fn create --name hello --env node \
    --src hello.zip \
    --oci-push ghcr.io/myorg/hello:v1
```

### Helm / chart changes

- `values.yaml`: new `packageRegistry` block (url, pullSecret, default
  namespace for pushed artifacts).
- New RBAC for buildermgr to read the pullSecret when BuildKit variant
  is enabled.

### Observability

- Record per-stage cold-start timings (image pull, pod start, first
  request) as histograms: `fission_function_coldstart_seconds{stage=...}`.
- Emit OTel spans around image pull, specialization, first-request.
- Expose `Package` status conditions:
  `OCIBuildSucceeded`, `OCIBuildFailed` with image digest and size.

## Alternatives considered

1. **Extend storagesvc with layer caching.** Rejected: reinvents OCI badly,
   doesn't get cross-node sharing for free, no ecosystem tooling.
2. **Ship a Fission-bundled registry.** Rejected: operational burden, not
   our competence, users already have registries.
3. **Nydus/estargz from day one.** Reasonable but requires a snapshotter
   installed on every node, raising the K8s floor implicitly. Deferred to
   a follow-up — plain OCI already wins; nydus adds an optional perf
   multiplier later.

## Backward compatibility

Additive. `Package.Spec.Deployment.OCI` is a new optional field. Existing
Packages with `Literal` or `URL` behave exactly as today. Existing CLI
flags unchanged. Helm chart values for OCI support default off — tarball
path remains the out-of-the-box default.

Deprecation: tarball path is **not** deprecated in this RFC.

## Rollout phases

1. **Phase 1 — CRD + validation.** Add `OCIArchive` type behind RFC-0003
   CRD update. CEL rules added. No executor changes yet. Ships in v1.N.
2. **Phase 2 — newdeploy/container executor paths.** Executor branches on
   `Package.Spec.Deployment.OCI != nil` and skips fetcher for these two
   types. Ships in v1.N.
3. **Phase 3 — CLI support.** `fission package create --oci`,
   `fission fn create --oci`. Ships in v1.N.
4. **Phase 4 — BuildKit builder variant.** New `Environment.Spec.Builder.Kind`.
   Ships in v1.(N+1).
5. **Phase 5 — poolmgr OCI path (image volumes).** Ships in v1.(N+2)
   pending KEP-4639 behavior on mutable mounts. If blocked, lands as a
   separate RFC.

## Verification

- **Unit**: validation rejects mutually-exclusive combinations; OCIRef
  round-trips through the CRD API.
- **Envtest**: controller writes OCIBuildSucceeded condition on a mocked
  push.
- **E2E (`test/tests/`)**: new `test_oci_hello_http.sh` — pre-built image
  in a local registry (kind + registry service), function deploys, HTTP
  request returns `hello`.
- **Benchmark**: cold-start p50/p95/p99 for a 50MB node function, same
  environment, same node. Targets:
  - tarball path: current baseline
  - OCI path (cold layer cache): ≥ 30% reduction
  - OCI path (warm layer cache): ≥ 70% reduction
- **Cross-node**: run same function across 5 nodes, measure aggregate pull
  bandwidth vs tarball path. Expect ≥ 50% reduction once base layers are
  cached.
- **Regression**: full existing E2E matrix continues to pass with tarball
  Packages.

## Open questions

- Registry auth UX: do we want Fission to manage pull secrets, or rely on
  cluster-default imagePullSecrets on the ServiceAccount? Lean toward the
  latter.
- Default tag policy: `:latest` or digest-required? Lean digest-required
  in production, warn-only in dev.
- Who owns BuildKit daemon lifecycle — per-build pod or long-lived
  Deployment? Per-build keeps blast radius small; long-lived amortizes
  daemon startup. Start per-build, revisit with data.

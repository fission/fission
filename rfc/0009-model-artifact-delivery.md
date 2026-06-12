# RFC-0009: Model Artifact Delivery & Caching

- Status: Proposed
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.(N+1)
- Requires: Kubernetes 1.32 (current floor) for the DaemonSet-prepuller / fetcher-pull path; Kubernetes 1.33+ (KEP-4639 image volumes) as an opt-in for the OCI image-volume path. Builds on **RFC-0001** (OCI-Native Package Delivery) for its OCI pull, keychain, and image-volume mechanics; consumed by **RFC-0010** (GPU-Native Inference). Self-contained at the CRD level.

## Summary

Add a cluster-scoped `Model` CRD and a node-local model cache so AI-inference functions can mount
multi-GB model weights (safetensors / GGUF / ONNX / HF transformers) read-only without baking them
into the runtime image or re-downloading them on every cold start. Weights are pulled once per node
by a DaemonSet prepuller (with a fetcher init-container fallback on the 1.32 floor and an opt-in
OCI image-volume path on 1.33+), digest-verified, and shared across all function pods on the node
via a read-only mount applied **after** `MergePodSpec`.

## Motivation

Inference functions are the fastest-growing Fission workload, and they are structurally hostile to
the current package model. A Fission `Package` bundles **code**; model weights have nowhere to go,
so today users pick one of two bad options:

1. **Bake weights into the runtime image.** A `FROM python ... COPY model/ /model` image is 5–25 GB.
   Builds are slow, the registry bloats, and every `fission env create`/update re-pushes gigabytes.
   The image-volume win from RFC-0001 does not help: the weights are in the *runtime* image, which
   is the pod's main container and is re-pulled on each new node regardless of code delivery.
2. **Download weights at cold start.** The function `__init__` calls
   `transformers.from_pretrained(...)` or `huggingface_hub.snapshot_download(...)` against HF / S3.
   On a fresh pod this adds **minutes** of latency, repeated per pod (no node-local sharing), and
   incurs egress cost plus HF rate-limit/429s under fan-out. Poolmgr's warm-pool model is defeated:
   a "warm" pod is not warm if it still has to fetch 8 GB on first request.

There is **no node-local, content-addressed model cache shared across pods** in Fission today.
RFC-0001 solved exactly this shape of problem for *code* (content-addressed, node-cached, dedup'd
delivery). This RFC reuses that substrate for *weights*, which are larger, change far less often
than code, and are naturally shared across many functions (a dozen functions all using
`Llama-3.1-8B`). Why now: RFC-0001 lands the OCI pull/extract/keychain plumbing, RFC-0010 needs a
stable artifact handle to schedule GPU pods against, and the 1.33 image-volume path is now within
one minor of the floor (CI already exercises 1.34/1.36).

## Goals

- A new content-addressed `Model` CRD describing a weight artifact (source + digest + size + format),
  decoupled from `Package`/code and shared across functions.
- **Node-local cache**, populated **once per node** and **read-only shared** across every function
  pod on that node — the primary mechanism is a **DaemonSet prepuller** writing to a per-node cache
  directory, working on the 1.32 floor.
- Two additional opt-in delivery paths reusing RFC-0001: an **init-container fetcher pull** (lazy,
  first-pod-on-node, 1.32 floor) and an **OCI image-volume** path (KEP-4639, 1.33+, capability-gated).
- **Integrity**: verify the declared digest before any pod mounts the artifact; surface
  `Pulling`/`Ready`/`Failed` conditions on `Model.Status`.
- Functions/environments reference models by name (`modelRefs`); models mount read-only at
  `/models/<name>`, applied **after** `MergePodSpec` (security invariant).
- **Eviction/GC**: LRU over the node cache against a configurable size budget, owned by the
  prepuller DaemonSet agent.
- Backward compatible and additive: no `modelRefs` ⇒ byte-for-byte current behavior.

## Non-goals

- **Training, fine-tuning, or producing weights.** A `Model` references a pre-existing artifact.
- **Serving/inference runtime.** Loading weights into a model server (vLLM, TGI, llama.cpp) is the
  function/environment's job; this RFC only makes the bytes present on disk. Scheduling those pods
  onto GPUs is **RFC-0010**.
- **A Kubernetes floor bump.** The image-volume path is opt-in + capability-gated exactly as in
  RFC-0001; the DaemonSet-prepuller and init-container paths work on 1.32.
- **Hosting a model registry or mirror.** Users bring their own source (HF Hub, S3, OCI registry,
  HTTP). Fission caches, it does not host.
- **Per-layer lazy streaming of weights** (nydus/estargz). Plain pull + node cache already removes
  the headline cost; a later multiplier.
- **Mutating models in place.** A `Model` is immutable once `Ready`; a new revision is a new `Model`
  (or a new `revision`/`digest`), mirroring image immutability.

## Design

### Why cluster-scoped

`Model` is **cluster-scoped**. The artifact is content-addressed (a digest) and the cache it feeds
is **node-local and node-shared** — a node cannot hold two different copies of the same digest, and
two namespaces requesting the same `Llama-3.1-8B` digest must dedup to one on-disk copy. A namespaced
`Model` would force per-namespace cache entries for identical bytes, defeating the central goal
(dedup + single pull per node). This mirrors how other node-scoped artifacts (images, `RuntimeClass`,
`CSIDriver`) are cluster-scoped. Access is gated by RBAC on the cluster-scoped resource plus the
function-side `modelRefs` reference (a function may only mount models its namespace's
`fission-fetcher`/executor SA is allowed to read — see RBAC below). `CanaryConfig` and `Environment`
precedent for namespaced CRDs does not apply because those are not content-addressed node artifacts.

### CRD (`pkg/apis/core/v1/types.go`)

Follow the 10-step new-CRD checklist at the top of `types.go` (create spec → type → list → Object/List
methods → `configureClient` in `pkg/crd/client.go` → `EnsureFissionCRDs` in `pkg/crd/crd.go` →
`crd_test.go` → CRUD interface `pkg/crd/model.go` → `FissionClient` getter →
`make codegen && make generate-crds`). The package imports `k8s.io/api/core/v1` as `apiv1`.

```go
// Model is a cluster-scoped, content-addressed weight artifact cached node-locally
// and mounted read-only into function pods that reference it.
// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope="Cluster",singular="model",shortName={model}
// +kubebuilder:printcolumn:name="Format",type=string,JSONPath=`.spec.format`
// +kubebuilder:printcolumn:name="Size",type=string,JSONPath=`.spec.sizeBytes`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type Model struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata"`

    Spec ModelSpec `json:"spec"`
    // +optional
    Status ModelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ModelList struct {
    metav1.TypeMeta `json:",inline"`
    metav1.ListMeta `json:"metadata"`
    Items           []Model `json:"items"`
}

// ModelSpec describes a single immutable weight artifact and where to obtain it.
// Exactly one of OCI / HTTP / S3 / HF must be set.
// +kubebuilder:validation:XValidation:rule="(has(self.oci)?1:0)+(has(self.http)?1:0)+(has(self.s3)?1:0)+(has(self.hf)?1:0) == 1",message="exactly one of oci, http, s3, or hf must be set"
type ModelSpec struct {
    // Format is a hint to the consuming runtime; Fission does not interpret the bytes.
    // +kubebuilder:validation:Enum=safetensors;gguf;onnx;pytorch;transformers;raw
    Format string `json:"format"`

    // Digest is the REQUIRED content hash of the assembled artifact, verified before
    // any pod is allowed to mount it. For OCI sources this is the manifest digest;
    // for http/s3/hf it is the sha256 of the resolved file tree (see Integrity).
    // +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
    Digest string `json:"digest"`

    // SizeBytes is an operator-supplied size hint used by the cache LRU planner and
    // by RFC-0010 for GPU node fit. Advisory; the agent records the real size in status.
    // +optional
    // +kubebuilder:validation:Minimum=0
    SizeBytes int64 `json:"sizeBytes,omitempty"`

    // Exactly one source. All fields below are +optional; the XValidation rule above
    // enforces "exactly one set".
    // +optional
    OCI *OCIArchive `json:"oci,omitempty"` // reuses RFC-0001's OCIArchive verbatim
    // +optional
    HTTP *ModelHTTPSource `json:"http,omitempty"`
    // +optional
    S3 *ModelS3Source `json:"s3,omitempty"`
    // +optional
    HF *ModelHFSource `json:"hf,omitempty"`
}

type ModelHTTPSource struct {
    // +kubebuilder:validation:MinLength=1
    URL string `json:"url"`
    // SecretRef names a Secret (in fission's controller namespace) with optional
    // basic-auth / bearer keys (username, password, token).
    // +optional
    SecretRef *apiv1.LocalObjectReference `json:"secretRef,omitempty"`
}

type ModelS3Source struct {
    // Bucket/Key/Region/Endpoint mirror storagesvc's stow S3 config so the same
    // credential plumbing (pkg/storagesvc) is reused.
    // +kubebuilder:validation:MinLength=1
    Bucket string `json:"bucket"`
    // +kubebuilder:validation:MinLength=1
    Key      string `json:"key"`            // object key or prefix (a model dir)
    Region   string `json:"region,omitempty"`
    Endpoint string `json:"endpoint,omitempty"` // for S3-compatible stores (MinIO)
    // +optional
    SecretRef *apiv1.LocalObjectReference `json:"secretRef,omitempty"`
}

type ModelHFSource struct {
    // Repo is the Hugging Face repo id, e.g. "meta-llama/Llama-3.1-8B".
    // +kubebuilder:validation:MinLength=1
    Repo string `json:"repo"`
    // Revision pins an immutable commit; a branch/tag is allowed but discouraged
    // (the resolved commit is recorded in status.resolvedRevision).
    // +optional
    Revision string `json:"revision,omitempty"`
    // Include/Exclude glob filters (HF allow_patterns / ignore_patterns) so a repo
    // can be narrowed to a single weight file.
    // +optional
    Include []string `json:"include,omitempty"`
    // +optional
    Exclude []string `json:"exclude,omitempty"`
    // SecretRef names a Secret with key "token" for gated/private repos.
    // +optional
    SecretRef *apiv1.LocalObjectReference `json:"secretRef,omitempty"`
}

// ModelPhase is a coarse, printable rollup of Status.Conditions.
// +kubebuilder:validation:Enum=Pending;Pulling;Ready;Failed
type ModelPhase string

const (
    ModelPhasePending ModelPhase = "Pending"
    ModelPhasePulling ModelPhase = "Pulling"
    ModelPhaseReady   ModelPhase = "Ready"
    ModelPhaseFailed  ModelPhase = "Failed"
)

type ModelStatus struct {
    // +optional
    Phase ModelPhase `json:"phase,omitempty"`
    // ObservedSizeBytes is the real on-disk size after the first successful pull.
    // +optional
    ObservedSizeBytes int64 `json:"observedSizeBytes,omitempty"`
    // ResolvedRevision is the immutable commit a mutable HF revision resolved to.
    // +optional
    ResolvedRevision string `json:"resolvedRevision,omitempty"`
    // NodesReady counts nodes that have the digest cached and verified.
    // +optional
    NodesReady int32 `json:"nodesReady,omitempty"`
    // Conditions: types "Pulling", "Ready", "Failed" (metav1.Condition).
    // +optional
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

New constant in `pkg/apis/core/v1/const.go` (default mount root):

```go
DefaultModelMountPath = "/models"
```

**Validation (Go, `pkg/apis/core/v1/validation.go`).** Add `ModelSpec.Validate()` (exactly-one
source backstop, digest format mirroring `Checksum.Validate`, per-source `URL`/`Bucket`/`Repo`
non-empty) and `Model.Validate()`. The exactly-one-source CEL rule is cheap (four `has()` ops summed,
nowhere near the pod-spec budget) and stays in `types.go`. Cross-object checks (does the referenced
`Secret` exist) are not expressible in CEL and are **not** added to the webhook either — the model
reconciler surfaces a missing-secret as a `Failed` condition, not an admission rejection (an operator
may legitimately create the `Model` before the `Secret`).

**`modelRefs` on the consuming side (`FunctionSpec`).** Add an optional field to `FunctionSpec` (the
same struct that already carries `Secrets`, `ConfigMaps`, `Resources`, `PodSpec`):

```go
// ModelRefs lists cluster-scoped Models to mount read-only into this function's
// pods. Each mounts at <DefaultModelMountPath>/<name> (override via MountPath).
// +optional
// +listType=map
// +listMapKey=name
ModelRefs []ModelReference `json:"modelRefs,omitempty"`

type ModelReference struct {
    // Name of a cluster-scoped Model.
    // +kubebuilder:validation:MinLength=1
    Name string `json:"name"`
    // MountPath overrides the default /models/<name>. Must be absolute and outside
    // the Fission-reserved shared volume mounts (/userfunc, /packages, /secrets,
    // /configs) — enforced in ModelReference.Validate, not CEL.
    // +optional
    MountPath string `json:"mountPath,omitempty"`
}
```

`modelRefs` goes on `FunctionSpec` (not `EnvironmentSpec`): a model is a per-function concern (two
functions sharing an env can want different models), and poolmgr specializes a generic env pool —
attaching weights to the env would force per-model warm pools the way RFC-0001's Path B forces
per-image pools. For the env-pool case we mount at **specialize** time on the chosen pod's
controller-created deployment is not possible (warm pods are pre-created), so for **poolmgr**,
`modelRefs` triggers the **newdeploy-style** per-function deployment with the model volume, OR (1.33+)
a per-`(env, modelSet)` image-volume pool. The default and simplest path is **newdeploy/container**,
where a Deployment is created per function and the model volumes are added to that Deployment's
PodSpec. Poolmgr image-volume model pools are an opt-in follow-up phase (see Rollout).

### Delivery mechanics (reuse RFC-0001)

A per-node cache directory `--model-cache-dir` (default `/var/lib/fission/models`, backed by a
`hostPath` or a node-local PVC) holds **content-addressed** subtrees keyed by digest:
`/<cache>/<sha256>/...`. Function pods mount the **digest-keyed** subtree read-only — so the same
bytes are shared by every pod on the node and never written by a function. Three population paths:

**Primary — DaemonSet prepuller (`pkg/modelmgr`, 1.32 floor, recommended default).** A new
DaemonSet `fission-model-agent` runs `fission-bundle --modelAgent` on every (eligible) node. Each
agent watches `Model` objects (and the function `modelRefs` that target its node's workloads, via a
field-selected informer) and, for any model scheduled to land on its node, pulls the source into the
cache, verifies the digest, and reports readiness. The DaemonSet is the **owner** of the cache:
population, integrity, and LRU/GC all live in one place, the artifact is present **before** the
function pod is scheduled (no first-request stall), and it works on the floor. This is recommended
because it gives the closest analogue to the kubelet image cache while staying portable.

**Fallback — init-container fetcher pull (1.32 floor, opt-in `lazy` policy).** When the prepuller is
disabled or a model is marked `pullPolicy: OnDemand`, the executor injects an **init container**
(the RFC-0001 `fetcher` image, reusing `extractOCIImage` / the same go-containerregistry + k8schain
keychain from `pkg/fetcher/oci.go`, extended with an HF/S3/HTTP fetch) into the function pod. The
init container populates the shared node cache dir (guarded by a per-digest flock so concurrent first
pods don't double-pull) and the main container mounts it read-only. Slower for the first pod on a
node but needs no DaemonSet and reuses RFC-0001's pull code paths directly.

**Opt-in optimization — OCI image-volume (KEP-4639, K8s 1.33+, capability-gated).** When the source
is `OCI` and the cluster supports image volumes (the exact `ImageVolumeSupported(disco)` gate from
RFC-0001's `pkg/executor/executortype/poolmgr/capability.go`, GitVersion ≥ 1.33, plus env
`ENABLE_OCI_IMAGE_VOLUME`), the model mounts directly as a `corev1.ImageVolumeSource{Reference,
PullPolicy}` — the **kubelet** pulls, caches cross-node, resolves pull secrets, and we add no cache
dir, no DaemonSet, no userspace credential code. This is the cleanest path but requires 1.33+ and an
OCI-packaged model, so it is strictly opt-in; the prepuller remains the default on the floor.

Path selection: `OCI source + image-volume capability ⇒ image-volume`; else `prepuller enabled ⇒
DaemonSet`; else `init-container fetcher`. A non-OCI source on a ≥1.33 cluster still uses the
prepuller/init paths (image volumes require an OCI reference).

### Integrity

The agent (or init container) verifies the declared `Spec.Digest` **before** the artifact is marked
`Ready` and **before** any pod is allowed to mount it:

- OCI: compare the pulled manifest digest to `Spec.Digest` (RFC-0001's `extractOCIImage` already does
  `img.Digest()` comparison).
- HTTP / S3 / HF: compute a deterministic sha256 over the canonical file tree (sorted relative paths
  + per-file content), reusing the same zip-slip-safe extraction posture as `pkg/utils/zip.go`
  (`os.OpenRoot` + per-entry `filepath.Clean`, reject `..`/absolute/symlink/hardlink). For HF, the
  resolved commit is recorded in `Status.ResolvedRevision`.

A mismatch sets the `Failed` condition with the observed-vs-expected digest and the artifact is
quarantined (moved to `/<cache>/.failed/<sha256>`), never mounted. Status conditions flow
`Pending → Pulling → Ready` (or `→ Failed`), and `Status.Phase` rolls them up for `kubectl`/CLI.

### Mounting (security invariant — after `MergePodSpec`)

Model volumes and mounts are added to the function pod **after every `MergePodSpec` call** and are
always read-only, mirroring the existing GHSA-85g2 re-clamp pattern at
`pkg/executor/executortype/poolmgr/gp_deployment.go:160`/`:206` and the equivalent
`newdeploy.go:222`/`:290` and `container/deployment.go:192` sites. A user-supplied `PodSpec` patch
cannot redirect, re-`writable`, or remove the model mount because the mount is applied last:

```go
// In a small util applied AFTER the final MergePodSpec in each executor type:
func ApplyModelVolumes(spec *apiv1.PodSpec, mainContainer string,
    refs []fv1.ModelReference, resolve func(name string) (volume apiv1.Volume, sub string)) {
    for _, ref := range refs {
        vol, subPath := resolve(ref.Name) // hostPath subPath=<digest>, or ImageVolumeSource
        spec.Volumes = append(spec.Volumes, vol)
        mountPath := ref.MountPath
        if mountPath == "" {
            mountPath = path.Join(fv1.DefaultModelMountPath, ref.Name)
        }
        addReadOnlyMount(spec, mainContainer, apiv1.VolumeMount{
            Name: vol.Name, MountPath: mountPath, SubPath: subPath, ReadOnly: true,
        })
    }
}
```

`resolve` returns either a `hostPath` volume rooted at `--model-cache-dir` with `subPath=<digest>`
(prepuller/init paths) or an `ImageVolumeSource{Reference: ociRef, PullPolicy: IfNotPresent}` (1.33+
path). `ReadOnly: true` is non-negotiable and set by `ApplyModelVolumes`, not by the patch.

### Eviction / GC (LRU, owned by the agent)

The DaemonSet agent owns a node-local LRU over `--model-cache-dir` with budget
`--model-cache-max-bytes` (Helm `modelCache.maxBytes`, default `0` = unbounded). On each successful
pull, and on a periodic sweep, the agent: (1) records an `atime`/last-mount timestamp per digest
sidecar file; (2) when total size + incoming size > budget, evicts least-recently-mounted digests
**that no Pod on the node currently mounts** (checked via the node's Pod list + `modelRefs` → digest
map); (3) never evicts a digest with `nodeMountCount > 0`. A digest in active use cannot be evicted,
so a running function never loses its weights mid-flight. The init-container path participates by
touching the same timestamp sidecar; the agent GC still owns reclamation (the init path is only a
populator). The image-volume path's cache is the kubelet's — Fission does not GC it (kubelet image GC
owns it), which is one more reason the prepuller is the recommended default for predictable cache
control.

### Wiring (controller, storagesvc, RBAC)

- **Reconciler.** A new `--modelmgr` subsystem in `cmd/fission-bundle/main.go` dispatching to
  `pkg/modelmgr.Start`, built as a controller-runtime reconciler per the RFC-0004 consolidation
  pattern (one manager, `.Watches(&fv1.Model{})`, no bespoke informer factory). It reconciles
  `Model.Status` rollup (aggregating per-node agent readiness reported via the agent updating a
  per-node condition or a `Lease`/annotation), validates source secrets, and resolves HF revisions.
  The per-node **agent** is the `--modelAgent` DaemonSet flag (distinct from the cluster `--modelmgr`
  reconciler): agent = data plane (pull/verify/GC on its node), modelmgr = control plane (status
  aggregation, resolution). Both are the same `fission-bundle` binary, dispatched by flag, exactly
  like every other subsystem.
- **storagesvc interplay (`pkg/storagesvc`).** For `S3` and `HTTP` sources, the agent reuses
  storagesvc's existing stow-backed client construction (`MakeStorageService` shares the same S3
  config shape: bucket/region/endpoint/credentials). storagesvc is **not** in the data path for the
  large pull (the agent streams source→cache directly, never round-tripping multi-GB through
  storagesvc), but the same credential/stow plumbing is reused so we don't grow a second S3 client.
  OCI sources reuse RFC-0001's go-containerregistry path verbatim.
- **CRD registration.** `pkg/crd/client.go` `configureClient` + a `GetModel()` getter on
  `FissionClient`; `pkg/crd/crd.go` `EnsureFissionCRDs`; new `pkg/crd/model.go` CRUD interface;
  `crds/v1/fission.io_models.yaml` generated by `make generate-crds`.
- **RBAC (`charts/fission-all/`).** New cluster role rules for the modelmgr/agent SA:
  `models` + `models/status` (get/list/watch/update/patch) and `secrets: get` (model source creds);
  the agent additionally needs `pods: list` (node-local mount accounting for GC) field-selected to
  its node, and `nodes: get`. The function-side executor SA needs `models: get/list/watch` to resolve
  `modelRefs` → digest. No new permissions on the function `fission-fetcher` SA beyond RFC-0001's
  `serviceaccounts: get`/`secrets: get` for the init-container path.

### Helm (`charts/fission-all/`)

- `values.yaml`: a `modelCache` block — `modelCache.enabled: false` (gates the DaemonSet + reconciler),
  `modelCache.hostPath: /var/lib/fission/models`, `modelCache.maxBytes: 0`,
  `modelCache.prepuller: true` (false ⇒ init-container fallback only),
  `modelCache.imageVolume: false` (1.33+ opt-in, reuses RFC-0001's `ENABLE_OCI_IMAGE_VOLUME`).
- New DaemonSet template `charts/fission-all/templates/model-agent/` deploying `fission-bundle
  --modelAgent --model-cache-dir=... --model-cache-max-bytes=...` with the cache `hostPath` mounted
  read-write **only** on the agent (function pods mount it read-only).
- New Deployment for `fission-bundle --modelmgr` (singleton, leader-elected like other reconcilers).
- All default off ⇒ zero change for clusters that don't opt in.

### CLI (`pkg/fission-cli/cmd/model/`)

New `fission model` command group (analogous to `cmd/environment`), wired into `pkg/fission-cli/cmd/cmd.go`:

```
fission model create --name llama31-8b --format safetensors \
    --hf-repo meta-llama/Llama-3.1-8B --hf-revision <commit> \
    --digest sha256:... --size 16000000000
fission model create --name phi3 --oci ghcr.io/myorg/phi3-weights:v1 --digest sha256:...
fission model get  --name llama31-8b      # shows Phase, NodesReady, conditions
fission model list
fission model delete --name llama31-8b
fission fn create --name chat --env vllm --pkg chat --model llama31-8b   # repeatable --model
```

New flag keys in `pkg/fission-cli/flag/key/key.go` (`ModelName`, `ModelFormat`, `ModelHFRepo`,
`ModelHFRevision`, `ModelOCI`, `ModelHTTP`, `ModelS3*`, `ModelDigest`, `ModelSize`) and `--model`
(repeatable) registered on `cmd/function` create/update to populate `FunctionSpec.ModelRefs`. The CLI
talks to the generated clientset directly (creating the `Model` CR), as all Fission CLI commands do.

## Alternatives considered

- **Reuse `Package`/`Archive` for weights (no new CRD).** Rejected: a `Package` is namespaced and
  code-shaped (build status, env ref, source→deploy lifecycle); a 16 GB content-addressed
  node-shared artifact has none of that and would force per-namespace duplicate caches, defeating
  dedup. A dedicated cluster-scoped CRD is the right altitude.
- **Bake weights into the runtime image (status quo).** Rejected as the *only* path: slow builds,
  registry bloat, and the runtime image is re-pulled per node regardless of RFC-0001. (It remains a
  user's prerogative — Fission doesn't forbid it — but it is not a first-class mechanism here.)
- **Download in the function at cold start (status quo).** Rejected: per-pod latency, no node
  sharing, egress cost, HF rate limits. This RFC exists to replace it.
- **A central shared `ReadWriteMany` PVC instead of per-node cache.** Rejected: RWX storage (NFS/EFS)
  adds a hard dependency, a network hop on every weight read (killing the cold-start win the node
  cache provides), and a single failure domain. Node-local content-addressed cache mirrors the
  kubelet image cache and needs no cluster storage class.
- **DaemonSet prepuller vs init-container-on-first-pod vs image-volume — pick one only.** Rejected in
  favor of all three with a clear default: prepuller (recommended, 1.32, predictable cache control),
  init-container (1.32 fallback, no DaemonSet, reuses RFC-0001 fetcher), image-volume (1.33+ opt-in,
  kubelet does everything). Each covers a real constraint; collapsing to one drops either floor
  support or the kubelet-cache optimization, exactly the RFC-0001 hybrid trade-off.
- **Namespaced `Model`.** Rejected: see "Why cluster-scoped" — content-addressed node artifacts can't
  dedup if scoped per namespace.

## Backward compatibility

Purely additive. `Model` is a new CRD; `FunctionSpec.ModelRefs` is a new `+optional` `+listType=map`
field — old clients and stored Functions round-trip (no `modelRefs` ⇒ no model volumes ⇒ byte-for-byte
current pod spec, the model-volume util is a no-op on an empty slice). No `Model` objects and
`modelCache.enabled: false` ⇒ no DaemonSet, no reconciler, zero behavior change. Existing CLI flags,
Helm values, and the package/tarball/OCI delivery paths are untouched. The image-volume model path
reuses RFC-0001's existing `ENABLE_OCI_IMAGE_VOLUME` gate and adds no new floor requirement.

## Rollout phases

Each phase is an independently shippable, green PR; the first compiles and is inert. (Branches are
pushed; PRs are opened by the maintainer.) This RFC assumes RFC-0001 phase 1–2 have landed (OCI
archive types + `extractOCIImage` + k8schain), and reuses them.

1. **Phase 1 — RFC + CRD foundation + CLI (compiles, inert).** This RFC + `rfc/README.md` index;
   add `Model`/`ModelList`/`ModelSpec`/`ModelStatus` + source types + `ModelReference` +
   `FunctionSpec.ModelRefs` + `DefaultModelMountPath`; Go + CEL validation; the 10-step registration
   (`crd/client.go`, `crd/crd.go`, `crd/model.go`, getter); `make codegen && make generate-crds`;
   `fission model create/get/list/delete` + `fn --model`. No executor data-path change, no agent. A
   registry-free `TestModelReconciles` envtest/integration test asserting CRUD + validation. Inert.
2. **Phase 2 — modelmgr reconciler + status.** `pkg/modelmgr` controller-runtime reconciler
   (`--modelmgr` dispatch in `cmd/fission-bundle/main.go`), source-secret validation, HF revision
   resolution, `Status` phase/conditions rollup. Helm Deployment + RBAC. No node agent yet (status
   reflects "Pending"). Unit + envtest.
3. **Phase 3 — DaemonSet agent: pull + digest verify (1.32 default path).** `--modelAgent` flag,
   `pkg/modelmgr/agent`, per-node cache dir, OCI/HF/S3/HTTP fetch reusing RFC-0001's keychain +
   zip-safe extraction, digest verification, `NodesReady`/quarantine. DaemonSet Helm template + cache
   `hostPath`. Unit (digest verify, quarantine) + integration (small real model lands + verifies).
4. **Phase 4 — Mount into functions (newdeploy/container).** `ApplyModelVolumes` applied **after**
   every `MergePodSpec` in `newdeploy`/`container` executor types; read-only `hostPath` subPath mount.
   Integration: a function mounts a small model, asserts presence at `/models/<name>`.
5. **Phase 5 — LRU/GC.** Node-local LRU with `--model-cache-max-bytes`, in-use protection, timestamp
   sidecars. Unit (LRU eviction order, in-use never evicted) + integration (budget forces eviction of
   an unused model, keeps the in-use one).
6. **Phase 6 — Init-container fallback + OCI image-volume opt-in (1.33+).** `OnDemand`/lazy
   init-container path (no DaemonSet); `ImageVolumeSource` model mount gated by RFC-0001's
   `ImageVolumeSupported` + `ENABLE_OCI_IMAGE_VOLUME`. Capability-gated integration test.

Future (out of scope): poolmgr per-`(env, modelSet)` image-volume model pools; nydus/estargz weight
streaming; weight pre-fetch hints driven by RFC-0010 GPU scheduling.

## Verification / test plan

Tests use the Go integration framework under `test/integration/` (build tag `//go:build integration`,
testify, `framework.Connect(t)`, `ns.CLI(...)`), envtest for CRUD round-trips, and fake-clientset
unit tests. Use `require` for preconditions, `assert` for independent checks, `t.Context()` over
`context.Background()`, and table-driven subtests with `t.Parallel()` per
`.claude/resources/test-writing-guidelines.md`.

**Unit.**
- `pkg/apis/core/v1/validation_validators_test.go` — table-driven: `ModelSpec.Validate()` accepts
  exactly-one-source, rejects zero/two sources, bad digest, bad format; `ModelReference.Validate()`
  rejects relative `MountPath` and reserved mount roots (`/userfunc`, `/packages`, `/secrets`,
  `/configs`); empty `ModelRefs` round-trips (backward-compat guard).
- `pkg/modelmgr/agent/digest_test.go` (new) — deterministic file-tree sha256 over a synthetic tree;
  mismatch quarantines; zip-slip / symlink / hardlink / `..` / absolute entries rejected (reuse the
  `pkg/utils/zip.go` posture).
- `pkg/modelmgr/agent/lru_test.go` (new) — LRU eviction order by last-mount; never evicts an in-use
  digest; budget=0 ⇒ no eviction; incoming larger than budget after freeing all evictable ⇒ error,
  not partial corruption.
- executor mount unit (`pkg/executor/executortype/newdeploy` / `container`) — `ApplyModelVolumes`
  adds a **read-only** mount at `/models/<name>` **after** `MergePodSpec`, and a user `PodSpec` patch
  cannot make it writable or relocate it; empty `ModelRefs` ⇒ no-op (no extra volume).

**envtest.**
- `Model` CRUD round-trips through the generated clientset; CEL rejects a `Model` with two sources or
  a bad digest pattern; `FunctionSpec.ModelRefs` round-trips; `Model.Status` subresource updates.

**Integration (`test/integration/suites/common/model_test.go`).**
- `TestModelReconciles` (Phase 1, no node infra) — `fission model create` + `fn create --model`;
  assert the `Model` CR fields and that a Function carries the `modelRefs`. Keeps Phase 1 green
  independent of agent/cache infra.
- `TestModelMountAndCache` (Phase 3/4, env-gated by a `FISSION_TEST_MODEL_*` image/URL like
  RFC-0001's `FISSION_TEST_REGISTRY`) — create a `Model` for a **small real** artifact (e.g. a tiny
  ONNX/GGUF file), a function that mounts it, invoke via the router, assert the function reports the
  file present at `/models/<name>` with the expected size/digest. **Then trigger a second cold start**
  (scale the function pods to zero and back, or invoke a fresh pod on the same node) and assert from
  the agent's metrics/logs (or a sentinel timestamp file) that the artifact was **not re-pulled** —
  the digest cache hit. `t.Skip` when the test model env var is unset so other CI legs stay green.

**Gates.** `make codegen && make generate-crds` clean (no diff); `make code-checks`;
`make license-check`; `make test-run`. The existing integration suite stays green (two port-forwards
+ `FISSION_INTERNAL_AUTH_SECRET` per CLAUDE.md). CEL: apply a `Model` with two sources and confirm
the API server rejects it with the "exactly one of oci, http, s3, or hf" message.

## Open questions

- **Digest UX for non-OCI sources.** Requiring an upfront sha256 for an HF repo is friction (the user
  must compute the canonical tree hash before creating the `Model`). Options: (a) require it
  (strict, supply-chain-clean); (b) allow `digest: ""` on first create, have the agent compute it,
  pin it into status, and reject future drift (TOFU). Lean (b) in dev, (a) for production — possibly a
  `modelCache.requireDigest` Helm gate.
- **Status aggregation transport.** modelmgr rolling up per-node readiness: per-node `Condition` on
  `Model.Status` (bounded by node count, can get large) vs. a `Lease`/annotation per node vs. a
  dedicated `ModelNodeStatus`. Lean toward `NodesReady` count + an aggregated condition, with details
  in agent metrics, to keep `Model.Status` bounded.
- **GPU-node affinity / selective prepull.** Should the agent prepull on every node or only on nodes
  matching a `Model`-level `nodeSelector`/`RuntimeClass` (so 16 GB weights don't land on CPU-only
  nodes)? This couples to **RFC-0010**; lean on adding `ModelSpec.NodeSelector` there rather than
  here, with the agent prepulling everywhere by default for now.
- **Mutable HF revision drift.** If a `Model` pins a branch and upstream moves, do we re-pull
  (breaking immutability) or freeze at `ResolvedRevision`? Lean freeze: record the resolved commit,
  never silently re-pull; a new revision is a new `Model`.
- **Init-container vs DaemonSet default when both are enabled.** Proposed: prepuller wins when
  `modelCache.prepuller: true`; init-container only fills gaps for `OnDemand` models or unscheduled
  nodes. Confirm the per-digest flock is sufficient against a DaemonSet/init double-pull race.

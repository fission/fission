# RFC-0010: GPU-Native Inference Functions

- Status: Proposed
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.N
- Requires: Kubernetes 1.32+ (floor); a GPU device plugin / operator installed by the cluster operator (NVIDIA GPU Operator, MIG manager, or any `DevicePlugin`); KEDA (already a Fission dependency, see `--mqt_keda`); consumes RFC-0009 (Model Artifact Delivery) for the `Model` CRD and node weight cache, and RFC-0008 (Streaming Invocation Path) for token output

## Summary

Add an opt-in `inference` execution profile that turns Fission functions into cost-efficient,
GPU-backed model servers: a model-aware warm pool that keeps a small number of pods warm with the
model already resident, scale-to-zero with seconds-not-minutes resume (by consuming the RFC-0009
node weight cache), and vendor-neutral fractional-GPU support (time-slicing / MIG / generic device
plugins) via pass-through of resource names, `RuntimeClassName`, and scheduling constraints. This is
a behavioral extension of the existing `newdeploy` and `poolmgr` executors gated by a new
`InferenceConfig`, not a fourth executor type — raw `nvidia.com/gpu` scheduling already works today
through the `PodSpec`/`Resources` fields; the gap this RFC closes is **lifecycle and economics**.

## Motivation

Fission can already schedule a function onto a GPU: `EnvironmentSpec.Runtime.PodSpec`
(`pkg/apis/core/v1/types.go:644`), `EnvironmentSpec.Resources`
(`pkg/apis/core/v1/types.go:725`), `FunctionSpec.Resources`
(`pkg/apis/core/v1/types.go:439`), and `FunctionSpec.PodSpec`
(`pkg/apis/core/v1/types.go:480`) all accept a full `apiv1.PodSpec` / `apiv1.ResourceRequirements`,
so a user can request `nvidia.com/gpu: 1` today. The problem is not scheduling — it is everything
around it:

- **The warm pool pins expensive GPUs idle.** `poolmgr` keeps generic env pods warm
  (`pkg/executor/executortype/poolmgr/gpm.go` → `reconcileEnvPool`, sized by
  `EnvironmentSpec.Poolsize`, `pkg/apis/core/v1/types.go:730`). For a CPU env, a handful of idle
  generic pods is cheap. For a GPU env, that pool sizing policy parks multi-thousand-dollar
  accelerators doing nothing, because the pool is **generic** (not specialized) — the model is not
  even loaded, so the warm pod still pays a cold model load on first request.
- **`newdeploy` can scale but every cold start reloads the model.** `newdeploy` creates a
  Deployment+Service per function and can scale on HPA metrics, but a scaled-from-zero or
  scaled-up replica re-downloads and re-loads model weights — minutes for a multi-GB LLM — which is
  unacceptable for interactive inference and defeats scale-to-zero economics.
- **No fractional GPU story.** There is no first-class way to express "give me a 10Gi MIG slice"
  or "share one GPU across N replicas via time-slicing"; users must hand-craft podspecs and hope
  the scheduler cooperates, with no guidance and no readiness gating.
- **Why now.** RFC-0009 introduces a `Model` CRD and a per-node weight cache (an image-volume /
  hostPath-backed artifact store), which makes "resume with weights already on the node" cheap for
  the first time. RFC-0008 introduces a streaming response path, which is what inference workloads
  actually need (token-by-token output). Together they make a real serverless-inference profile
  feasible; this RFC is the compute-plane piece that ties them to the executor.

## Goals

- An opt-in `InferenceConfig` on `FunctionSpec` / `EnvironmentSpec` that requests GPU resources,
  references one or more RFC-0009 `Model`s, and configures warm replicas + scale-to-zero.
- **Model-aware warm pool**: keep N pods warm with the model already loaded, so resume is seconds
  (weights served from the RFC-0009 node cache), distinct from the generic CPU pool sizing policy.
- **Scale-to-zero with fast resume** via KEDA `ScaledObject` (reusing the wiring Fission already
  ships for `--mqt_keda`) or HPA, with the node cache making the resume cheap.
- **Vendor-neutral fractional GPU**: pass through resource names, `RuntimeClassName`,
  `nodeSelector`, and `tolerations` so NVIDIA time-slicing, MIG, and any generic device plugin all
  work without Fission knowing vendor specifics.
- **Correct HPA scoping**: GPU/inference pods carry sidecars (`fetcher`, RFC-0009 model-puller), so
  reuse the function-container scoping already landed in commit `a1de74a1`.
- **Readiness gating**: do not route to a pod until its model is loaded, tied to RFC-0009 model
  status and the RFC-0006 readiness patterns (`FunctionReasonReady`).
- Backward compatible and additive: a function with no `InferenceConfig` behaves exactly as today.

## Non-goals

- **Fission does not install or manage device plugins, the NVIDIA GPU Operator, MIG partitioning, or
  `RuntimeClass` objects.** Those are the cluster operator's responsibility; the inference profile
  is capability-gated on their presence and surfaces a clear status condition when the requested
  resource is unschedulable.
- **No Kubernetes floor bump.** Everything here is GA at or before 1.32 (`ContainerResource` HPA
  metrics GA 1.30, native sidecars GA 1.29, device plugins long-GA). RFC-0009's image-volume cache
  path (K8s 1.33+) is itself opt-in per RFC-0009; the fetcher-pull fallback works on the 1.32 floor.
- **No new `Model` CRD here.** The `Model` type, its node cache, and its status are defined by
  RFC-0009; this RFC only *references* `Model` objects and *reads* their readiness.
- **No fourth executor type.** `InferenceConfig` extends `newdeploy` (the scaling backend) and
  `poolmgr` (the warm-pool backend); see Alternatives for why a `ExecutorTypeInference` was rejected.
- **No autoscaling on GPU-utilization custom metrics in v1** (DCGM-exporter integration). v1 scales
  on request concurrency / external KEDA triggers; GPU-metric scaling is an Open question.
- **No multi-model routing / LoRA hot-swap inside one pod.** One function serves the model(s) its
  `InferenceConfig` lists; richer routing is future work (and overlaps RFC-0011 AI-gateway concerns).

## Design

The inference profile is selected by a new optional `InferenceConfig` pointer on both
`FunctionSpec` and `EnvironmentSpec`. When present, the executor that owns the function
(`poolmgr` or `newdeploy`, chosen by the existing `ExecutionStrategy.ExecutorType`) applies
inference-specific behavior; when absent, nothing changes. The config is **purely additive** and
`+optional`, so it round-trips on old clients and stored objects.

### CRD types (`pkg/apis/core/v1/types.go`)

New field on `FunctionSpec` (an inference function overrides/augments env defaults) and on
`EnvironmentSpec` (a GPU env declares the default profile for all its functions):

```go
// On FunctionSpec (near Resources / PodSpec, ~line 480):

// InferenceConfig opts this function into the GPU-native inference profile:
// model-aware warm pool, scale-to-zero with fast resume, and fractional-GPU
// pass-through. Nil means classic behavior. Fields here override the
// environment's InferenceConfig when both are set.
// +optional
InferenceConfig *InferenceConfig `json:"inferenceConfig,omitempty"`
```

```go
// On EnvironmentSpec (near Resources / Poolsize, ~line 725):

// InferenceConfig declares the default inference profile for functions using
// this environment (e.g. a vLLM or TGI runtime image). Per-function
// InferenceConfig overrides individual fields.
// +optional
InferenceConfig *InferenceConfig `json:"inferenceConfig,omitempty"`
```

```go
// InferenceConfig configures the GPU-native inference execution profile.
// It does NOT install device plugins or RuntimeClasses; it only passes the
// operator-provisioned resources through to the function pod and gates
// readiness on model load. Vendor neutrality is achieved by treating the GPU
// as an opaque named resource plus optional RuntimeClass / scheduling hints.
InferenceConfig struct {
    // GPU describes the accelerator request rendered onto the function
    // container's resource requests/limits. Required when InferenceConfig is set.
    GPU GPURequest `json:"gpu"`

    // ModelRefs references one or more cluster-scoped RFC-0009 Model objects
    // whose weights this function serves, reusing the ModelReference type
    // RFC-0009 introduces (this RFC does not redefine it). The model-puller
    // sidecar (RFC-0009) mounts them from the node cache; readiness is gated on
    // all referenced models being Ready.
    // +listType=map
    // +listMapKey=name
    // +kubebuilder:validation:MinItems=1
    ModelRefs []ModelReference `json:"modelRefs"`

    // ScaleToZero enables scaling the function's backing Deployment down to zero
    // replicas when idle, via KEDA (when --mqt_keda KEDA is present) or an
    // HPA-to-zero fallback. Resume is fast because weights are served from the
    // RFC-0009 node cache rather than re-downloaded. Only meaningful with the
    // newdeploy executor. Defaults to false.
    // +optional
    ScaleToZero bool `json:"scaleToZero,omitempty"`

    // WarmReplicas is the number of pods kept warm WITH the model loaded, so a
    // request never pays a cold model load. For poolmgr this sizes a dedicated,
    // model-specialized GPU pool (distinct from the generic Poolsize). For
    // newdeploy it is the floor the function scales down to (the HPA/KEDA
    // minReplicaCount); 0 means true scale-to-zero. Defaults to 0.
    // +optional
    // +kubebuilder:validation:Minimum=0
    WarmReplicas int `json:"warmReplicas,omitempty"`

    // ScaleDownDelay is how long a warm pod stays warm after its last request
    // before becoming eligible for scale-down (the KEDA cooldownPeriod / HPA
    // stabilization window). Distinct from IdleTimeout so GPU pods can be held
    // longer than CPU pods. The unit is seconds. Defaults to 300.
    // +optional
    // +kubebuilder:validation:Minimum=0
    ScaleDownDelay int `json:"scaleDownDelay,omitempty"`

    // RuntimeClassName is passed through to PodSpec.runtimeClassName (e.g. the
    // NVIDIA container runtime class). Fission does not create the RuntimeClass.
    // +optional
    RuntimeClassName string `json:"runtimeClassName,omitempty"`
}

// GPURequest expresses an accelerator request as an opaque named resource plus
// an optional fraction, so NVIDIA time-slicing, MIG, AMD ROCm, and generic
// device plugins are all expressible without vendor-specific code.
GPURequest struct {
    // ResourceName is the extended resource the device plugin advertises, e.g.
    // "nvidia.com/gpu", "nvidia.com/mig-1g.10gb", "amd.com/gpu". Defaults to
    // "nvidia.com/gpu".
    // +optional
    // +kubebuilder:default="nvidia.com/gpu"
    ResourceName string `json:"resourceName,omitempty"`

    // Count is the number of whole devices to request (request==limit, as the
    // device-plugin API requires for extended resources). For MIG profiles and
    // time-sliced replicas the device plugin already advertises the fractional
    // slice as a distinct ResourceName, so Count stays a whole number.
    // +kubebuilder:validation:Minimum=1
    // +kubebuilder:default=1
    Count int64 `json:"count,omitempty"`

    // NodeSelector and Tolerations are appended to the pod (via MergePodSpec
    // semantics) to target GPU nodes / tolerate GPU taints. Pass-through only;
    // Fission does not infer them.
    // +optional
    NodeSelector map[string]string `json:"nodeSelector,omitempty"`
    // +optional
    Tolerations []apiv1.Toleration `json:"tolerations,omitempty"`
}
```

`ModelReference` is the type introduced by **RFC-0009** (a cluster-scoped `Model` name plus an
optional `MountPath`); this RFC reuses it verbatim and does not redefine it.

Notes:

- These types follow the same shape conventions as the existing `IngressConfig`/`RouteConfig`
  additions and the per-trigger podspec fields. After editing `types.go`, `make codegen` +
  `make generate-crds` regenerate `pkg/generated/`, `zz_generated_deepcopy.go`, and `crds/v1/`.
- A `func (ic *InferenceConfig) Validate() error` is added in
  `pkg/apis/core/v1/validation.go` and called from `FunctionSpec.Validate` and
  `EnvironmentSpec.Validate`: non-empty `ResourceName` (DNS-subdomain/extended-resource form),
  `Count >= 1`, at least one `ModelRef`, and `ScaleToZero` only with `ExecutorTypeNewdeploy`
  (poolmgr cannot scale a Deployment to zero — it manages a pool, not a Deployment). The
  poolmgr-incompatible combination is rejected at validation time, not silently ignored.
- Bounded CEL `+kubebuilder:validation:XValidation` is added for the cheap invariants
  (`ScaleToZero` requires newdeploy, `WarmReplicas <= MaxScale` when `MaxScale > 0`), matching the
  CEL-where-cheap / webhook-where-expensive split already used for the podspec safety rules. No new
  webhook handler is required (no per-container iteration here).

### Resource rendering: where the GPU lands (`pkg/executor/util` + both executors)

A shared helper renders `InferenceConfig` onto the function's main container and pod:

`pkg/executor/util/inference.go`:

```go
// ApplyInferenceConfig mutates the function container's resources and the pod
// spec to satisfy ic. It is called AFTER MergePodSpec so the GPU request,
// runtimeClassName, nodeSelector, and tolerations are not clobbered by the
// env/function podspec merge (the mount-after-merge invariant). containerName
// is the resolved main-container name (see mainContainerName).
func ApplyInferenceConfig(podSpec *apiv1.PodSpec, containerName string, ic *fv1.InferenceConfig) error
```

It:

1. Resolves `ic.GPU.ResourceName` (default `nvidia.com/gpu`) and sets
   `requests[name] == limits[name] == Count` on the container named `containerName` only — never on
   sidecars (extended resources must be request==limit; the device-plugin API forbids fractional
   extended-resource quantities, so MIG/time-slice fractions are expressed by the device plugin's
   own `ResourceName`, not by a fractional quantity here).
2. Sets `podSpec.RuntimeClassName = new(ic.RuntimeClassName)` when non-empty (Go 1.26 builtin
   `new(...)` rather than `ptr.To`).
3. Appends `ic.GPU.NodeSelector` into `podSpec.NodeSelector` and `ic.GPU.Tolerations` into
   `podSpec.Tolerations` (append, matching `MergePodSpec` list semantics).

Both executors call `ApplyInferenceConfig` from their pod-spec construction path, right after the
existing `MergePodSpec`, consistent with the "apply pod-spec invariants AFTER MergePodSpec"
invariant. The effective `InferenceConfig` is computed by merging the function's over the
environment's (function fields win), in a `fv1.MergeInferenceConfig(fnIC, envIC)` helper.

### Model-aware warm pool (`pkg/executor/executortype/poolmgr`)

The CPU warm pool (`reconcileEnvPool`, `gpm.go:631`, sized by `EnvironmentSpec.Poolsize`) keeps
**generic, unspecialized** pods. That policy is wrong for GPU envs on two counts: it parks idle
accelerators, and a generic pod still pays a model load on first request. So when a function's
effective `InferenceConfig` is set and `ExecutorType == poolmgr`, poolmgr maintains a **separate,
model-specialized GPU pool** keyed by `(env, sorted modelRefs)` rather than feeding the generic
pool:

- A new `gpuPoolSizer` (in `pkg/executor/executortype/poolmgr/gpu_pool.go`) computes desired warm
  count = `InferenceConfig.WarmReplicas` (NOT `Poolsize`). The generic pool's
  one-size-fits-all `Poolsize` is explicitly bypassed for inference functions, because a GPU pool's
  cost profile is fundamentally different: the policy is "keep exactly `WarmReplicas` model-loaded
  pods, never speculative extras." `getPool`/`reconcileEnvPool` consult `gpuPoolSizer` for inference
  envs.
- Warm pods are created **already specialized** to the model: the RFC-0009 model-puller sidecar
  mounts the node-cached weights and the runtime loads them at startup, so the pod is request-ready
  the moment it goes `Ready`. This replaces the generic-pool "specialize on demand via fetcher"
  step (`gp.go:419 specializePod`) for inference — the expensive work (weight load) happens once at
  pool-fill time, not per request.
- Resume cost: because RFC-0009 keeps weights in the node cache, refilling a warm slot after a pod
  is reaped is bounded by container start + cache mount + runtime load-from-local-disk (seconds),
  not a multi-GB network download (minutes). poolmgr's `IdleStrategy`/idle reaper
  (`gpm.go:706 IdleStrategy`) honors `InferenceConfig.ScaleDownDelay` for these pods instead of the
  function `IdleTimeout`, so GPU pods can be held warm longer than CPU pods.

`poolmgr` cannot scale a backing Deployment to zero (it manages a pool of pods), so
`ScaleToZero` is only valid with `newdeploy` (enforced in `Validate`). For poolmgr,
`WarmReplicas: 0` means "no warm pool; create a model-loaded pod on first request and reap it after
`ScaleDownDelay`" — a cold-on-first-request, warm-after profile.

### Scale-to-zero with fast resume (`pkg/executor/executortype/newdeploy` + KEDA)

For `ExecutorType == newdeploy` with `ScaleToZero: true`, the function's backing Deployment is
driven down to zero when idle and back up on demand. Fission already ships a KEDA-driven scaler
manager for message-queue triggers (`--mqt_keda` → `pkg/mqtrigger/scalermanager.go`, which creates
`kedav1alpha1.ScaledObject` objects via `clientGen.GetKedaClient()` under leader election). We reuse
that exact mechanism for inference:

- A new reconciler path in `newdeploymgr.go` creates a `kedav1alpha1.ScaledObject` targeting the
  function's Deployment when `ScaleToZero` is set and the KEDA client is available, with
  `minReplicaCount = WarmReplicas`, `maxReplicaCount = MaxScale`, and
  `cooldownPeriod = ScaleDownDelay`. The default v1 trigger is HTTP-concurrency-based: a
  KEDA `prometheus`/`http-add-on`-style scaler keyed on the router's per-function in-flight request
  gauge (Fission already exports request metrics). The scaler trigger is selected by Helm value
  (see below) so operators who run the KEDA HTTP add-on or a Prometheus stack pick what they have.
- **Fallback without KEDA**: when the KEDA client is absent, `newdeploy` emits a standard
  `autoscaling/v2` HPA with `minReplicas = WarmReplicas` (HPA cannot go below 1 on its own; true
  zero requires KEDA, so `WarmReplicas: 0` + no KEDA degrades to `minReplicas: 1` and logs a
  capability-gated condition). This keeps the profile usable on clusters without KEDA, with the
  honest limitation surfaced in status rather than silently.
- Fast resume on scale-from-zero is exactly what makes this viable: the new replica's model-puller
  sidecar (RFC-0009) finds weights in the node cache and the runtime loads from local disk. The
  scale-from-zero latency budget is container-start + cache-mount + local-load, gated by readiness
  (below) so the router never proxies to a not-yet-loaded pod.

### Fractional GPU (time-slicing / MIG / generic device plugins)

Fission stays vendor-neutral by **never installing or partitioning** anything — it only renders the
operator-provisioned resource:

- **Time-slicing**: the operator configures the NVIDIA device plugin to advertise, e.g., 4 virtual
  `nvidia.com/gpu` per physical GPU; the user requests `resourceName: nvidia.com/gpu, count: 1` and
  gets a time-sliced share. No Fission change beyond the pass-through.
- **MIG**: the operator partitions GPUs into MIG profiles; the device plugin advertises each profile
  as a distinct extended resource (`nvidia.com/mig-1g.10gb`, `nvidia.com/mig-3g.40gb`). The user
  sets `gpu.resourceName: nvidia.com/mig-1g.10gb`. This is documented as the canonical way to
  request a MIG slice — a named extended-resource request rendered onto the function container, plus
  the matching `nodeSelector` (e.g. `nvidia.com/mig.config: all-1g.10gb`) the operator's MIG manager
  applies to nodes.
- **Generic device plugins** (AMD, Intel, Habana, custom): identical path — any
  `<vendor>/<resource>` name passes through unchanged.
- `RuntimeClassName` (e.g. the NVIDIA container runtime class) and GPU node `tolerations`/
  `nodeSelector` are passed through the same `ApplyInferenceConfig` path.

The profile is **capability-gated**: if the requested `ResourceName` is not advertised by any node,
the pod stays `Pending` (Unschedulable) and the executor surfaces a clear function status condition
("requested GPU resource `nvidia.com/gpu` not schedulable; is a device plugin installed?") rather
than hanging. Fission does not pre-flight node capacity beyond this best-effort condition.

### HPA / ContainerResource scoping (`pkg/executor/executortype/newdeploy`)

Inference pods carry sidecars — `fetcher` and the RFC-0009 model-puller — and may carry user
sidecars. Pod-wide `autoscaling/v2` `Resource` metrics require **every** container to declare the
targeted resource request, and average utilization is diluted by idle sidecars. Commit `a1de74a1`
("scope HPA resource metrics to the function container") already rewrote newdeploy/container HPA
metrics to `ContainerResource` metrics scoped to the function's main container (resolved by
`mainContainerName`, `newdeploy.go:327`). The inference profile **inherits this unchanged**:

- The KEDA `ScaledObject` / HPA fallback continues to target the resolved `mainContainerName`, so
  the model-puller and fetcher sidecars never dilute the autoscaling signal and never trip the
  kube-controller-manager "missing request for cpu in container <name>" failure.
- This is called out explicitly because the GPU pod adds *another* always-on sidecar
  (model-puller); the existing per-container scoping is what makes that safe, and the inference
  rendering must not regress it (covered by a unit test that asserts the GPU request and the HPA
  metric both name the main container).

### Health / readiness: don't route until the model is loaded

The router must not proxy a request to a pod whose model is still loading (otherwise the user sees a
multi-second hang or a 503). Readiness is gated on model-ready, layered on RFC-0006's readiness
patterns and RFC-0009's model status:

- The runtime container's `ReadinessProbe` is configured to report ready only after the model is
  loaded. RFC-0009 standardizes a model-ready signal; for runtimes that expose a health endpoint
  (vLLM `/health`, TGI `/health`) the env's `Runtime.Container.ReadinessProbe` is used directly.
  When the env does not set one, the inference path injects a default `httpGet` readiness probe on
  the model-ready endpoint so a half-loaded pod is held out of the Service endpoints.
- For `poolmgr`, a warm pod is only added to the pool / marked usable once `Ready` (the existing
  ready-pod queue, `gpm.go:592 enqueueReadyPod` / `seedReadyPodQueue`), so a model-loading pod is
  never `choosePod`-selected.
- The executor sets the function status to `FunctionReasonReady` (the RFC-0006 readiness reason,
  `pkg/apis/core/v1/const.go`) only after at least one model-ready pod exists, and additionally
  gates on all `ModelRefs` reporting `Ready` in their RFC-0009 `Model.Status`. If a referenced
  `Model` is not yet cached on any eligible node, the function reports a `WaitingForModel`
  condition and does not go Ready — the router keeps the trigger un-routable, returning a clear
  503 instead of a cold hang.

### Helm (`charts/fission-all/`)

`values.yaml` additions (all default to today's behavior):

```yaml
inference:
  # Master switch. When false, InferenceConfig on CRs is validated but the
  # executor logs that the profile is disabled and falls back to classic
  # scheduling (the raw podspec GPU request still works).
  enabled: false
  # Default GPU pool policy knobs (overridable per-env/per-function).
  defaultRuntimeClassName: ""          # e.g. "nvidia"
  defaultScaleDownDelaySeconds: 300
  # KEDA scaler trigger used for scale-to-zero inference functions.
  # One of: "http-add-on" | "prometheus". Empty => HPA-to-min fallback.
  scaleToZeroTrigger: ""
  # Prometheus address for the "prometheus" trigger (router request metrics).
  prometheusServerAddress: ""
```

- The executor Deployment gains an `INFERENCE_ENABLED` env (plumbed like the existing feature
  toggles) and `INFERENCE_DEFAULT_RUNTIME_CLASS` / `INFERENCE_SCALE_TO_ZERO_TRIGGER`. Library
  constructors stay env-free (deterministic, per the `publisher.MakeWebhookPublisher` rule); the
  values are read once in `cmd/fission-bundle/main.go` and passed into the executor `Start`
  function, mirroring how `ROUTER_INTERNAL_URL` is threaded.

RBAC (`_fission-kubernetes-roles.tpl`): when `inference.enabled` **and** `scaleToZeroTrigger` is a
KEDA trigger, grant the executor role `keda.sh/scaledobjects` and `keda.sh/triggerauthentications`
(create/get/list/watch/update/patch/delete) — the same grants the `--mqt_keda` role already uses,
added only under the toggle so a non-inference cluster keeps minimal RBAC. No GPU/device-plugin RBAC
is needed (Fission never touches those APIs).

### CLI (`pkg/fission-cli/`)

New flags on `fission environment create/update` and `fission fn create/update`, defined in
`pkg/fission-cli/flag/flag.go` with keys in `pkg/fission-cli/flag/key/`:

- `--gpu` (int, default 0; >0 turns on the inference profile)
- `--gpu-resource` (string, default `nvidia.com/gpu`)
- `--model` (repeatable; each an RFC-0009 `Model` name → `ModelRefs`)
- `--scale-to-zero` (bool; newdeploy only)
- `--warm-replicas` (int)
- `--scale-down-delay` (int seconds)
- `--runtime-class` (string)
- `--gpu-node-selector` (repeatable `k=v`), `--gpu-toleration` (repeatable)

A `GetInferenceConfig(input cli.Input) (*fv1.InferenceConfig, error)` builder (analogous to
`GetResourceReqs` / `GetIngressConfig`) assembles the struct; it is nil when `--gpu` is 0, so
existing CLI behavior is unchanged. `fission fn get`/`spec` round-trip the new fields.

## Alternatives considered

- **A fourth executor type `ExecutorTypeInference`.** Rejected. The two hard behaviors —
  warm-with-model pooling and scale-to-zero-with-fast-resume — are exactly what `poolmgr` and
  `newdeploy` already do, minus the model awareness. A new executor would duplicate
  `AdoptExistingResources`, the fscache wiring, the reconciler, idle reaping, and HPA scoping. A
  config-gated extension reuses all of that and keeps one code path per concern. It also keeps the
  user's mental model ("pick poolmgr for warm pools, newdeploy for scale-to-zero") intact.
- **Scale-to-zero via a Fission-native controller instead of KEDA.** Rejected. Fission already
  depends on and ships KEDA wiring (`--mqt_keda`, `kedav1alpha1.ScaledObject` via `GetKedaClient`);
  rebuilding scale-from-zero activation (the hard part — request buffering during cold start) would
  duplicate the KEDA HTTP add-on for no benefit. KEDA is opt-in; the HPA-to-min fallback covers
  clusters without it.
- **A fractional-GPU abstraction (e.g. a `gpuFraction: 0.25` field) that Fission translates.**
  Rejected as non-portable. Fractioning is the device plugin's job and is expressed differently per
  vendor (time-sliced replicas vs MIG profiles vs MPS). Translating a generic fraction would couple
  Fission to vendor specifics and break the moment a new accelerator appears. Passing through the
  operator-advertised resource name is vendor-neutral and future-proof.
- **Building model loading into Fission directly (no RFC-0009 dependency).** Rejected. Weight
  delivery, caching, and node affinity are a substantial subsystem of their own (that is RFC-0009).
  Coupling them here would make this RFC unshippable and duplicate artifact-delivery logic that
  already exists for packages.
- **Reusing the generic `Poolsize` for GPU pools.** Rejected. `Poolsize` is a single env-wide knob
  for cheap generic pods; GPU economics demand a per-function, model-keyed, exact warm count. Sharing
  the knob would either over-provision GPUs or under-warm models. A distinct `gpuPoolSizer` policy is
  the point.

## Backward compatibility

- `InferenceConfig` is `+optional` on both `FunctionSpec` and `EnvironmentSpec`; nil ⇒ today's
  behavior exactly. Old clients and stored CRs round-trip (new fields are additive, deepcopy
  regenerated).
- Raw `nvidia.com/gpu` requests via `PodSpec`/`Resources` keep working with no `InferenceConfig` —
  this RFC does not change or restrict the existing podspec path.
- All new CLI flags default off; existing `environment`/`fn` commands are unaffected.
- `inference.enabled: false` is the Helm default; a cluster that does not opt in sees zero behavior
  change and no new RBAC. When disabled, an `InferenceConfig` on a CR is validated for correctness
  but the executor falls back to classic scheduling and logs the disabled state (capability-gated).
- KEDA RBAC and `ScaledObject` creation only appear under `inference.enabled` + a KEDA trigger;
  removal/disable is clean (the reconciler deletes the `ScaledObject` it owns, like the `--mqt_keda`
  cleanup path).
- No deprecations introduced; nothing removed. (If a future RFC ever promotes inference to a default,
  that would follow the ≥2-minor deprecation policy.)

## Rollout phases (one PR each, bisectable)

1. **CRD + validation (compiles, inert).** Add `InferenceConfig`, `GPURequest`, `ModelReference`
   types + `+optional` fields + kubebuilder/CEL markers + `Validate` + `MergeInferenceConfig`;
   `make codegen && make generate-crds`. No executor reads the field yet. Unit tests for validation
   and merge.
2. **Resource rendering helper.** `pkg/executor/util/inference.go` `ApplyInferenceConfig`
   (GPU request on main container, runtimeClassName, nodeSelector/tolerations append) + unit tests;
   not yet called by either executor.
3. **newdeploy wiring (no scale-to-zero).** Call `ApplyInferenceConfig` after `MergePodSpec`;
   render `WarmReplicas` as `minReplicas`; keep `ContainerResource` HPA scoping. Readiness-probe
   injection for model-ready. Behind `INFERENCE_ENABLED`. Unit + envtest.
4. **Scale-to-zero (KEDA + HPA fallback).** `ScaledObject` creation/cleanup in newdeploy reusing
   `GetKedaClient`; HPA-to-min fallback when KEDA absent; `WaitingForModel`/capability conditions.
5. **poolmgr model-aware GPU pool.** `gpu_pool.go` `gpuPoolSizer`, model-specialized warm pods,
   `ScaleDownDelay` idle policy, ready-gating. Unit tests for the sizing policy.
6. **Status / readiness integration with RFC-0009.** Gate `FunctionReasonReady` on `Model.Status`
   readiness; surface `WaitingForModel`.
7. **Helm.** `inference.*` values, executor env, conditional KEDA RBAC.
8. **CLI.** Flags + `GetInferenceConfig` + create/update/get/spec round-trip.
9. **Docs + gated integration test** (skip when no GPU node; CPU-fallback variant always runs).

Each phase compiles and is independently revertable; phase 1 is the classic "compiles, inert" CRD
landing.

## Verification / test plan

- `make codegen && make generate-crds` clean; `make license-check`; `make code-checks`.
- **Unit** (`testify`, `t.Context()`, table-driven, `t.Parallel()`):
  - `InferenceConfig.Validate` / `MergeInferenceConfig` (function-over-env precedence, `ScaleToZero`
    rejected on poolmgr, empty `ModelRefs` rejected).
  - `ApplyInferenceConfig`: GPU request lands on the main container only (never sidecars),
    request==limit, `RuntimeClassName`/nodeSelector/tolerations appended; MIG `ResourceName`
    rendered verbatim.
  - `gpuPoolSizer`: desired warm count == `WarmReplicas` (not `Poolsize`); 0 ⇒ no warm pool.
  - HPA metric stays scoped to `mainContainerName` with the model-puller sidecar present (regression
    guard for `a1de74a1`).
  - `ScaledObject` rendering: `minReplicaCount == WarmReplicas`, `cooldownPeriod == ScaleDownDelay`,
    target == function Deployment; HPA fallback emitted when KEDA client is nil.
- **envtest:** Environment/Function CRUD with `InferenceConfig` round-trips through the API server;
  CEL rejects `ScaleToZero` on poolmgr and zero `Count`.
- **Integration** (`test/integration/suites/common/`, build tag `integration`):
  - `t.Skip` when no GPU node is present (detected by listing nodes for the configured
    `gpu-resource` capacity) and the GPU runtime image env var is unset.
  - A tiny inference function (CPU-fallback echo "model server" image when no GPU; real GPU image
    when the cluster advertises the resource) that: (a) reports Ready only after model-ready;
    (b) with `ScaleToZero` + `WarmReplicas: 0` scales its Deployment to zero after `ScaleDownDelay`;
    (c) a subsequent request resumes it and returns a response (asserting resume succeeded, not its
    latency, to avoid CI flakiness, per the newdeploy-reconcile-racy guidance).
  - A poolmgr inference function asserts `WarmReplicas` model-loaded pods are kept warm and that a
    request never hits a cold load.
- Cross-references: weight delivery / `Model` CRD / node cache = **RFC-0009**; token streaming
  response path = **RFC-0008**. This RFC depends on both and does not reimplement either.

## Open questions

- **GPU-utilization autoscaling.** v1 scales on request concurrency / external KEDA triggers. Should
  v2 add a KEDA DCGM-exporter trigger so functions scale on actual GPU utilization? Proposed: defer;
  request-concurrency is the safe default and avoids a hard DCGM dependency.
- **Default `scaleToZeroTrigger`.** Should Fission pick `http-add-on` vs `prometheus` automatically
  by probing which KEDA components are installed, or require the operator to set it? Proposed:
  require explicit config; auto-detection is brittle.
- **Multi-model per pod / LoRA hot-swap.** Out of scope here; overlaps RFC-0011's AI-gateway routing.
  Should `ModelRefs` allowing >1 model imply the runtime multiplexes them, or is that a runtime
  concern Fission stays out of? Proposed: Fission mounts all referenced models and leaves
  multiplexing to the runtime.
- **Warm-pool sharing across functions.** Two functions on the same env + same model could in
  principle share a warm pool. Proposed: keep pools function-scoped in v1 (simpler ownership /
  adoption semantics); revisit if GPU cost pressure warrants it.
- **`ScaleDownDelay` vs `IdleTimeout` precedence.** Both exist; inference uses `ScaleDownDelay`. Do
  we hard-error if a user sets both with conflicting values, or document that `ScaleDownDelay` wins
  for inference functions? Proposed: document precedence, warn on conflict.

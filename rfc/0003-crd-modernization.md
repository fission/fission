# RFC-0003: CRD Modernization — Conditions, CEL Validation, SSA Readiness

- Status: Proposed
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.N (foundational; prerequisite for RFC-0001)
- Requires: Kubernetes 1.33+

## Summary

Modernize Fission's CRDs to match current Kubernetes API conventions:

1. Add `metav1.Condition` arrays to all `*Status` types, with standard
   condition types per CRD; add a `FunctionStatus` subresource (currently
   missing).
2. Migrate in-code validation from `pkg/apis/core/v1/validation.go` to
   CEL `+kubebuilder:validation:XValidation` markers on the types, where
   possible. Keep the webhook for cross-resource rules.
3. Add Server-Side Apply markers (`+listType=map`, `+listMapKey=name`,
   `+kubebuilder:validation:Immutable`) so CRDs work cleanly under SSA.

All changes are additive: existing status fields, existing clients,
existing CLI behavior all keep working.

## Motivation

- **Conditions**: `kubectl wait --for=condition=Ready function/foo` does
  not work today. GitOps tools (Argo CD, Flux) can't surface Fission
  resource health in their dashboards. Operators can't reconcile on
  Fission objects' states because `Status` is either absent (Function)
  or a bare string (CanaryConfig). Conditions are the universal
  Kubernetes status contract.
- **CEL**: validation rules live in Go (`pkg/apis/core/v1/validation.go`,
  ~20 functions). They're enforced only by the admission webhook, which
  means:
  - `kubectl apply --dry-run=server` against a cluster without the
    webhook (e.g. early bootstrap) silently accepts junk.
  - Reviewing validation logic requires reading Go.
  - Schema introspection tools (like `kubectl explain` with validation,
    IDE YAML plugins) see no rules.
  CEL rules live in the CRD itself, are enforced by the apiserver, and
  are documented in the OpenAPI schema.
- **SSA**: without `listType=map`, two controllers patching the same
  Function spec will clobber each other's entries in `Spec.Secrets`
  (for example). SSA-compliant CRDs are also required for some
  Argo CD features and for well-behaved operator reconciliation.

## Goals

### Conditions
- Add `FunctionStatus` with `Conditions []metav1.Condition` and promote
  Function to use `+kubebuilder:subresource:status`.
- Add `Conditions` to every existing `*Status`: `PackageStatus`,
  `HTTPTriggerStatus`, `KubernetesWatchTriggerStatus`,
  `MessageQueueTriggerStatus`, `TimeTriggerStatus`, `CanaryConfigStatus`.
- Define standard condition types per CRD (below).
- Controllers write these conditions.
- Existing status fields (e.g. `Package.Status.BuildStatus`) remain
  populated for compat.

### CEL
- Migrate ~60–80% of rules from `validation.go` to `+kubebuilder:validation:XValidation`
  markers. Hard cases (cross-object references like Package→Environment
  existence, RBAC-aware checks) stay in the webhook.
- `validation.go` remains as a library usable by the CLI (`fission spec
  validate`) for client-side checks.

### SSA
- Add `+listType=map` + `+listMapKey=name` to every list-of-objects
  field that has an identity.
- Add `+listType=set` to lists of scalars where appropriate.
- Add `+kubebuilder:validation:Immutable` to: `Function.Spec.Environment`,
  `Package.Spec.Environment`, `Environment.Spec.Version`.
- Verify `crds/v1/` generated schema has `x-kubernetes-list-type` and
  `x-kubernetes-list-map-keys` in the right places.

## Non-goals

- Breaking any field name or type.
- Removing the webhook. (Only field-level rules move to CEL; webhook
  stays for cross-object and RBAC-aware validation.)
- Auto-migration of existing CR instances. They keep working; new
  conditions are populated by controllers on next reconcile.

## Design

### Condition types

```go
// Function
const (
    FunctionReady         = "Ready"
    FunctionPackageReady  = "PackageReady"      // points at referenced Package
    FunctionEnvReady      = "EnvironmentReady"  // points at referenced Environment
    FunctionProgressing   = "Progressing"       // transient reconcile
)

// Package
const (
    PackageBuildSucceeded = "BuildSucceeded"    // replaces BuildStatusSucceeded enum (kept in parallel)
    PackageReady          = "Ready"             // composite: BuildSucceeded && URL populated (for tarball) or OCIRef valid (for OCI)
)

// HTTPTrigger
const (
    HTTPTriggerRouteAdmitted = "RouteAdmitted"  // router accepted the mux entry
    HTTPTriggerReady         = "Ready"
)

// MessageQueueTrigger
const (
    MQTBindingReady = "BindingReady"            // mqtmanager or KEDA hooked up
    MQTReady        = "Ready"
)

// TimeTrigger
const (
    TimeTriggerScheduled = "Scheduled"
    TimeTriggerReady     = "Ready"
)

// KubernetesWatchTrigger
const (
    KWatchSubscribed = "Subscribed"
    KWatchReady      = "Ready"
)

// CanaryConfig
const (
    CanaryProgressing = "Progressing"
    CanaryReady       = "Ready"
)
```

Each condition uses `metav1.Condition` with `Type`, `Status`, `Reason`,
`Message`, `LastTransitionTime`, `ObservedGeneration`. Reasons follow
`PascalCase` convention (e.g. `BuildPending`, `ImagePullFailed`,
`EnvironmentMissing`).

### FunctionStatus

```go
// +kubebuilder:subresource:status
type Function struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    Spec              FunctionSpec   `json:"spec"`
    // +optional
    Status            FunctionStatus `json:"status,omitempty"`
}

type FunctionStatus struct {
    // ObservedGeneration reflects the spec generation last reconciled.
    // +optional
    ObservedGeneration int64 `json:"observedGeneration,omitempty"`
    // Conditions are the latest observations of a Function's state.
    // +optional
    // +patchMergeKey=type
    // +patchStrategy=merge
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

### CEL rules — representative migrations

Current in-code check (`validation.go:FunctionSpec.Validate`):

```go
if fs.Environment.Name == "" {
    result = multierror.Append(result, MakeValidationErr(ErrorUnsupportedType, "FunctionSpec.Environment.Name", fs.Environment.Name, "environment name required"))
}
```

Becomes:

```go
// +kubebuilder:validation:XValidation:rule="self.environment.name != ''",message="environment name required"
type FunctionSpec struct { ... }
```

More examples:

```go
// Package.Spec.Deployment: exactly one of literal / url / oci
// +kubebuilder:validation:XValidation:rule="[has(self.literal) && self.literal != '', has(self.url) && self.url != '', has(self.oci)].filter(x, x).size() == 1",message="exactly one of literal, url, or oci must be set"

// HTTPTrigger.Spec: host is non-empty when ingress.annotations has cert-manager.io
// +kubebuilder:validation:XValidation:rule="!(has(self.ingressConfig.annotations) && self.ingressConfig.annotations.exists(k, k.startsWith('cert-manager.io'))) || self.host != ''",message="host required when cert-manager annotations present"

// Environment.Spec.Poolsize: non-negative
// +kubebuilder:validation:Minimum=0

// CanaryConfig.Spec.WeightIncrement: 1..100
// +kubebuilder:validation:Minimum=1
// +kubebuilder:validation:Maximum=100
```

### SSA markers

Apply to every list of objects:

```go
// FunctionSpec
type FunctionSpec struct {
    // +listType=map
    // +listMapKey=name
    Secrets []SecretReference `json:"secrets,omitempty"`
    // +listType=map
    // +listMapKey=name
    ConfigMaps []ConfigMapReference `json:"configmaps,omitempty"`
    // ...
    // +kubebuilder:validation:XValidation:rule="self == oldSelf",message="environment is immutable"
    Environment EnvironmentReference `json:"environment"`
}
```

### Regeneration pipeline

`make codegen && make generate-crds && make generate-webhooks` must run
and produce clean diffs. CI already enforces this; this RFC adds:

- A `make check-cel` target that runs `kubectl apply --dry-run=server`
  of a curated set of valid + invalid fixtures against a kind cluster.
- A `docs/crd-conditions.md` generated from condition constants.

## Alternatives considered

1. **Keep Go validation, skip CEL.** Status quo. Fails the
   observability + tooling goals.
2. **Rewrite CRDs from scratch in v1beta2.** Breaks every client. No.
3. **Deprecate and remove legacy status fields immediately.** Violates
   backward compat. Deprecated but kept for 2+ minor releases.

## Backward compatibility

Purely additive.

- New `Conditions` arrays on statuses — existing clients ignore them.
- New `FunctionStatus` subresource — existing writers that set `.status`
  via the main resource will get a 422 *only* if they previously wrote
  to `.status` (which would have been a no-op since there was no status
  subresource). All existing writers use the main resource and write
  `.spec` only.
- CEL rules enforced at apiserver admission. Rules are audited against
  current production data; any rule that would reject an existing valid
  resource is either (a) relaxed, or (b) gated behind a feature-flag
  and phased in over 2 releases.
- Existing CLI continues to work. `fission fn get` output gains a new
  `CONDITIONS` column (opt-in via `-o wide`).

## Rollout phases

1. **Phase 1 — Types + conditions scaffolding.** Add condition constants
   and `*Status.Conditions` fields. Add `FunctionStatus`. Regenerate
   CRDs. Ships in v1.N.
2. **Phase 2 — Controllers write conditions.** Buildermgr writes
   `BuildSucceeded`; executor writes `EndpointsReady` (aligned with
   RFC-0002); router writes `RouteAdmitted`. Ships in v1.N.
3. **Phase 3 — SSA markers and immutability.** Regenerate CRDs with
   listType/listMapKey and `Immutable` markers. Ships in v1.N.
4. **Phase 4 — CEL migration batch 1.** Port obviously-safe rules
   (required fields, ranges, enums). Ships in v1.N.
5. **Phase 5 — CEL migration batch 2.** Port complex rules
   (mutually-exclusive fields, compound conditions). Dry-run audit
   across curated clusters first. Ships in v1.(N+1).
6. **Phase 6 — Deprecate legacy Go validation duplicates.**
   `validation.go` kept for CLI client-side use only. Webhook retains
   cross-object rules. v1.(N+1).

## Verification

- **Unit**: Condition setter helpers in `pkg/conditions/` round-trip; no
  duplicate condition types; `LastTransitionTime` only updates on
  status change.
- **Envtest**: `kubectl wait --for=condition=Ready function/foo`
  succeeds after the function's package builds and endpoints are ready.
- **E2E**: existing tests pass unchanged. Add `test_conditions.sh`
  that asserts the full condition timeline for a happy-path function.
- **CEL smoke**: each CEL rule has a positive + negative fixture under
  `pkg/apis/core/v1/testdata/`; `make check-cel` applies them via
  envtest.
- **SSA**: two clients patch non-overlapping fields on the same
  Function; both survive. Integration test in
  `test/ssa/test_two_writers.sh`.
- **Compat audit**: script that lists every CR in a target cluster and
  runs `kubectl apply --dry-run=server -f <re-exported>`; expect 100%
  success rate on any existing resource.

## Open questions

- Some tools (helm diff, etc.) interact poorly with generated
  `conditions` fields; need a Helm `lookup`-free pattern for the CRDs.
- Do we want to expose function cold-start duration on the `Function`
  status itself? It's observational, not desired-state. Lean: keep it
  on metrics only, not on `Status`.
- Canary's bare `Status string` — do we keep it forever or deprecate
  via release notes? Lean: keep forever (cheap), add conditions
  alongside.

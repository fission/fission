# RFC-0007: Gateway API Route Provider (deprecate Ingress)

- Status: Implemented ([#3478](https://github.com/fission/fission/pull/3478), merged 2026-06-08)
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.N
- Requires: Kubernetes 1.32+ (floor); Gateway API CRDs installed by the cluster operator

## Summary

Add first-class, provider-pluggable route exposure to the Fission router.
Today the router can create a `networking.k8s.io/Ingress` per HTTPTrigger (`--createIngress`).
Ingress is frozen and the ecosystem is moving to the **Gateway API** (`gateway.networking.k8s.io`).
This RFC introduces an internal `RouteProvider` abstraction, ships a Gateway-API provider that
emits `HTTPRoute` objects attached to an operator-managed `Gateway`, refactors the existing
Ingress code into an `ingress` provider behind the same interface, and marks the Ingress
integration deprecated.

## Motivation

- `ingress-nginx` is in maintenance and many operators are migrating to Gateway API
  implementations (Envoy Gateway, Istio, Cilium, Contour, NGINX Gateway Fabric).
- The Ingress API is frozen by SIG-Network; Gateway API is its official successor (GA since
  Gateway API v1.0, `HTTPRoute` v1).
- Without router support, users must hand-author `HTTPRoute` objects out-of-band, decoupling
  them from the HTTPTrigger lifecycle (no create/update/delete coupling, no status).
- A real user (EKS + Envoy Gateway) hit exactly this gap.

## Goals

- Expose functions through Gateway API `HTTPRoute` objects, lifecycle-coupled to the HTTPTrigger.
- Keep the design provider-pluggable so future route mechanisms (or a Fission-native route type)
  plug in without touching the reconciler.
- Attach mode only: Fission creates `HTTPRoute` with a `parentRef` to a Gateway the operator owns.
- Backward compatible: existing `CreateIngress` + `IngressConfig` keep working, now marked
  deprecated.
- Opt-in and capability-gated: the Gateway provider is enabled by a Helm value that also grants
  the `gateway.networking.k8s.io` RBAC; disabled by default, no behavior change for existing users.

## Non-goals

- Fission will **not** create/own a `Gateway` or `GatewayClass` (listeners/TLS are the operator's
  responsibility). This keeps RBAC minimal and avoids coupling to listener config.
- No Kubernetes floor bump. Gateway API CRDs are installed by the operator, not bundled by Fission.
- No removal of the Ingress path in this RFC (removal follows the 2-minor-release deprecation
  policy in `rfc/README.md`).
- No `TCPRoute`/`GRPCRoute`/`TLSRoute` (HTTP functions only, for now).

## Design

### RouteProvider abstraction (`pkg/router/`)

```go
type RouteProvider interface {
    Name() string // "ingress" | "gateway"
    // Reconcile creates/updates the route when the trigger requests THIS provider,
    // and deletes any object this provider owns for the trigger otherwise — so every
    // registered provider sees every trigger and self-cleans on provider switch / toggle-off.
    Reconcile(ctx context.Context, trigger *fv1.HTTPTrigger) error
    // DeleteByName removes any object this provider owns for a deleted trigger (idempotent).
    DeleteByName(ctx context.Context, name string) error
}
```

- `httpTriggerReconciler` holds `providers []RouteProvider`. Its `Reconcile` loops the providers
  (replacing the single `reconcileIngress` call); the delete path loops `DeleteByName`.
- Desired-provider resolution: `RouteConfig.Provider` if set; else legacy `CreateIngress` ⇒
  `"ingress"`; else none. Each provider checks "am I the desired provider & is exposure
  requested?" → reconcile, else → delete-own. This reuses the toggle-off / provider-switch
  semantics the current `reconcileIngress` already has.
- The `ingress` provider is the current `reconcileIngress`/`deleteIngressByName`/`GetIngressSpec`
  moved behind the interface — no behavior change; it also serves legacy `CreateIngress`.
- The `gateway` provider is registered only when `GATEWAY_API_ENABLED` is set, so an absent
  provider (no RBAC) surfaces as a clear HTTPTrigger status condition rather than RBAC errors.

### Gateway provider

`pkg/router/gatewayapi.go` + `util.GetHTTPRouteSpec(namespace, trigger, defaultParentRefs)` →
`gwapiv1.HTTPRoute`:

- `ParentRefs` from `RouteConfig.Gateway.ParentRefs`, falling back to the Helm-configured default
  Gateway parentRef when the trigger omits it.
- `Hostnames` from `RouteConfig.Hostnames`.
- One rule: `PathPrefix`/`Exact` match from `RouteConfig.Path` (default `/`); `BackendRefs` →
  `router` Service:80 (the same backend Ingress uses); annotations + `GetDeployLabels` labels.
- TLS is the Gateway listener's responsibility in attach mode → not set on the `HTTPRoute`.
- Client: a typed `sigs.k8s.io/gateway-api/pkg/client/clientset/versioned` clientset, doing
  direct API create/update/delete (mirrors how the Ingress provider uses `kubernetes.Interface` —
  no Manager cache informer for `HTTPRoute`). Reached via `crd.ClientGenerator.GetGatewayClient()`.

### CRD (`pkg/apis/core/v1/types.go`)

New provider-neutral field on `HTTPTriggerSpec` (successor to `CreateIngress` + `IngressConfig`):

```go
RouteConfig *RouteConfig `json:"routeConfig,omitempty"`

type RouteConfig struct {
    Provider    string              `json:"provider"` // +kubebuilder:validation:Enum=ingress;gateway
    Hostnames   []string            `json:"hostnames,omitempty"`
    Path        string              `json:"path,omitempty"`
    Annotations map[string]string   `json:"annotations,omitempty"`
    TLS         string              `json:"tls,omitempty"` // ingress-only; ignored by gateway
    Gateway     *GatewayRouteConfig `json:"gateway,omitempty"`
}
type GatewayRouteConfig struct { ParentRefs []GatewayParentRef `json:"parentRefs,omitempty"` }
type GatewayParentRef struct {
    Name        string `json:"name"`
    Namespace   string `json:"namespace,omitempty"`
    SectionName string `json:"sectionName,omitempty"`
    Port        int32  `json:"port,omitempty"`
}
```

- `CreateIngress` + `IngressConfig` are marked `// Deprecated:` (kept functional).
- CEL `+kubebuilder:validation:XValidation` mirrors the IngressConfig rules (path absolute, host
  DNS1123). Go `RouteConfig.Validate()` is added and called from `HTTPTriggerSpec.Validate`.
- HTTPTrigger has no admission webhook (CEL-only per RFC-0003) → no webhook change.

### Helm (`charts/fission-all/`)

- `values.yaml`: `gatewayAPI.enabled: false` (+ optional `gatewayAPI.defaultParentRef`).
- Router role (`_fission-kubernetes-roles.tpl`): when enabled, add
  `gateway.networking.k8s.io/httproutes` (create/get/list/watch/update/patch/delete) and read-only
  `referencegrants`.
- Router deployment: pass `GATEWAY_API_ENABLED` (+ default parentRef) env, following the existing
  `enableIstio` / feature-`config` plumbing.

### CLI (`pkg/fission-cli/`)

New `httptrigger` flags: `--route-provider` (ingress|gateway), `--route-host` (repeatable),
`--route-path`, `--gateway`/`--gateway-parentref name[.namespace]`, `--route-annotation`,
`--route-tls`. `GetRouteConfig` builds `*RouteConfig` (analogous to `GetIngressConfig`).
Legacy `--createingress*` flags are kept with deprecation notes in their usage; if both are passed,
`RouteConfig` wins.

## Alternatives considered

- **Gateway-API-only, no abstraction.** Simpler, but the user explicitly wants a flexible,
  extensible mechanism; the abstraction is cheap (one interface, two impls) and future-proofs.
- **Standalone `--gatewayApi` subsystem.** Rejected: Ingress reconciliation already lives in the
  router tied to HTTPTrigger; a new process/flag/leader-election adds operational surface for no
  benefit.
- **Fission-managed Gateway + GatewayClass.** Rejected for v1: couples Fission to listener/TLS
  config and needs broad RBAC. Attach mode is the portable, minimal-privilege default. Could be a
  future opt-in.
- **Manager cache-backed client for HTTPRoute.** Rejected: would spin a cluster informer for
  HTTPRoutes; direct typed-clientset writes match the Ingress provider and keep the cache lean.

## Backward compatibility

- `CreateIngress`/`IngressConfig` and all `--ingress*` CLI flags continue to work unchanged.
- New fields are `+optional`; old clients and stored objects round-trip.
- Gateway provider is off by default; zero change for clusters that don't enable it.
- Deprecation removal of the Ingress path is out of scope and gated by the 2-release policy.

## Rollout phases (one PR each, bisectable)

1. Add `sigs.k8s.io/gateway-api` dep; register `gwapiv1` into the router scheme. (compiles, inert)
2. CRD: `RouteConfig` types + deprecate Ingress fields + CEL + `RouteConfig.Validate`; codegen +
   generate-crds; validation unit tests.
3. `RouteProvider` abstraction; extract Ingress into the `ingress` provider; reconciler loops
   providers. Pure refactor — existing router tests stay green.
4. Gateway provider: `gatewayapi.go` + `GetHTTPRouteSpec` + `GetGatewayClient` wiring; provider
   registered on `GATEWAY_API_ENABLED`; unit tests.
5. Helm: `gatewayAPI.enabled` value, RBAC, deployment env.
6. CLI: flags + `GetRouteConfig` + create/update; deprecate legacy ingress flag usage strings.
7. Docs + gated integration test (skip when no Gateway controller present).

## Verification / test plan

- `make codegen && make generate-crds` clean; `make license-check`.
- Unit: `GetHTTPRouteSpec`, `RouteConfig.Validate`, provider switch/toggle-off/delete semantics
  (table-driven, testify, `t.Context()`).
- envtest: HTTPTrigger CRUD with `RouteConfig` round-trips; CEL rejects bad path/host.
- Manual E2E (kind/EKS + Envoy Gateway): install `GatewayClass`+`Gateway`, create a
  gateway-provider HTTPTrigger, confirm Fission creates the `HTTPRoute`, it attaches
  (`Accepted`/`ResolvedRefs`), curling the Gateway routes to the function, and toggling
  provider/deleting the trigger removes the `HTTPRoute`.
- Integration suite: new test in `test/integration/suites/common/`, `t.Skip` when no Gateway
  controller present.

## Open questions

- Default match type for `Path`: `PathPrefix` (Ingress-like) vs `Exact`. Proposed: `PathPrefix`,
  overridable later.
- Should `RouteConfig.Provider` default to `gateway` when `GATEWAY_API_ENABLED` and a default
  parentRef is configured, so `--route-host` alone is enough? Proposed: require explicit provider
  for now.
- Cross-namespace `HTTPRoute → Gateway` needs a `ReferenceGrant` in the Gateway's namespace,
  owned by the operator. Document only; Fission does not create grants.

# RFC-0005: SPIFFE Workload Identity

- Status: Proposed
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.(N+1) onwards, phased
- Requires: Kubernetes 1.32 (current floor).
  A SPIFFE Workload API implementation in the cluster (SPIRE, Istio CA, or cert-manager-csi-driver-spiffe) when the feature is enabled; Fission itself requires nothing new when it is disabled.

## Summary

Give every Fission workload — control-plane components and deployed functions alike — a verifiable SPIFFE identity, consumed through the standard SPIFFE Workload API with a bring-your-own issuer.
Functions get per-function X.509 and JWT SVIDs they can use to reach databases, cloud providers (AWS STS, GCP WIF, Vault), and SPIFFE-aware services with zero stored credentials; the control plane gets an optional mTLS mode that replaces the shared-secret HMAC channels with asymmetric identity, including the router→function invocation hop.

## Motivation

Fission has two identity gaps today:

1. **Internal auth is symmetric.**
   The post-GHSA HMAC scheme (`pkg/auth/hmac`) derives five per-channel keys from one master secret (`FISSION_INTERNAL_AUTH_SECRET`).
   It authenticates messages but does not encrypt transport, any holder of the master can forge every channel, and every function pod holds the fetcher channel's derived key in memory — a compromised function pod can sign as any fetcher.
   Rotation is manual (active/old key pair).
2. **Functions have no identity at all.**
   A function that calls a database or a cloud API gets long-lived credentials via Secrets or env vars.
   There is no way for an external system to answer "is this caller function X in namespace Y?", and no story comparable to EKS IRSA / GKE Workload Identity that serverless users increasingly expect.

SPIFFE solves both with one primitive: attested, short-lived, auto-rotated identities (SVIDs) issued against a standard API.
The ecosystem has converged on it — Istio and Linkerd issue SPIFFE IDs, AWS Roles Anywhere / GCP WIF / Vault accept SPIFFE-derived JWT or X.509 credentials, and OPA policies key on SPIFFE IDs.
No mainstream FaaS framework offers per-function SPIFFE identity today; for Fission it is both a security upgrade and a differentiator.

## Goals

- Per-function SPIFFE identity for **all three executor types**, including poolmgr's runtime-specialized pods.
- Issuer-agnostic consumption: Fission speaks only the SPIFFE Workload API socket; SPIRE/Istio/cert-manager are deployment choices documented in recipes, never linked against (except the optional Phase 4 fast path).
- Functions consume identity without SPIFFE-aware code: rotating cert/key/bundle files at well-known paths plus audience-scoped JWT-SVID files, exposed via env vars.
- `internalAuth.mode: spiffe` as an optional alternative to HMAC on the same five channels, with per-channel SPIFFE-ID authorizers.
- mTLS on the router→function invocation hop, terminated in-pod by the fetcher so environment runtimes stay untouched.
- Visible degradation: no silent fallback to plaintext, metrics for identity latency and failures.

## Non-goals

- Deprecating or removing the HMAC scheme.
  It stays the zero-dependency default indefinitely; SPIFFE mode is an alternative, not a successor.
- Bundling SPIRE in the Fission chart (an optional convenience sub-chart may follow later; documentation-first).
- Fission acting as an identity issuer or token broker of any kind.
- Per-language SDK helpers (`fission-spiffe` libraries) — files and env vars are the contract; sugar can come later.
- Service-mesh replacement.
  Where a mesh already wraps pod-to-pod traffic, the data-plane mTLS toggle is turned off to avoid double encryption.
- Federation / multi-cluster trust-domain topology (documentation-only in Phase 4).

## Design

### Identity scheme

The trust domain belongs to the cluster operator, never to Fission; `spiffe.trustDomain` is a required Helm value when the feature is on, used for authorizer matching only.
Fission defines the path convention beneath it:

| Workload | SPIFFE ID path |
|---|---|
| Specialized function pod (all executor types) | `/ns/<ns>/function/<name>-<uid>` |
| Generic pool pod (pre-specialization) | `/ns/<ns>/environment/<env>` |
| Builder pod | `/ns/<ns>/builder/<env>` |
| Control-plane component | `/fission/<component>` (`router`, `executor`, `storagesvc`, `buildermgr`, `timer`, `mqtrigger`, `kubewatcher`, `canaryconfigmgr`, `webhook`) |

Decisions embedded in the table:

- **UID in the function path.**
  Delete + recreate of a function yields a new identity, so external trust grants die with the object — no resurrection impersonation.
  The documented consequence: GitOps flows that recreate functions need wildcard grants (`/ns/team-a/function/orders-*`) or re-grant automation.
- **Pre-specialization pods get a real (environment) identity, not none.**
  The fetcher must authenticate to storagesvc before the function identity exists, and an env-scoped identity is a better state than an unattested workload.
- **Namespace in the path** gives natural tenant boundaries: external policy can grant `/ns/team-a/*` without enumerating functions.

### Identity assignment: label-driven attestation

Fission performs **no issuer API calls**.
The executor already stamps `functionName`/`functionUid`/`functionNamespace` labels on pods — at creation for newdeploy/container, and at specialization time for poolmgr via the existing `choosePod` relabel (`pkg/executor/executortype/poolmgr/gp.go`).
The issuer maps labels to SPIFFE IDs declaratively.
The shipped SPIRE recipe is two `ClusterSPIFFEID` CRs (spire-controller-manager):

1. Control plane: deployment-label selector → `/fission/<component>`.
2. Functions: pod-label template → `spiffe://<td>/ns/{{ .PodMeta.Namespace }}/function/{{ index .PodMeta.Labels "functionName" }}-{{ index .PodMeta.Labels "functionUid" }}`, with a fallback env-labeled entry for unspecialized pool pods.

Equivalent recipes for Istio CA and cert-manager-csi-driver-spiffe are documentation work.

### Function identity data flow

**newdeploy / container:** function labels exist at pod creation, the entry matches at admission, and the SVID is on the socket from the start.
No flip.

**poolmgr:**

1. Pool pod starts with env labels → attested as `/ns/<ns>/environment/<env>`.
2. `choosePod` relabels the pod (existing behavior, unchanged).
3. The issuer's pod watch swaps the registration entry; the agent pushes the new SVID down the pod's Workload API stream.
   Typically sub-second with spire-controller-manager, but unbounded in theory — treated as asynchronous.
4. The fetcher holds a Workload API watch, sees the SVID change from env-ID to function-ID, and atomically re-materializes the identity files (write-new + rename).

**Identity-readiness modes** — `Function.spec.identity.mode`:

- `async` (default): specialization completes without waiting; the function may serve its first requests before the flip lands.
  Zero cold-start cost.
- `strict`: the fetcher blocks the specialize response until the function SVID is live (timeout → specialization failure → executor retries on another pod).
  For functions whose first action is an authenticated call.

**Ordering note:** the specialize-time package fetch from storagesvc can race the SVID flip, so the storagesvc archive channel's authorizer accepts environment identities as well as function identities (env pods could always fetch packages; that is their job).

**Cleanup:** poolmgr pods are destroyed after use, never returned to the pool, so there is no identity-downgrade path.
Pod deletion removes the entry via the issuer's own pod watch.

### Consumption surface

The fetcher materializes identity files into the pod's shared volume, atomically rotated:

```
/secrets/spiffe/
  svid.pem        # leaf cert + intermediates (X.509-SVID)
  svid.key        # private key (0600, function-readable)
  bundle.pem      # local trust-domain bundle
  jwt/<audience>  # one JWT-SVID file per declared audience
```

(The exact directory is finalized against the existing shared-mount layout during implementation; the contract is the env vars, not the literal path.)

Env vars injected into the function container: `SPIFFE_ID`, `SPIFFE_TRUST_DOMAIN`, `SPIFFE_SVID_PATH`, `SPIFFE_KEY_PATH`, `SPIFFE_BUNDLE_PATH`.
Ordinary TLS clients work with zero SPIFFE-aware code (`curl --cert/--key/--cacert`, `sslcert=` in pg connection strings).

**JWT-SVIDs for cloud federation** — `Function.spec.identity.jwtAudiences: []string`.
The fetcher maintains a fresh JWT-SVID per declared audience, refreshing at 50% TTL — the same file-based pattern as projected ServiceAccount tokens, which AWS SDKs already consume via `AWS_WEB_IDENTITY_TOKEN_FILE`.
The flagship flow: declare `jwtAudiences: ["sts.amazonaws.com"]`, set `AWS_ROLE_ARN` and `AWS_WEB_IDENTITY_TOKEN_FILE=/secrets/spiffe/jwt/sts.amazonaws.com`, and the stock AWS SDK assumes a role with zero stored credentials; the IAM trust policy matches the SPIFFE ID in the token's `sub`.
This requires the issuer's OIDC discovery endpoint (SPIRE's `oidc-discovery-provider`) — deployment documentation, not Fission code.

**Raw socket passthrough** — `Function.spec.identity.mountSocket: bool`, default false.
Mounts the Workload API socket into the function container for SPIFFE-native code.
Opt-in because the socket bypasses the declared-audience scoping: a function with the socket can mint JWTs for any audience.

**CRD addition** (Phase 2, follows the types.go checklist + codegen):

```go
type FunctionIdentity struct {
    // Mode selects identity readiness semantics: "async" (default) or "strict".
    // +optional
    Mode string `json:"mode,omitempty"`
    // JwtAudiences lists audiences for which the fetcher maintains
    // refreshed JWT-SVID files under the well-known identity directory.
    // +optional
    JwtAudiences []string `json:"jwtAudiences,omitempty"`
    // MountSocket additionally mounts the SPIFFE Workload API socket
    // into the function container. Off by default; widens the
    // function's ability to request arbitrary-audience JWTs.
    // +optional
    MountSocket bool `json:"mountSocket,omitempty"`
}
```

`FunctionSpec` gains `Identity *FunctionIdentity` (`+optional`).
Environment runtimes require **no changes** in any phase.

### Control-plane mTLS (`pkg/auth/spiffe`)

A sibling package to `pkg/auth/hmac` implementing the same five logical channels, selected by one Helm value (`internalAuth.mode: hmac | spiffe | none`).
Built on `go-spiffe/v2`.

- Every fission-bundle deployment mounts the Workload API socket and obtains its `/fission/<component>` SVID.
  Control-plane labels exist at deployment creation, so there is no flip anywhere in the control plane.
- **Server side:** each gated listener uses `tlsconfig.MTLSServerConfig` with a per-channel authorizer — the SPIFFE-mode equivalent of the derived key.
- **Client side:** the existing signer-transport seams (e.g. `pkg/storagesvc/client.MakeClient`) get an mTLS transport instead of an HMAC RoundTripper.
  Library constructors stay deterministic — sockets and IDs arrive as arguments, never env reads.

The channel → allowed-client matrix (this table **is** the security policy and ships in code as table-driven authorizers):

| Channel (server) | Allowed client SPIFFE IDs |
|---|---|
| `storagesvc` (/v1/archive) | `/fission/buildermgr`, `/ns/*/environment/*`, `/ns/*/function/*`, `/ns/*/builder/*` |
| `fetcher` (in-pod endpoints) | `/fission/executor` |
| `builder` (/build) | `/fission/buildermgr` |
| `executor` (HTTP API) | `/fission/router` |
| `router-internal` (/fission-function) | `/fission/executor`, `/fission/timer`, `/fission/mqtrigger`, `/fission/kubewatcher`, `/fission/canaryconfigmgr` |

What SPIFFE mode buys over HMAC: asymmetric identity (no forgeable shared secret), transport encryption, automatic rotation, and authorization expressed as identities rather than key possession.
The fetcher channel is the standout: today every function pod holds a derived HMAC key; under SPIFFE a compromised function pod holds only its own identity.

Migration semantics: the mode is per-installation, both ends of every channel flip together on Helm upgrade — the same operational contract as enabling `internalAuth` today.
No dual-stack listeners.

### Data-plane mTLS (router→function)

The invocation hop is plaintext today, protected only by whatever NetworkPolicy the operator added.
Environment runtimes cannot reasonably all learn TLS, so:

**The fetcher grows a small mTLS reverse-proxy listener**: router →(mTLS)→ fetcher-proxy →(localhost plaintext)→ env runtime.
Plaintext never leaves the pod's network namespace; env runtimes stay untouched.

- **Router side:** `MTLSClientConfig` with the authorizer pinned to the expected function SVID for that service entry — the router cryptographically verifies it reached the right function, not just the right IP.
  Existing router connection pooling amortizes handshakes.
- **Pod side:** the proxy authorizes `{/fission/router, /fission/executor}`; the executor's specialize/ping traffic folds into the same listener, closing the "anything in-cluster can hit the function port" gap properly.
- **Async-flip interaction:** in `async` mode a just-specialized pod may briefly serve the env SVID, so the router's authorizer accepts `function-ID OR that function's env-ID` by default; `strict` mode tightens to function-ID-only.
  This is the same trust statement async mode already makes.
- **Container executor type** has no fetcher (arbitrary user image).
  When data-plane mTLS is on, the executor injects the fetcher binary as a proxy-only sidecar container into the Deployment it already owns.
- **Toggle:** `spiffe.dataPlaneMTLS`, default **on** when `mode: spiffe` (secure by default); opt out for raw-throughput installations or mesh-wrapped clusters.

### Configuration

```yaml
internalAuth:
  mode: hmac | spiffe | none          # default: current behavior (hmac when secret set, else none)
spiffe:
  trustDomain: ""                      # required when any SPIFFE feature is on
  workloadAPISocket: /run/spire/agent-sockets/api.sock
  functionIdentity: true               # per-function SVIDs + fetcher materialization
  dataPlaneMTLS: true                  # router→function hop (only meaningful in spiffe mode)
```

Shipped as documentation (not chart dependencies): the SPIRE + spire-controller-manager + `oidc-discovery-provider` install recipe and the two `ClusterSPIFFEID` CRs.

### Failure modes

- **Workload API socket unavailable at component start:** log + retry with backoff; `/readyz` stays false in spiffe mode until the first SVID.
  No silent fallback to plaintext — the mode is explicit, degradation is visible.
- **SVID flip never arrives (poolmgr):** `async` → function keeps serving; fetcher logs and increments `fission_spiffe_flip_timeouts_total`.
  `strict` → specialization fails, executor retries on another pod.
- **JWT refresh failure:** the stale token stays in place until expiry (callers see provider-side auth errors, never a missing file); log + metric on each failed refresh.
- **Bundle rotation / issuer upgrade:** handled by go-spiffe streaming watches; files re-materialize atomically.

### Observability

- `fission_spiffe_flip_latency_seconds` (histogram) — relabel→function-SVID-on-socket latency; the async/strict trade-off rests on this number.
- `fission_spiffe_flip_timeouts_total`, `fission_spiffe_jwt_refresh_failures_total`.
- SVID identity and expiry logged at component start and on rotation (debug level).

## Alternatives considered

- **Fission identity registrar talking to SPIRE server APIs** (per-Function registration entries, SPIRE Delegated Identity for instant specialize-time SVIDs).
  Rejected as the baseline: hard-couples Fission to SPIRE, and a component holding SPIRE server credentials is a large blast radius.
  Survives as the optional Phase 4 fast path on the agent-side Delegated Identity API only.
- **Fission token broker** (env-level attestation + executor-signed claims exchanged for function-scoped JWTs).
  Rejected: makes Fission an issuer — the broker becomes the crown jewel to attack, and it contradicts the BYO-issuer decision.
- **Per-environment identity only.**
  Rejected: functions sharing an environment could impersonate each other to external services; the weak story defeats the differentiator.
- **Function name without UID in the path.**
  Rejected in favor of strictness: identity should not survive object recreation; wildcard grants cover the GitOps-recreate pattern.
- **Bundling SPIRE in the chart.**
  Deferred: documentation-first keeps Fission out of the SPIRE lifecycle business; a convenience sub-chart can follow once the recipes stabilize.
- **TLS in environment runtimes** instead of the fetcher proxy.
  Rejected: dozens of language images would each need TLS + reload support; the in-pod proxy keeps the runtime contract untouched.

## Backward compatibility

- Everything is opt-in.
  `internalAuth.mode` defaults to current behavior; `spiffe.*` values are inert unless set.
- The HMAC scheme is untouched and remains the zero-dependency default; no deprecation is announced by this RFC.
- The `FunctionSpec.Identity` field is `+optional` and additive; CRD upgrade is non-breaking (codegen + generate-crds per the standard checklist).
- Clusters without any Workload API implementation are unaffected; enabling SPIFFE mode without one fails visibly (readiness), not silently.

## Rollout phases

1. **Control-plane mTLS.**
   `pkg/auth/spiffe`, the mode switch, control-plane SVIDs, the authorizer matrix, Helm values, SPIRE recipe docs.
   No CRD changes.
   Proves the plumbing end to end.
2. **Function identity.**
   Label-templated issuance recipe for function pods, fetcher Workload API watch + file materialization, `FunctionSpec.Identity` (mode, jwtAudiences, mountSocket), env vars, JWT refresh loop, flip-latency metrics, AWS-federation tutorial.
3. **Data-plane mTLS.**
   Fetcher proxy listener, router SVID pinning, container-type proxy sidecar injection, `dataPlaneMTLS` toggle.
4. **Reserved / conditional.**
   SPIRE Delegated-Identity fast path (only if Phase 2 latency data demands it), optional SPIRE sub-chart, federation and multi-cluster documentation.

Each phase is independently shippable and useful; later phases depend on earlier ones but not vice versa.

## Verification / test plan

- **Unit:** `pkg/auth/spiffe` against go-spiffe's in-memory test CA; table-driven authorizer tests covering every channel × {allowed, denied} SPIFFE IDs; fetcher materialization (atomic rotation, JWT refresh scheduling) with a fake Workload API.
- **Integration:** a `suites/spiffe` suite gated on a SPIRE-enabled cluster (kind profile installs SPIRE; tests `t.Skip` otherwise, the same pattern as image-gated tests).
  Covers: per-function SVID after specialization on all three executor types, file materialization and rotation, JWT audience files, data-plane mTLS round trip, strict-mode specialize failure on flip timeout, and socket-absent → readyz false.
- **Latency:** the poolmgr flip latency (`fission_spiffe_flip_latency_seconds`) is measured in CI and tracked across releases; it is the gate for whether Phase 4's fast path is warranted.

## Open questions

- Exact identity directory inside the pod, finalized against the current shared-volume layout (the env-var contract is authoritative either way).
- Whether the proxy-only fetcher sidecar for the container executor type should be a separate minimal binary instead of a fetcher mode flag.
- Whether `strict` mode should also gate the data-plane authorizer to function-ID-only automatically (currently: yes, documented behavior).
- Label-value constraints: `functionUid` is a full UUID; confirm combined template output stays within SPIFFE path-segment limits across issuers (SPIRE is fine; verify cert-manager template support).

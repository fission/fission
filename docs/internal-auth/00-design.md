# HMAC Application-Layer Authentication for Internal Services

- Tracking issues: GHSA-chf8-4hv6-8pg6, GHSA-7g8g-g937-26g8

## Summary

Add a shared-secret HMAC-SHA256 application-layer authentication scheme to Fission's internal HTTP services.
The first delivery covers `storagesvc /v1/archive`; the same primitive is designed for reuse on every other Fission control-plane HTTP surface (in-pod fetcher, builder, executor, router internal listener) and is rolled out service-by-service.

The scheme is transport-agnostic, replay-resistant within a one-minute window with `±60s` skew tolerance, and ships behind a Helm toggle (`internalAuth.enabled`) so existing installations continue to work during upgrade.

Per-service signing keys are derived from a single chart-managed master secret via HKDF-SHA256, so a leak of one channel's runtime memory cannot forge requests on a different channel.
The operator manages exactly one Secret regardless of how many services are signed.

## Motivation

Two coordinated security advisories — GHSA-chf8-4hv6-8pg6 ("StorageSvc has no authentication on `/v1/archive`") and its duplicate GHSA-7g8g-g937-26g8 — describe the same root cause: any pod able to reach `storagesvc` over the cluster network can list, download, upload, and delete every Fission package archive.
Storagesvc currently relies on `NetworkPolicy` (added in #3365) and namespace isolation as its only access controls.
That is brittle:

- **Mis-configured NetworkPolicy**: clusters that disable the default policy, override the namespace selector, or run the CNI in permissive mode lose the only barrier.
- **Compromised in-namespace pod**: a function pod or sidecar that gets RCE'd is, by selector, *inside* the policy and can talk to storagesvc directly.
- **Cluster-network egress controllers** (Cilium, Calico) that allow same-namespace traffic by default need explicit deny rules; not every operator writes them.
- **Future internal services**: the router will grow an internal listener for executor callbacks (advisory 4), and the in-pod fetcher / cmd/builder / executor HTTP API have similar exposure today.
We need a reusable primitive instead of a one-off.

A symmetric HMAC scheme is the smallest meaningful step up from "no auth" that still works in every cluster (no cert-manager dependency, no service-mesh requirement) and gives us replay resistance without a server-side nonce store.

## Threat model

The scheme defends against:

1. **Lateral movement from a same-namespace compromised pod** — the attacker has cluster-DNS access to a Fission internal service but not the Fission control-plane secret.
2. **Mis-configured NetworkPolicy** that admits unintended workloads.
3. **Replayed captured request** outside a 60-second window (e.g. via cluster log scraping).
4. **One-channel runtime compromise** — an attacker who reads the in-process key for one service (memory dump, debug endpoint) cannot forge requests on a different service because each channel uses an independently-derived key.

It does NOT defend against:

- An attacker that *also* exfiltrates `Secret/fission-internal-auth` (e.g. cluster-admin, full RBAC compromise).
At that point the master rotates and the cluster needs full forensics, not application-layer auth.
- Active MITM on the in-cluster network.
That is the kubelet/CNI/mTLS layer's job.
- Cross-namespace requests (still gated by NetworkPolicy).
HMAC is an additional defence-in-depth layer, not a NetworkPolicy replacement.

## Goals

- Reject unsigned requests at every signed service's HTTP endpoints.
- Sign every internal Fission client call to a signed service, using the per-service derived key.
- Allow `/healthz` to bypass signing so kubelet probes pass.
- Provide a reusable Go package (`pkg/auth/hmac`) so each new signed surface adds one server-side middleware registration and one client-side transport wrapper, no new chart wiring.
- Tolerate clock skew up to `±60s` between caller and verifier (typical kubelet drift).
- Support online secret rotation via a paired `OldSecret` accepted alongside the current `Secret` for a 5-minute overlap.
- Default-on for new installs (`internalAuth.enabled=true`); opt-in for in-place upgrades from the previous minor.
- Operator manages exactly one Secret regardless of how many services are signed.

## Non-goals

- mTLS or a service mesh.
Out of scope; HMAC is the lowest common denominator that works without cert-manager.
- End-user authentication (CLI → router public listener).
That is a separate AuthN/AuthZ effort.
- CLI-side signing of public router requests.
The CLI talks to a public listener; HMAC here would just be an obfuscated bearer token.

## Services

Each row is a logical communication channel.
The signer and verifier on a given row use the same per-service derived key (see "Per-service key derivation" below).
Service identifiers are part of the HKDF info string and must remain stable across releases.

| Service ID | Server endpoint(s) | Caller(s) | Status |
|---|---|---|---|
| `storagesvc` | `storagesvc /v1/archive` (GET, POST, DELETE, HEAD) | in-pod `cmd/fetcher` (download + upload), buildermgr archive cleanup, `fission` CLI archive subcommands, `pkg/fission-cli/cmd/package/util` | **Phase 1 (this PR)** |
| `fetcher` | in-pod `cmd/fetcher` `/fetch`, `/upload`, `/clean`, `/specialize` (port 8000) | buildermgr (build → fetcher), executor (specialization → fetcher) | Phase 2 (PR-α) |
| `builder` | in-pod `cmd/builder` `/build` (port 8001) | buildermgr | Phase 2 (PR-α) |
| `executor` | `pkg/executor` HTTP API: `/v2/getServiceForFunction`, `/v2/tap`, `/v2/error`, etc. | router, kubewatcher, timer, mqt-fission-kafka, canaryconfig | Phase 2 (PR-β) |
| `router-internal` | router's internal listener that hosts `/fission-function/<ns>/<name>` | executor, kubewatcher, timer, mqt-fission-kafka, mqt-keda connectors, `fission` CLI (`fn test`, `fn test --async`) | Advisory 4 (separate PR) |

Out of scope:
- **Webhook server** — already authenticated by the kube-apiserver's CA on the admission path.
- **`/healthz`, `/metrics`** — kubelet / Prometheus probes have no signing path; bypass is mandatory.
- **MQT-KEDA connector → router-internal** — upstream `fission/kafka-http-connector` images don't sign; they need an upstream image change or a deploy-time NetworkPolicy-only acceptance.

### CLI direct invocation (`fission fn test`)

`fission fn test` (and `fission fn test --async`) invoke a function directly via `/fission-function/<ns>/<name>` on the router **internal** listener (port 8889, `svc/router-internal`) — not the public listener (8888), which no longer serves that path after GHSA-3g33-6vg6-27m8. The CLI port-forwards to `svc/router-internal` and HMAC-signs the request with the `ServiceRouterInternal` key when `FISSION_INTERNAL_AUTH_SECRET` is set.

**When internal auth is enabled** (`internalAuth.enabled=true`, the chart default), the operator must export `FISSION_INTERNAL_AUTH_SECRET` in the shell before running `fission fn test`:

```bash
export FISSION_INTERNAL_AUTH_SECRET="$(kubectl get secret fission-internal-auth -n fission -o jsonpath='{.data.secret}' | base64 -d)"
fission fn test --name my-fn
```

Without it the router's internal verifier rejects the request with `401`/`403`. When internal auth is disabled (pass-through mode, `internalAuth.enabled=false`), no env var is needed — the verifier short-circuits and accepts unsigned requests.

## Design

### Canonical string

The HMAC input is:

```
<METHOD>\n
<REQUEST-URI>\n
<SHA256_HEX(BODY)>\n
<UNIX_MINUTE>
```

where:
- `<REQUEST-URI>` is the path **plus** the raw query string (`r.URL.RequestURI()` on the Go side) so query parameters like `?id=<archive-id>` are bound to the signature.
A captured `GET /v1/archive?id=A` cannot be replayed as `?id=B` within the skew window.
- `UNIX_MINUTE = floor(unix_seconds / 60) * 60`.
The minute granularity ensures the signature is stable for the duration of a typical request (sub-second) and tolerates retries within the same minute without re-signing.
- The body hash binds the signature to the bytes; absent it, an attacker who captured a signed `POST` could swap the body.

### Headers

- `X-Fission-Auth-Timestamp` — the caller's unix-seconds timestamp at request time.
- `X-Fission-Auth-Signature` — `hex(HMAC-SHA256(derived_key, canonical))`.

The timestamp is sent as the *exact* unix seconds the caller used, not the rounded minute, so the verifier can apply skew tolerance against its own clock.
The signature itself is computed over the rounded minute — so two requests issued 30 seconds apart from the same client share a signature input but differ in `X-Fission-Auth-Timestamp`.

### Per-service key derivation

The chart distributes a single 32-byte master secret to every signed service.
At runtime, each service derives its own key from the master via HKDF-SHA256:

```
derived_key = HKDF-SHA256(
  ikm    = master_secret,
  salt   = nil,
  info   = "fission-internal-v1:" + service_id,
  length = 32 bytes,
)
```

The signer and verifier on a given channel both call this with the same `service_id`, so they end up with the same `derived_key` end-to-end.
The master never leaves the verifier/signer constructors at the boundary; only the per-service derived key is passed to the actual HMAC primitives.

This gives the operational simplicity of a single shared secret (one chart Secret, one rotation event) with the compromise-isolation properties of independent per-service secrets:

- A leaked `derive(master, "storagesvc")` lets the attacker forge storagesvc requests but **not** fetcher / builder / executor / router-internal.
- The derivation is one-way: the derived key reveals nothing about the master.
- Rotating the master rotates every derived key atomically.

The constant `KeyVersion = "fission-internal-v1"` is the wire-format version mixed into the HKDF info string.
Bumping `KeyVersion` invalidates every signature in flight; treat it as a breaking change requiring a coordinated rollout.

### Verification

```
1. If Secret is empty → pass through (backwards-compat short-circuit).
2. If path ∈ Bypass set → pass through.
3. Read X-Fission-Auth-Timestamp; reject if missing or unparseable.
4. Read X-Fission-Auth-Signature; reject if missing.
5. abs(now - timestamp) > skew → reject ("stale timestamp"), BEFORE buffering body.
6. Slurp body (bounded by MaxBodyBytes via http.MaxBytesReader; over the limit → 413).
7. Recompute Sign(derived_key, method, request_uri, body, timestamp).
8. crypto/hmac.Equal(want, got) → pass; re-inject body for downstream handler.
9. Else if OldSecret-derived key set → repeat 7-8.
10. Else → 401.
```

Comparison uses `crypto/hmac.Equal` to avoid timing oracles.
The skew check happens before body buffering so a stale-timestamp request with a multi-MB body cannot force the verifier to allocate `MaxBodyBytes` before rejecting.
The body is read once with `io.ReadAll`, the hash is computed, and `r.Body` is re-injected as `io.NopCloser(bytes.NewReader(body))` so downstream handlers (e.g. multipart parsers in `uploadHandler`) can re-read it.

### Bypass paths

- `/healthz` — kubelet probes have no signing path; an unsigned 200 must remain available.

No other bypasses.
In particular `/metrics` is served on a different port (8080) and never reaches a service's signed mux.

### Library shape (`pkg/auth/hmac`)

The package exposes both the low-level primitives and the per-service convenience constructors.
New services should always use the convenience constructors so the service identifier is bound at compile time.

```go
// Primitives (each new signed service does NOT need to call these directly):
func Canonical(method, requestURI string, body []byte, ts int64) string
func Sign(key []byte, method, requestURI string, body []byte, ts int64) string
func Verify(key []byte, method, requestURI string, body []byte, ts int64, sig string) bool
func NewSigner(key []byte, rt http.RoundTripper, now func() time.Time) *Signer
func Verifier(opts VerifierOpts) func(http.Handler) http.Handler

// Per-service convenience (preferred entry points):
func DeriveServiceKey(master []byte, service Service) []byte
func ServiceSigner(master []byte, service Service, rt http.RoundTripper, now func() time.Time) *Signer
func ServiceVerifier(master, oldMaster []byte, service Service, opts VerifierOpts) func(http.Handler) http.Handler

// Service identifiers (extend this list when adding a new signed channel):
const (
    ServiceStoragesvc     Service = "storagesvc"
    ServiceFetcher        Service = "fetcher"
    ServiceBuilder        Service = "builder"
    ServiceExecutor       Service = "executor"
    ServiceRouterInternal Service = "router-internal"
)
```

Adding a new signed surface is mechanical:

1. Add a new `Service` constant.
2. Server side: register `ServiceVerifier(master, oldMaster, ServiceXxx, opts)` as middleware.
3. Client side: wrap the transport with `ServiceSigner(master, ServiceXxx, rt, time.Now)` when `master` is non-empty.
4. Make sure both server and client pods have `FISSION_INTERNAL_AUTH_SECRET` mounted (the chart's `_helpers.tpl::internalAuth.envs` partial covers all top-level deployments; `pkg/fetcher/config/config.go::internalAuthEnvVars` covers dynamically-created builder/function pods).

No new Helm Secret, no new env var, no chart change.

### Secret distribution

The Helm chart materializes one Secret per Fission-using namespace, named `fission-internal-auth`, each with the same `data.secret` value (32-byte alphanumeric, b64-encoded).

Why per-namespace copies and not one cross-namespace reference: kubelet does not support cross-namespace `secretKeyRef`, and Fission builder / function pods are scheduled into user namespaces (e.g. `default`).
The chart iterates over `.Release.Namespace` plus `.Values.defaultNamespace` plus each `.Values.additionalFissionNamespaces` and renders one Secret per entry.
A single `$secretValue` is computed once at the top of the template via `lookup` (preserved across upgrades) so all copies share the same value.

The Secret is mounted as `FISSION_INTERNAL_AUTH_SECRET` (and the optional `FISSION_INTERNAL_AUTH_SECRET_OLD`) into:

- The four top-level Fission deployments that talk to storagesvc — `storagesvc`, `buildermgr`, `executor`, `router` — via the `_helpers.tpl::internalAuth.envs` partial.
  Phase 2-β / Advisory 4 extend the same partial to `kubewatcher`, `timer`, `mqt-fission-kafka`, and `mqt-keda` once those services need to sign executor / router-internal calls; that chart change ships in the follow-up PRs, not in Phase 1.
- Every dynamically-created builder pod and function-runtime pod's fetcher container via `pkg/fetcher/config/config.go::internalAuthEnvVars`, sourced from the per-namespace Secret with `optional: true` so installs with `internalAuth.enabled=false` still admit the pod.

### Rotation

To rotate the master secret:

1. Operator copies the live `secret` value into `internalAuth.oldSecret`.
2. Operator sets `internalAuth.secret` to a fresh 32-byte value.
3. `helm upgrade` rolls every Fission deployment.
During the rollout the verifier accepts both keys (each per-service `OldSecret` is derived from the master `oldSecret`); signers use the new derived key as soon as the pod restarts.
4. After every signer pod has rolled (≤5 minutes by default), operator runs a second `helm upgrade` with `internalAuth.oldSecret=""` to remove the old key.

Because all per-service keys are derived from the master, rotation is atomic across every signed channel.

### Backwards compatibility

Three layers of opt-out, in increasing priority:

- **Code level**: an empty master in `ServiceVerifier` produces an empty derived key; the underlying `Verifier` middleware short-circuits to pass-through.
That makes it safe to deploy the Go side first; nothing breaks until the env var is set.
- **Container env**: the env var `FISSION_INTERNAL_AUTH_SECRET` defaults to empty; verifiers start up unguarded if unset.
Signers likewise check the env and only sign when a master is present.
- **Helm value**: `internalAuth.enabled=false` skips the Secret resource and the env mounts entirely; the cluster behaves exactly as it does on `main` today.

For new installs `internalAuth.enabled=true` is the default.
For in-place upgrades from the previous minor we recommend leaving the default — the chart preserves the existing master on subsequent upgrades and signed clients/verifier are introduced atomically by a single `helm upgrade`.

## Alternatives considered

### mTLS with cert-manager

Considered.
Stronger guarantees (transport binding, identity attestation) but requires cert-manager as a hard dependency, which we don't currently mandate.
We can add mTLS later as an additional layer; HMAC is not exclusive of it.

### Bearer token with a per-call random token

Considered and rejected.
A static bearer is replayable forever; rotating it produces the same complexity as HMAC without the body binding.

### Request-scoped JWTs

Considered and rejected.
Signing JWTs needs a JWKS endpoint, key rotation tooling, and clock-sync that we'd then have to operate.
HMAC-SHA256 over a canonical string is a *much* smaller surface and is the path Kubernetes ServiceAccount tokens use internally.

### NetworkPolicy alone

Already in place via #3365.
We *keep* it; HMAC is layered on top.
On its own, NetworkPolicy fails open the moment a cluster operator overrides the namespace selector or a CNI is installed in permissive mode.

### Single shared key across all services (no derivation)

Considered and rejected.
Operationally equivalent to deriving (one chart Secret, one rotation), but a runtime memory leak on any one service exposes the key for *every* internal channel.
HKDF derivation costs ~µs on first use and gives per-channel compromise isolation for free.

### Separate Secret resource per service

Considered and rejected.
Per-channel isolation is identical to the derived-key approach but the operator carries 5 Secrets, 5 rotation events, and 5 chart values.
The complexity is not justified when HKDF gives the same isolation from one master.

## Limitations

The scheme has known limitations operators should plan around.

**Maximum body size.**
The verifier reads the entire request body into memory before computing the signature so the body bytes can be re-injected for downstream handlers (multipart parsers, etc.).
That cost is bounded by `VerifierOpts.MaxBodyBytes` (default 256 MiB, set on each registration).
Bodies that exceed the cap are rejected with `413 Request Entity Too Large` *before* signature verification — i.e. an unauthenticated attacker cannot use a giant unsigned body to DoS a signed service.
Operators that legitimately need to upload archives larger than 256 MiB should bump the cap rather than disable enforcement; the cap is the largest archive size we expect to see in practice.
For the one bulk-data endpoint, `storagesvc /v1/archive`, the cap is operator-tunable: set the Helm value `storagesvc.maxArchiveSizeMib` (env var `STORAGE_MAX_ARCHIVE_SIZE_MIB`), and size the storagesvc memory request/limit to match, since the body is held in memory during verification.
The other registrations (fetcher, builder, executor, router-internal) carry small control-plane payloads and keep the 256 MiB default.
For very large packages, OCI-native delivery (`packageRegistry`) is the better-managed alternative to raising the cap — the code is pulled and mounted from a registry rather than buffered through storagesvc.

**Replay within the skew window.**
A captured signed request can be replayed any number of times within the `SkewSec` window (default 60s) and will pass verification each time.
Adding nonce tracking would require a shared replay-cache store across all replicas (Redis, a distributed cache, or a Lease-CR scheme).
That is out of scope for this PR; the 60-second window is short enough that the practical attack surface is limited to a recently-captured packet on the cluster network — at which point an attacker capable of sniffing in-cluster traffic has bigger problems.
A future change may add a nonce store if a use case justifies the operational cost.

**`fission-cli` archive subcommands.**
The CLI's archive subcommands (`fission archive list`, `fission archive get`, etc.) talk to storagesvc directly via a port-forward.
They sign using `HMACSecretFromCluster` which reads `Secret/fission-internal-auth` from the install namespace via the user's kubeconfig — works as long as the operator has read access to that Secret.
Standard CLI flows that go through `fission package` and `fission function` are unaffected — those commands talk to the Kubernetes API server, not storagesvc directly.

**MQT-KEDA connectors don't sign router-internal requests.**
Upstream KEDA connector images (`fission/kafka-http-connector` etc.) call into `/fission-function/<ns>/<name>` on the router internal listener (advisory 4) and do not currently sign.
Until those images are upgraded, operators enabling the router-internal verifier should either rely on NetworkPolicy alone for KEDA traffic or build signing-aware connector images.

## Verification / test plan

- Unit tests: `pkg/auth/hmac/*_test.go` cover canonical-string formatting, sign/verify round-trip, `OldSecret` fallback, skew tolerance, healthz bypass, body re-readability, body-cap enforcement, tampered-body / tampered-method / tampered-path rejection, the rejection-log emission contract, and the per-service derivation (`TestDeriveServiceKey*`, `TestServiceSignerVerifier*`).
- In-process wiring test (`pkg/storagesvc/storagesvc_auth_test.go`) exercises the storagesvc middleware chain end-to-end.
- `helm template charts/fission-all -n fission --set additionalFissionNamespaces='{ns-a,ns-b}'` renders one `fission-internal-auth` Secret per namespace, all carrying the same `data.secret`.
- Manual smoke: an existing function workflow (`fission fn run hello`) survives an in-place upgrade with `internalAuth.enabled=true`.

## Rollout phases

1. **Phase 1 — primitives + storagesvc enforcement** (this PR; advisory 2).
   `pkg/auth/hmac` is introduced with primitives + per-service derivation.
   Storagesvc registers `ServiceVerifier(..., ServiceStoragesvc, ...)`; in-pod fetcher / buildermgr / fission-cli sign with `ServiceSigner(..., ServiceStoragesvc, ...)`.
   Chart materializes the master Secret in every Fission-using namespace.
   `internalAuth.enabled` defaults to `true`.
   Backwards compat carried by the empty-secret short-circuit.
2. **Phase 2-α — extend to in-pod fetcher and cmd/builder** (separate follow-up PR).
   `cmd/fetcher/app/server.go` registers `ServiceVerifier(..., ServiceFetcher, ...)`.
   `cmd/builder` HTTP server registers `ServiceVerifier(..., ServiceBuilder, ...)`.
   `pkg/fetcher/client` (used by buildermgr → fetcher and executor → fetcher) wraps its transport with `ServiceSigner(..., ServiceFetcher, ...)`.
   buildermgr → builder client wraps with `ServiceSigner(..., ServiceBuilder, ...)`.
3. **Phase 2-β — extend to executor HTTP API** (separate follow-up PR).
   `pkg/executor` HTTP server registers `ServiceVerifier(..., ServiceExecutor, ...)`.
   Each caller (router, kubewatcher, timer, mqt-fission-kafka, canaryconfig) wraps its executor-client transport with `ServiceSigner(..., ServiceExecutor, ...)`.
4. **Phase 3 — router internal listener** (advisory 4, the design references this doc).
   `ServiceRouterInternal` was reserved up front so advisory 4 can drop into the same library without touching the chart.
5. **Phase 4 — rotation procedure documentation** in the release-engineering runbook once Phase 1 has shipped in a release.

## Open questions

- Should the verifier also surface a structured `WWW-Authenticate` header on 401?
Current implementation returns a bare 401 with no body to minimize information leakage.
- Should we expose a Prometheus counter `fission_internal_auth_failures_total{reason="...", service="..."}`?
Useful for detecting mis-rotation; left out of Phase 1 to keep the change minimal.
- Should the rejection log level be configurable per service?
Currently info-level by default so operators can debug without enabling V(1) verbosity.

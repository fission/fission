# HMAC Application-Layer Authentication for Internal Services

- Tracking issues: GHSA-chf8-4hv6-8pg6, GHSA-7g8g-g937-26g8

## Summary

Add a shared-secret HMAC-SHA256 application-layer authentication scheme to Fission's internal HTTP services, starting with `storagesvc` and (in a follow-up PR) the router's internal callbacks.
The scheme is transport-agnostic, replay-resistant within a one-minute window with `±60s` skew tolerance, and ships behind a Helm toggle (`internalAuth.enabled`) so existing installations continue to work during upgrade.

## Motivation

Two coordinated security advisories — GHSA-chf8-4hv6-8pg6 ("StorageSvc has no authentication on `/v1/archive`") and its duplicate GHSA-7g8g-g937-26g8 — describe the same root cause: any pod able to reach `storagesvc` over the cluster network can list, download, upload, and delete every Fission package archive.
Storagesvc currently relies on `NetworkPolicy` (added in #3365) and namespace isolation as its only access controls.
That is brittle:

- **Mis-configured NetworkPolicy**: clusters that disable the default policy, override the namespace selector, or run the CNI in permissive mode lose the only barrier.
- **Compromised in-namespace pod**: a function pod or sidecar that gets RCE'd is, by selector, *inside* the policy and can talk to storagesvc directly.
- **Cluster-network egress controllers** (Cilium, Calico) that allow same-namespace traffic by default need explicit deny rules; not every operator writes them.
- **Future internal services**: the router will grow an internal listener for executor callbacks (advisory 4).
We need a reusable primitive instead of a one-off.

A symmetric HMAC scheme is the smallest meaningful step up from "no auth" that still works in every cluster (no cert-manager dependency, no service-mesh requirement) and gives us replay resistance without a server-side nonce store.

## Threat model

The scheme defends against:

1. **Lateral movement from a same-namespace compromised pod** — the attacker has cluster-DNS access to `storagesvc` but not the Fission control-plane secret.
2. **Mis-configured NetworkPolicy** that admits unintended workloads.
3. **Replayed captured request** outside a 60-second window (e.g. via cluster log scraping).

It does NOT defend against:

- An attacker that *also* exfiltrates `Secret/fission-internal-auth` (e.g. cluster-admin, full RBAC compromise).
At that point the secret rotates and the cluster needs full forensics, not application-layer auth.
- Active MITM on the in-cluster network.
That is the kubelet/CNI/mTLS layer's job.
- Cross-namespace requests (still gated by NetworkPolicy).
HMAC is an additional defence-in-depth layer, not a NetworkPolicy replacement.

## Goals

- Reject unsigned requests at every `/v1/archive` HTTP method (GET, POST, DELETE, HEAD).
- Sign every internal Fission client call to storagesvc (buildermgr, fetcher, executor).
- Allow `/healthz` to bypass signing so kubelet probes pass.
- Provide a reusable Go package (`pkg/auth/hmac`) so advisory 4 (router internal listener) can use the same primitive.
- Tolerate clock skew up to `±60s` between caller and verifier (typical kubelet drift).
- Support online secret rotation via a paired `OldSecret` accepted alongside the current `Secret` for a 5-minute overlap.
- Default-on for new installs (`internalAuth.enabled=true`); opt-in for in-place upgrades from the previous minor.

## Non-goals

- mTLS or a service mesh.
Out of scope; HMAC is the lowest common denominator that works without cert-manager.
- End-user authentication (CLI → router public listener).
That is a separate AuthN/AuthZ effort.
- CLI-side signing of public router requests.
The CLI talks to a public listener; HMAC here would just be an obfuscated bearer token.

## Design

### Canonical string

The HMAC input is:

```
<METHOD>\n
<PATH>\n
<SHA256_HEX(BODY)>\n
<UNIX_MINUTE>
```

where `UNIX_MINUTE = floor(unix_seconds / 60) * 60`.
The minute granularity ensures the signature is stable for the duration of a typical request (sub-second) and tolerates retries within the same minute without re-signing.
The body hash binds the signature to the bytes; absent it, an attacker who captured a signed `POST /v1/archive` could swap the body.

### Headers

- `X-Fission-Auth-Timestamp` — the caller's unix-seconds timestamp at request time.
- `X-Fission-Auth-Signature` — `hex(HMAC-SHA256(secret, canonical))`.

The timestamp is sent as the *exact* unix seconds the caller used, not the rounded minute, so the verifier can apply skew tolerance against its own clock.
The signature, however, is computed over the rounded minute — so two requests issued 30 seconds apart from the same client share a signature input but differ in `X-Fission-Auth-Timestamp`.

### Verification

```
1. If path ∈ Bypass set → pass through.
2. Read X-Fission-Auth-Timestamp; reject if missing or unparseable.
3. Read X-Fission-Auth-Signature; reject if missing.
4. abs(now - timestamp) > skew → reject.
5. Slurp body, hash it, recompute Sign(secret, method, path, body, timestamp).
6. crypto/hmac.Equal(want, got) → pass; re-inject body for downstream handler.
7. Else if OldSecret set → repeat 5–6 with OldSecret.
8. Else → 401.
```

Comparison uses `crypto/hmac.Equal` to avoid timing oracles.
The body is read once with `io.ReadAll`, the hash is computed, and `r.Body` is re-injected as `io.NopCloser(bytes.NewReader(body))` so downstream handlers (e.g. multipart parsers in `uploadHandler`) can re-read it.
For very large uploads (function archives can be hundreds of MB) this means we buffer the body in memory inside the verifier; we accept that cost because storagesvc already buffers via `multipart.Reader`.

### Bypass paths

- `/healthz` — kubelet probes have no signing path; an unsigned 200 must remain available.

No other bypasses.
In particular `/metrics` is served on a different port (8080) and never reaches the storagesvc mux.

### Secret distribution

The Helm chart materializes one secret, `Secret/fission-internal-auth` in the release namespace, with a 32-byte random key (alphanumeric, b64-encoded).
On upgrade the chart preserves the existing key via `lookup`; on first install it generates a fresh one.
The secret is mounted as `FISSION_INTERNAL_AUTH_SECRET` into the four internal services that talk to storagesvc:

- `storagesvc` (verifier)
- `buildermgr` (signer; uploads built archives)
- `executor` (signer; the storagesvc-client is reused by some executor codepaths)
- `router` (signer; `/v1/archive` callbacks during canary inspection)

A second optional key `oldSecret` is mounted as `FISSION_INTERNAL_AUTH_SECRET_OLD` (`optional: true`) for rotation windows.

### Rotation

To rotate the secret:

1. Operator copies the live `secret` value into `internalAuth.oldSecret`.
2. Operator sets `internalAuth.secret` to a fresh 32-byte value.
3. `helm upgrade` rolls all four deployments.
During the rollout the verifier accepts both keys; signers use the new key as soon as the pod restarts.
4. After every signer pod has rolled (≤5 minutes by default), operator runs a second `helm upgrade` with `internalAuth.oldSecret=""` to remove the old key.

### Backwards compatibility

Three layers of opt-out, in increasing priority:

- **Code level**: an empty `Secret` in `VerifierOpts` disables enforcement entirely.
That makes it safe to deploy the Go side first; nothing breaks until the env var is set.
- **Container env**: the env var `FISSION_INTERNAL_AUTH_SECRET` defaults to empty; storagesvc starts up unguarded if unset.
The signer in `pkg/storagesvc/client` likewise checks the env and only signs when a secret is present.
- **Helm value**: `internalAuth.enabled=false` skips the Secret resource and the env mounts entirely; the cluster behaves exactly as it does on `main` today.

For new installs `internalAuth.enabled=true` is the default.
For in-place upgrades from the previous minor we recommend leaving the default — the chart preserves the existing secret on subsequent upgrades and signed clients/verifier are introduced atomically by a single `helm upgrade`.

## Alternatives considered

### mTLS with cert-manager

Considered.
Stronger guarantees (transport binding, identity attestation) but requires cert-manager as a hard dependency, which we don't currently mandate.
Storagesvc's traffic profile is large body uploads; mTLS adds CPU overhead per connection.
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

## Limitations

The scheme has known limitations operators should plan around.

**Maximum body size.**
The verifier reads the entire request body into memory before computing the signature so the body bytes can be re-injected for downstream handlers (multipart parsers, etc.).
That cost is bounded by `VerifierOpts.MaxBodyBytes` (default 256 MiB, set on the storagesvc registration).
Bodies that exceed the cap are rejected with `413 Request Entity Too Large` *before* signature verification — i.e. an unauthenticated attacker cannot use a giant unsigned body to DoS storagesvc.
Operators that legitimately need to upload archives larger than 256 MiB should bump the cap rather than disable enforcement; the cap is the largest archive size we expect to see in practice.

**Replay within the skew window.**
A captured signed request can be replayed any number of times within the `SkewSec` window (default 60s) and will pass verification each time.
Adding nonce tracking would require a shared replay-cache store across all storagesvc/router replicas (Redis, a distributed cache, or a Lease-CR scheme).
That is out of scope for this PR; the 60-second window is short enough that the practical attack surface is limited to a recently-captured packet on the cluster network — at which point an attacker capable of sniffing in-cluster traffic has bigger problems.
A future change may add a nonce store if a use case justifies the operational cost.

**`fission-cli` does not sign storagesvc requests.**
The CLI's archive subcommands (`fission archive list`, `fission archive get`, etc.) talk to storagesvc directly via a port-forward and do not currently know how to sign.
Operators who use these commands must either:

- Run the cluster with `internalAuth.enabled=false` (acceptable on dev clusters; reverts to NetworkPolicy-only).
- Set `FISSION_INTERNAL_AUTH_SECRET` in the shell to match the value in `Secret/fission-internal-auth` (NOT recommended — pulls the cluster secret onto the operator's laptop).
- Prefer `kubectl exec` into the fetcher/builder pods, which already have the secret mounted, and run archive operations from there.

Standard CLI flows that go through `fission package` and `fission function` are unaffected — those commands talk to the Kubernetes API server, not storagesvc directly.

## Verification / test plan

- Unit tests: `pkg/auth/hmac/*_test.go` cover canonical-string formatting, sign/verify round-trip, `OldSecret` fallback, skew tolerance, healthz bypass, and body re-readability after the verifier passes.
- Integration test (`test/integration/suites/common/storagesvc_auth_test.go`) hits a port-forwarded storagesvc and asserts:
  - `GET /v1/archive` without headers → 401.
  - `GET /healthz` without headers → 200.
- `helm template charts/fission-all` renders the Secret and four `FISSION_INTERNAL_AUTH_SECRET` env mounts cleanly; the env var count is `≥ 8` (4 services × 2 env entries).
- Manual smoke: an existing function workflow (`fission fn run hello`) survives an in-place upgrade with `internalAuth.enabled=true`.

## Rollout phases

1. **Phase 1 — primitives, server enforcement, client signing, chart wiring** (this PR; Tasks A2.1-A2.11 of the security advisory plan).
   `internalAuth.enabled` defaults to `true`.
   Backwards compat carried by the empty-secret short-circuit.
2. **Phase 2 — extend to router internal listener** (advisory 4, the design will reference this doc).
3. **Phase 3 — rotation procedure documentation** in the release-engineering runbook once Phase 1 has shipped in a release.

## Open questions

- Should the verifier also surface a structured `WWW-Authenticate` header on 401?
Current implementation returns a bare 401 with no body to minimize information leakage.
- Should we expose a Prometheus counter `fission_internal_auth_failures_total{reason="..."}`?
Useful for detecting mis-rotation; left out of Phase 1 to keep the change minimal.

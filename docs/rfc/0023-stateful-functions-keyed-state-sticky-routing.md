# RFC-0023: Stateful functions — keyed state API and sticky routing

- Status: Proposed
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.N+1 (phase 1) / v1.N+2 (sticky routing)
- Requires: RFC-0021 statestore (`KVStore` capability); sticky routing additionally leans on the RFC-0002 EndpointSlice index (shipped, default-on).

## Summary

Give functions durable, scoped state without embedding database clients: `FunctionSpec.State` opts a function into a keyed KV API served by a new lightweight `statesvc` head and backed by `statestore.KVStore`, with per-function scoped tokens injected at specialization time.
A second phase adds **sticky routing**: the router consistent-hashes a declared request key onto the function's ready pods, so all requests for one key land on one pod — making in-memory caches coherent and enabling single-writer patterns (sessions, counters, carts, per-entity aggregation, agent memory).
This is Cloudflare Durable Objects / Dapr state territory: a differentiator, not Lambda parity (Lambda still has nothing here).

## Motivation

"FaaS is stateless" is the reason whole workload classes leave the platform: anything needing a session, a per-user counter, a shopping cart, a rate limit, or agent conversation memory must bring its own Redis, hand-wire credentials into every environment image, and re-implement scoping/quota per team.
The platform can do this once: it already injects per-pod configuration at specialization time (fetcher specialize payload), already derives scoped service keys via HKDF (`pkg/auth/hmac`, tenant-controller per-namespace keys), and — after RFC-0021 — owns a KV substrate with tenancy scoping built in.
Function code shrinks to `state.get/set/cas` against a local-cluster HTTP API, portable across environments and across state backends.

## Goals

- A minimal state HTTP API (get/set/delete/cas/list) scoped per function, with TTL, size quota, and namespace isolation enforced platform-side.
- Zero credentials in user code: capability arrives as an injected URL + scoped bearer token.
- Environment SDK helpers (node/python first) that are ~20 lines over plain HTTP, so any environment works without an SDK.
- Phase 3: opt-in sticky routing by request key over the existing ready-pod index, coherent in-memory caching above durable state.
- Works in the RFC-0018 local dev loop via the memory driver.

## Non-goals

- Cross-function shared state in v1 (a keyspace belongs to one function; sharing is a later explicit grant, not a default).
- Transactions across keys, queries/secondary indexes, or large blobs (KV values capped, default 256KiB; blobs belong in object storage).
- Strict single-writer *guarantees* under sticky routing (stickiness is an optimization; durable truth is the state API — see Design).
- Replacing user-owned databases for relational workloads.

## Design

### API surface on the Function CRD

```go
// FunctionSpec gains (presence = opt-in, like Tool and Streaming):
State *StateConfig `json:"state,omitempty"`

type StateConfig struct {
    // Keyspace defaults to the function name; explicit so a function can be
    // renamed without orphaning its data.
    Keyspace     string             `json:"keyspace,omitempty"`
    DefaultTTL   *metav1.Duration   `json:"defaultTTL,omitempty"`
    MaxValueBytes int64             `json:"maxValueBytes,omitempty"` // default 262144
    MaxKeys      int64              `json:"maxKeys,omitempty"`       // default 10000
    Backend      string             `json:"backend,omitempty"`       // "" = default driver; "redis" if deployed
    // Sticky (phase 3): how to extract the routing key from a request.
    Sticky *StickyConfig `json:"sticky,omitempty"`
}

type StickyConfig struct {
    Source StickySource `json:"source"` // Header | PathParam | QueryParam
    Name   string       `json:"name"`   // e.g. "X-Session-Id"
}
```

Webhook validation: keyspace charset, quota bounds, sticky source enum, and `Backend` referencing a configured driver (warning otherwise).

### statesvc (`pkg/statesvc`)

A new small `fission-bundle` head (`--stateApiPort` — deliberately distinct from the existing `--storageServicePort` — ClusterIP-only `svc/statesvc`), mirroring the MCP head's shape: Options-only `Start`, injectable listener, `/readyz` gated on `statestore.Capabilities.Ping` (RFC-0021) and Function-cache sync.

Routes (JSON over HTTP; ETag-style versions for CAS):

```
GET    /v1/state/{key}          → 200 {value} + X-Fission-State-Version | 404
PUT    /v1/state/{key}          body=value; If-Match: <version> → CAS (412 on conflict); TTL via X-Fission-State-TTL
DELETE /v1/state/{key}          If-Match honored
POST   /v1/state/{key}/cas      {expectVersion, value} — explicit CAS for clients without If-Match plumbing
GET    /v1/state?prefix=&cursor= → paged key listing
```

The scope is **not** client-supplied: it is derived entirely from the verified token (below), so a function cannot name another function's keyspace.
Quota (`MaxValueBytes`, `MaxKeys`, namespace byte budget) enforced here via the RFC-0021 `scopedStore` wrapper; violations are 413/429 with machine-readable bodies.

### Token derivation and injection

- Per-function token: HKDF over `FISSION_INTERNAL_AUTH_SECRET` with info `state:<ns>:<fn-name>:<keyspace>`, hex-encoded (the binary-env-var bite from the tenant-controller work applies verbatim).
  statesvc re-derives and compares — stateless verification, no token storage, same family as the `ServiceRouterInternal` key derivation.
- Injection point: the executor adds `FISSION_STATE_URL` + `FISSION_STATE_TOKEN` to the fetcher **specialize payload** for opted-in functions, so poolmgr generic pods (which are function-agnostic until specialization) receive it at specialize time; newdeploy/container set plain env vars at Deployment render.
  Rotation of the root secret rotates all tokens on next specialization; a dual-accept window (old+new secret) covers running pods, matching the internal-auth rotation story.
- statesvc joins the NetworkPolicy story: a policy admitting function pods (by the executor-managed pod labels) to statesvc only; statesvc itself is on the DSN allowlist per RFC-0021.

### SDKs and local dev

- `fission-state` helpers in node/python env repos: `get/set/cas/delete/list`, version-aware, reading the two env vars; everything is plain HTTP so unsupported environments lose nothing.
- RFC-0018 `fission function run` wires the memory driver behind a localhost statesvc, so stateful functions work offline with zero setup.
- CLI debugging: `fission fn state get|set|list|delete --name <fn>` (admin path through statesvc with operator JWT, honoring the same scope).

### Phase 3 — sticky routing

Goal: all requests carrying the same key land on the same specialized pod while the pod set is stable, so in-memory caches (above the durable state API) stay coherent and single-writer patterns avoid CAS churn.

- Seam: the EndpointSlice-fed index's admission path, not a pre-admission hook.
  In the shipped data plane, endpoint pick and concurrency admission are **one fused atomic operation**: the resolver (`pkg/router/resolver_fallback.go`) calls `endpointcache.Index.Admit`, which selects the best ready endpoint (the merged `Lookup` list contains not-ready endpoints too; `Admit`/`ReadyCount` filter), takes the concurrency slot, and returns the `Release` closure.
  Sticky routing therefore lands as a variant selection policy *inside* `Admit` (an HRW pick restricted to ready endpoints, with the routing key passed down from the resolver after extraction per `StickyConfig`); **rendezvous hashing** (HRW) gives minimal reshuffle on pod add/remove with no ring state — a pure function of (key, ready endpoints).
- Requests missing the key fall back to the default pick (documented; not an error).
- Accounting invariant: because the sticky pick stays inside `Admit`, the router-admitted vs executor-resolved split is untouched — `Release != nil` ⟺ router-admitted still holds, and `pkg/router/transport.go` needs no changes.
  If the hashed-to endpoint is saturated, `Admit` falls back to its normal overflow behavior rather than queueing on the sticky target (stickiness is best-effort under saturation; documented).
- Churn semantics: on scale-up/down or pod replacement, a key's owner may move; because durable truth is the state API and memory is a cache, this is a latency event (cache warm-up), never a correctness event.
  Functions wanting stronger fencing use CAS versions as fencing tokens (documented pattern).
- Cold path: if the index has no endpoints (cold function), the normal executor cold-start RPC path runs unchanged; stickiness applies once endpoints exist.
  Legacy data plane (`endpointSliceCache.mode=off`) does not support stickiness (validated warning), keeping the pinned-legacy CI leg meaningful.

## Invariants & verification

**Invariants.**

- S1 *(scope isolation)*: a token derived for function A can never read or write function B's keyspace — the scope comes from the verified token, never from the request.
- S2 *(no lost updates)*: concurrent CAS writers to one key are linearizable; exactly one writer wins per version.
- S3 *(quota soundness)*: `MaxValueBytes`/`MaxKeys`/namespace budgets are never exceeded, including under concurrent writes racing the counters.
- S4 *(sticky determinism)*: the sticky pick is a pure function of (key, ready endpoint set) — same inputs, same pod, on every router replica independently.
- S5 *(minimal reshuffle)*: removing one pod moves only the keys that mapped to it; adding one pod moves only keys that now map to it (the HRW property).
- S6 *(stickiness is an optimization, never a correctness dependency)*: durable truth lives behind the state API; any request may legally land anywhere.

**Verification.**

- S1 is fuzzed, not just example-tested: `go test -fuzz` mutates valid tokens (bit flips, truncation, scope-field splices, re-encoding) against the verifier and asserts nothing but the exact derived token authenticates — a fuzzer is the right adversary for an HKDF-scope check.
- S2: `porcupine` linearizability checking over recorded concurrent histories driven through the real statesvc HTTP surface (not just the driver), plus the classic get→cas counter integration test asserting zero lost increments under load.
- S3: `pgregory.net/rapid` sequences of writes/deletes racing quota boundaries against the memory model.
- S4/S5 are pure-function properties, two-line `rapid` tests: distribution balance within χ² tolerance for N keys over M pods, and the only-removed-pod's-keys-move assertion under random churn sequences.
- TTL behavior and token-rotation dual-accept windows run in `testing/synctest` bubbles (virtual clock, deterministic, no sleeps).
- The sticky resolver path runs under `-race` with concurrent endpoint churn (the same build-vs-serve race family the gorilla `Methods()` bite documented).
- No model checking here: statesvc is a stateless proxy over the RFC-0021 KV whose CAS protocol is already covered by the substrate's verification; the concurrency lives in the driver, not this layer.

## Alternatives considered

- **Dapr sidecar for state** — per-pod sidecar injection conflicts with poolmgr's generic-pool model (pods exist before their function identity is known), and drags a second control plane; rejected as before (RFC-0021).
- **Credentials-to-functions (inject the DSN)** — simplest, but every environment grows DB clients, quota/tenancy become unenforceable, and rotating one leaked function's access means rotating the store; rejected.
- **State facet on the router internal listener** — fewer pods, but couples the request hot path's availability and NetworkPolicy surface to state traffic and widens the GHSA-hardened listener's responsibilities; a separate ClusterIP head is cheap and independently scalable.
- **Session affinity via Kubernetes Service `sessionAffinity: ClientIP`** — wrong key (client IP ≠ logical key), unavailable for the router's direct pod-endpoint dials, and invisible to the resolver's admission accounting.
- **CRDs as the KV backend** — etcd write rates and object caps; rejected (RFC-0021 Motivation).

## Backward compatibility

Additive: optional CRD field (nil = exactly today's behavior), new optional head render-gated on `statestore.enabled`, no changes for functions that do not opt in.
Sticky routing changes endpoint *selection order* only for opted-in functions on the default data plane.

## Rollout phases (one PR each, bisectable)

1. `StateConfig` CRD field + codegen + webhook validation; statesvc head with KV routes, token verification, quotas; Helm component + NetworkPolicies; CLI `fn state` commands.
2. Executor/fetcher injection (specialize payload + newdeploy/container env), integration tests with a real function reading/writing state.
3. Node + Python SDK helpers; RFC-0018 local-loop wiring (memory driver).
4. Sticky routing: `StickyConfig`, HRW pick in the resolver, metrics (`fission_router_sticky_hits_total`, `_reshuffles_total`), bench scenario.

## Verification / test plan

- statesvc unit tests: token scope forgery attempts (function A's token against keyspace B → 403), CAS conflict matrix, quota rejections, TTL expiry.
- Integration: end-to-end counter function (get→cas loop) under concurrent load asserting no lost updates; specialization injection on poolmgr and env-var path on newdeploy; token rotation dual-accept window.
- Sticky phase: distribution test (N keys over M pods, HRW balance within tolerance), pod-kill reshuffle affects only the killed pod's keys, `-race` on the resolver path under churn (the gorilla-Methods-style build-vs-serve races live here).
- RFC-0020 bench: state-API latency scenario (target p99 < 5ms in-cluster, measured in both embedded mode and against an external Postgres) and sticky cache-hit-rate scenario.

## Open questions

- Admin/global access model for `fission fn state` when authentication is disabled (leaning: require the internal auth secret path, refuse otherwise — fail closed like MCP).
- Whether `Backend: redis` selection is per-function (as drafted) or per-namespace policy (operator-controlled); per-function is more flexible, per-namespace easier to reason about for capacity.
- Key listing pagination limits and whether `List` is even exposed to functions in v1 (needed for cleanup patterns, but it is the most abusable endpoint).

# RFC-0021: Statestore — a standard durable-state interface for the control plane

- Status: Implemented ([#3574](https://github.com/fission/fission/pull/3574), merged 2026-07-14): `pkg/statestore` with memory/SQLite/Postgres/HTTP-client drivers, external + embedded modes, KVStore/EventLog/Queue capabilities and the shared conformance suite. The optional Redis KV driver is deferred (YAGNI — no consumer needs it). Consumed by RFC-0024 (async), RFC-0027 (eventing).
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.N (enabler for RFC-0022 workflows, RFC-0023 stateful functions, RFC-0024 async invocation)
- Requires: nothing new from Kubernetes; an **externally deployed** Postgres (recommended for production) or the built-in embedded mode for local/small deployments — Fission never deploys or manages a database product.

## Summary

Introduce `pkg/statestore`: a small, pluggable Go interface layer for durable transactional state, with three capability interfaces (`KVStore`, `EventLog`, `Queue`) and two deployment modes — **external** (an externally deployed, user-managed Postgres; the recommended production path) and **embedded** (a single-replica Fission-owned store backed by pure-Go SQLite; the zero-dependency default for local and small deployments).
Drivers: Postgres (reference), SQLite (embedded mode), an HTTP client driver (consumers of the embedded store), optional Redis for the KV capability, and in-memory for tests and local development.
Fission never deploys or manages a database product — the chart wires credentials and picks the mode, nothing more.
This is the shared substrate for durable workflows (RFC-0022), stateful functions (RFC-0023), and async invocation with retries/DLQ (RFC-0024).
It is explicitly **not** built on `pkg/storagesvc`: storagesvc is a blob archive service (local FS or S3, plus an archive pruner) — the wrong shape for keyed reads, CAS writes, append-ordered logs, or leased queues, and too fragile to become the platform's state backbone.

## Motivation

Three proposed features each need durable state with different access patterns:

- Workflow execution history (RFC-0022): append-only, ordered, replayable — an event log.
- Per-function keyed state (RFC-0023): get/set/CAS with TTL and quotas — a KV store.
- Async invocations (RFC-0024): enqueue, lease with visibility timeout, ack/nack, dead-letter — a queue.

Fission's control plane today has **no** transactional store of any kind (verified: no redis/postgres/sql imports anywhere under `pkg/`; the only persistence is the blob-shaped storagesvc and the Kubernetes API itself).
Without a shared substrate, each feature would grow its own ad-hoc persistence, tripling the operational surface (three schemas, three credential paths, three backup stories) and locking each to a specific backend.
CRD-based storage (state in `status` or dedicated CRs) is not viable at data-plane rates: etcd write amplification, 1.5MiB object caps, and apiserver QPS budgets make it suitable only for control metadata, which the features keep there anyway.

One interface layer means: one Helm story, one credential/secret path, one place to enforce multi-tenant scoping and quotas, and backend portability (Postgres today, DynamoDB/etcd drivers later) without touching consumers.

## Goals

- A minimal, stable Go interface (`pkg/statestore`) that RFC-0022/0023/0024 consume.
- Postgres reference driver implementing all three capabilities; SQLite driver for embedded mode; in-memory driver for unit tests and `fission function run`; Redis driver for the KV capability where latency matters.
- Deployment story with two modes: **external** (recommended — bring-your-own Postgres DSN; the chart only wires credentials, never deploys a database) and **embedded** (default for local/small deployments — a single-replica Fission-owned store pod backed by pure-Go SQLite on a PVC); chart render fails if a dependent feature is enabled without a statestore mode, mirroring the `mcp.enabled && !authentication.enabled && !mcp.allowInsecure` gate.
- Tenancy scoping and per-scope quota enforcement in the interface layer, not per driver.
- User functions never receive store credentials; only control-plane components link drivers directly.

## Non-goals

- Blob storage (stays in storagesvc / OCI registries per RFC-0001/0012).
- A user-facing state HTTP API (that is RFC-0023's `statesvc`, a consumer of this layer).
- Shipping or operating a database product: the chart never deploys Postgres/Redis (no StatefulSet, no operator dependency); production state stores are external and user-managed, and the embedded mode is explicitly single-replica with no HA/replication story.
- Exactly-once semantics; the substrate provides CAS and at-least-once leases, and consumers build idempotency on top.
- Migrating existing subsystems (canary state, package archives) onto statestore in v1.

## Design

### Capability interfaces

Three narrow interfaces rather than one god-interface, so drivers can implement only what they are good at and consumers declare exactly what they need.

```go
package statestore

// Scope carries tenancy: every operation is namespaced to a Fission
// namespace, an owner object (function or workflow), and a keyspace.
// Quota and authz are enforced here, above the driver.
type Scope struct {
    Namespace string
    Owner     string // "<kind>/<name>", e.g. "function/orders"
    Keyspace  string
}

type KVStore interface {
    Get(ctx context.Context, s Scope, key string) (Value, error)          // Value carries bytes + Version
    Set(ctx context.Context, s Scope, key string, val []byte, o SetOptions) error // o.IfVersion → CAS; o.TTL
    Delete(ctx context.Context, s Scope, key string, ifVersion int64) error
    List(ctx context.Context, s Scope, prefix string, page Page) (KeyPage, error)
}

type EventLog interface {
    Append(ctx context.Context, stream string, expectedSeq int64, events []Event) (int64, error) // optimistic concurrency
    Read(ctx context.Context, stream string, fromSeq int64, limit int) ([]Event, error)
    Trim(ctx context.Context, stream string, belowSeq int64) error
}

type Queue interface {
    Enqueue(ctx context.Context, queue string, msg Message, o EnqueueOptions) (string, error) // o.Delay, o.DedupKey
    Lease(ctx context.Context, queue string, n int, leaseFor time.Duration) ([]LeasedMessage, error)
    Ack(ctx context.Context, id string) error
    Nack(ctx context.Context, id string, retryAfter time.Duration) error
    Kill(ctx context.Context, id string, reason string) error // move to the queue's dead-letter table
    DeadLetters(ctx context.Context, queue string, page Page) ([]DeadMessage, error)
    Redrive(ctx context.Context, queue string, ids []string) error
}
```

Version semantics: `Set` with `IfVersion: 0` means create-only; a mismatched version returns `ErrVersionConflict` (a sentinel, checked with `errors.Is`).
`Lease` is at-least-once: a message whose lease expires without Ack becomes leasable again; consumers must be idempotent or use `DedupKey`.

### Postgres reference driver (`pkg/statestore/postgres`)

One dependency (`jackc/pgx/v5`) implements all three capabilities with boring, well-understood SQL:

- **KV**: `state_kv(namespace, owner, keyspace, key, value bytea, version bigint, expires_at timestamptz, PRIMARY KEY(namespace, owner, keyspace, key))`; CAS is `UPDATE ... WHERE version = $n`; TTL rows are reaped by an index-scan sweep goroutine (and filtered on read so expiry is exact).
- **EventLog**: `state_events(stream, seq bigint, type, payload jsonb, at timestamptz, PRIMARY KEY(stream, seq))`; `Append` inserts inside a transaction guarded by `WHERE max(seq) = expectedSeq`, so concurrent appenders get `ErrVersionConflict` instead of interleaving.
- **Queue**: `state_queue(id, queue, payload bytea, visible_at timestamptz, attempts int, dedup_key, ...)`; `Lease` is the standard `SELECT ... FOR UPDATE SKIP LOCKED` pattern (same family as River/Temporal-lite job queues), setting `visible_at = now() + leaseFor`; `Kill` moves the row to `state_dead(queue, id, payload, reason, died_at)`.

Schema is created/migrated by an idempotent embedded migration set (plain `//go:embed` SQL files, sequential version table) run by whichever component connects first; migrations are additive-only.
Connection config comes from a mounted Secret (`statestore-postgres`, key `dsn`); pool sizing via env with conservative defaults.

### Other drivers

- **SQLite (`pkg/statestore/sqlite`)** — the embedded-mode backend: pure-Go `modernc.org/sqlite` (no cgo, so the static image build is untouched), WAL mode, writes serialized via `BEGIN IMMEDIATE`; same table shapes and migration set as Postgres, with the queue lease relying on `visible_at` + the epoch guard (single-writer semantics make `SKIP LOCKED` unnecessary).
- **Client (`pkg/statestore/client`)** — a thin HTTP client implementing the three capability interfaces against the embedded store service (see Deployment); consumers hold `KVStore`/`EventLog`/`Queue` interfaces and are byte-identical across modes, never knowing whether Postgres or the embedded store is behind them.
- **In-memory (`pkg/statestore/memory`)**: all three capabilities behind plain mutex-guarded maps; powers unit tests and the `fission function run` local loop (RFC-0018) so stateful functions work offline.
- **Redis (`pkg/statestore/redis`)**: `KVStore` only in v1 (hash-per-scope, `WATCH`/`MULTI` CAS, native TTL); declared per-capability, so a deployment can serve KV from Redis and EventLog/Queue from Postgres.
- The interfaces and error sentinels are public; external drivers (DynamoDB, etcd for tiny installs) can land out-of-tree first.

### Capability negotiation and wiring

A single factory reads config once at component start:

```go
// statestore.Open returns the configured driver set; a consumer asks for the
// capabilities it needs and fails fast at startup if one is missing.
caps, err := statestore.Open(ctx, statestore.FromEnv())
kv, err := caps.KV()        // ErrCapabilityUnavailable if not configured
err = caps.Ping(ctx)        // health affordance for consumers' /readyz gates
```

Consumers (workflow controller, router async dispatcher, statesvc) receive interfaces through their `Options` structs, consistent with the injectable-listener/Options-only constructor convention from the service-flexibility refactor — no env reads inside library constructors, so unit tests inject the memory driver directly.

### Deployment (Helm)

Fission does **not** ship a database: the state store is deployed externally and consumed by Fission; the chart's job is credential wiring and mode selection only.
For local and small deployments, an **embedded** mode provides a working default without any external dependency — Fission's own binary, not a database product.

```yaml
statestore:
  mode: ""             # "external" | "embedded" — required once any dependent feature is on
  external:
    dsn: ""            # externally deployed Postgres (recommended for production); stored into a Secret
    existingSecret: "" # or reference a pre-created Secret
    redis:
      addr: ""         # optional external Redis for the KV capability
  embedded:
    storageSize: 1Gi   # PVC for the embedded store pod
```

- **external** (recommended for production): every consumer (workflow controller, router dispatcher, statesvc) links the Postgres driver directly against the user-managed database; no extra Fission pods.
  Lifecycle, HA, backups, and upgrades of the database are entirely the user's, by design.
- **embedded** (the local/small-deployment default): a single-replica `statestore` Deployment (a new small `fission-bundle` head) owns a PVC-backed SQLite file and serves the capability API over HTTP on a ClusterIP Service, authenticated with an HKDF-derived service key like the other internal surfaces; consumers use the `client` driver against it.
  Single-writer by construction (one replica owns the file), explicitly not HA, with a documented migration path: point `external.dsn` at a real Postgres and flip the mode — consumers are identical across modes, and a `fission statestore export/import` CLI pair moves existing data.
  NOTES.txt states the durability posture plainly (data lives on one PVC).

Render-time gates (a `{{ required ... }}` in each dependent component's deployment template, same pattern as the MCP auth gate in `templates/mcp/deployment.yaml`): `workflows.enabled || functionState.enabled || asyncInvocation.enabled` without a valid `statestore.mode` fails the render with an actionable message.

### Tenancy, quota, and access model

- All enforcement lives in a `scopedStore` wrapper applied above the driver: key-count and byte quotas per `Scope` (defaults per namespace, overridable via the owning CR's spec), and metric emission (`fission_statestore_ops_total`, `_errors_total`, `_quota_rejections_total`, `_lease_expirations_total` via the RFC-0019 OTel meters).
- Only control-plane pods (workflow controller, router, statesvc, buildermgr-adjacent components) mount the DSN Secret (external mode) or hold the embedded store's service key (embedded mode).
- User functions reach state exclusively through RFC-0023's `statesvc` HTTP API with scoped bearer tokens; neither the DSN nor the embedded store's service key ever enters a function pod.
- In embedded mode, a NetworkPolicy admits only control-plane pods to the embedded store Service by `svc:` label, following the router-internal allowlist convention.

## Invariants & verification

The substrate's correctness claims are stated as numbered invariants and checked at three layers: model checking of the protocol design, property-based conformance of the drivers, and runtime accounting gates.

**Invariants.**

- Q1 *(SettledAtMostOnce)*: a queue message reaches a terminal settle (acked or dead) at most once.
- Q2 *(NoOrphanedCurrentDelivery)*: only the current lease decides a message's outcome — a stale delivery's ack/nack/kill never lands while a newer lease's delivery is in flight.
- Q3: dead-lettering happens only with the attempt budget spent (or MaxAge exceeded / permanent error, per RFC-0024).
- Q4: at most `MaxAttempts` deliveries of a message ever start.
- K1: KV CAS is linearizable per key — no lost updates under concurrent `Set(IfVersion)`.
- K2: TTL is exact on read — an expired key is never returned, even before the sweeper runs.
- E1: `EventLog.Append` with `expectedSeq` admits exactly one writer per sequence slot; streams are gap-free and append-ordered.
- T1 *(conservation)*: at all times, enqueued = in-flight + acked + dead — nothing vanishes, nothing duplicates.

**Design-time model checking.** The queue lease/settle protocol is specified in [`specs/queue.tla`](specs/queue.tla) and TLC-checked (safety, small bounds) — Q1–Q4 verified.
The negative model (`specs/queue-unguarded.cfg`, expected to fail) shows Q2 breaking without a lease-epoch guard, which is why the Postgres driver's settle statements are `... WHERE id = $1 AND epoch = $2`, never id alone.
A finding from checking worth recording: "work that ever succeeded is never dead-lettered" is *unachievable* under at-least-once + visibility timeouts (a success slower than its lease is rightly rejected as stale and the retry may dead-letter) — this is inherent SQS-style semantics, and it is why lease duration must exceed the consumer's work timeout.
The EventLog CAS discipline is checked by [`specs/workflowfold.tla`](specs/workflowfold.tla) (see RFC-0022).
Protocol changes must change the spec first; CI runs TLC (pinned tla2tools) on both green configs plus an assertion that the unguarded config still fails.

**Implementation-time verification.**

- The driver conformance suite (`statestoretest.RunConformance`) is property-based: `pgregory.net/rapid` generates random operation interleavings and asserts the Postgres driver's observable behavior equals the in-memory model's (model-based testing; the memory driver is the executable spec).
  Do not use the stdlib `testing/quick` — it is frozen; `rapid` is its maintained successor.
- KV linearizability (K1): record concurrent CAS histories against real Postgres and check them with `porcupine` (the checker etcd/TiDB use).
- All timing behavior — lease expiry, visibility timeouts, TTL expiry (K2), backoff delays — is tested inside `testing/synctest` bubbles (stable since Go 1.25; this repo is on Go 1.26): the bubble virtualizes `time.Now`/timers, so tests are deterministic and instant with **no injectable-clock seam and no sleeps**; drivers simply use the standard `time` package.
- Encoding/parsing boundaries (value serialization, DSN/config parsing) get native `go test -fuzz` targets.

**Runtime gates.** The conservation identity T1 is exported as `fission_statestore_conservation_drift` (computed from the op counters) with a CI bar of zero, following the `fission_router_route_resync_drift_total` pattern.

## Alternatives considered

- **storagesvc as the backend** — rejected: blob-only semantics (whole-object PUT/GET), no CAS, no ordered append, no leases; its S3 mode would turn every queue poll into object listing; and it is operationally the most fragile piece we run.
  Explicitly ruled out by maintainer direction.
- **CRDs/etcd for all state** — rejected for data-plane rates (see Motivation); CRDs remain the home for specs and status summaries only.
- **Dapr** — a full sidecar runtime and CRD suite as a hard dependency to obtain what is ~1.5k lines of SQL driver; too heavy, and its sidecar-per-pod model clashes with poolmgr's generic-pool specialization flow.
- **gocloud.dev (Go CDK)** — `docstore` lacks CAS-with-version and Postgres support, `pubsub` lacks visibility-timeout leases and dead-letter inspection; we would wrap it so thickly that the portability benefit disappears.
  (The repo already walked away from `stow` once, in the RFC-0005 footprint work.)
- **NATS JetStream** — good EventLog/Queue, weak KV story at our semantics, and re-introduces the always-on broker dependency that the old `fission-workflows` was criticized for; still available later as an out-of-tree driver.
- **Bundling a Postgres StatefulSet in the chart** — considered and rejected on maintainer direction: Fission would own database lifecycle, upgrades, and data-loss blame for a product it doesn't build; every serious deployment brings its own Postgres anyway, and the embedded SQLite mode covers the local/small case with Fission's own binary instead of a third-party database deployment.
- **Embedded SQLite linked directly into every consumer** (no store pod) — rejected: the queue and event log are *shared* state across the workflow controller, router dispatcher, and statesvc, and SQLite is single-writer per file; a file-per-component split would break cross-component features, so embedded mode centralizes the file behind one small service instead.

## Backward compatibility

Purely additive: a new package, a new optional chart component, no changes to existing CRDs or subsystems.
Clusters that enable none of the dependent features see no new pods and no new dependencies.

## Rollout phases (one PR each, bisectable)

1. `pkg/statestore` interfaces + memory driver + `scopedStore` quota/metrics wrapper, full unit tests.
2. Postgres driver (pgx, embedded migrations) with dockertest-or-envtest-style integration tests behind a build tag; CI leg with a Postgres service container.
3. SQLite driver + the embedded store head (`fission-bundle` `--statestorePort`) + HTTP client driver, sharing the conformance suite.
4. Helm `statestore` block: mode selection, external Secret plumbing, embedded Deployment/PVC/Service, render gates, NetworkPolicy; `fission statestore export/import` CLI for embedded→external migration.
5. Redis KV driver (optional, after a consumer exists that wants it).

## Verification / test plan

- Driver conformance suite: a shared `statestoretest.RunConformance(t, factory)` exercised by the memory, Postgres, SQLite, and client-against-embedded drivers — CAS conflict matrices, TTL expiry exactness, lease expiry → re-lease, dedup, dead-letter/redrive round-trip, `Append` concurrency (two writers, one wins).
  One suite across all four is what makes "consumers are identical across modes" a tested claim rather than a slogan.
- Race coverage under `-race` for the memory driver (it is the concurrency model documentation).
- Chart: `helm template` drift tests for the render gates (feature-on/statestore-off must fail).
- Load sanity via the RFC-0020 bench harness once RFC-0024 lands (queue throughput under the c500 saturation scenario).

## Open questions

- Whether embedded mode ships in the same release as external or one release later (leaning: same release — evaluation friction kills adoption of the dependent features, and embedded is the default someone kicks the tires with).
- Whether the embedded store head should also accept the Postgres wire protocol later so `psql`/standard tooling can inspect it (out of scope for v1; the HTTP capability API plus the export CLI cover debugging).
- Schema-migration ownership when multiple components race to connect first in external mode (leaning: advisory-lock around the migration set, any component may run it; moot in embedded mode — one owner).
- Whether `EventLog.Trim` is needed in v1 or workflow-history GC waits for a retention phase in RFC-0022.

# RFC-0027: Statestore-backed eventing — a built-in message-queue provider

- Status: Proposed
- Tracking issue: TBD
- Supersedes: — (completes the topic-destination step RFC-0024 deferred as design decision D1)
- Targets: Fission v1.N+2
- Requires: RFC-0021 statestore (`Queue`, `EventLog`, `KVStore`) with a small Phase-1 EventLog extension (see Design); RFC-0024 async invocation — Phases 1-2 are on `main`, and the DLQ admin API/CLI + `Queue.Purge` that Phase 3 of this RFC extends are in-flight in PR #3580 (this RFC's broker-egress phase sequences after it).
  Composes with RFC-0022 workflows (steps can publish/consume topics) and the RFC-0002/0013 data plane (unchanged).

## Summary

Make the RFC-0021 statestore a first-class **eventing substrate**: a `Publisher` interface with a statestore implementation, durable **topics** on the `EventLog`, and `messageQueueType: statestore` as a built-in MQ-trigger provider — so publish/subscribe function pipelines work with **zero external brokers**.
RFC-0024's topic destinations, currently rejected at admission ("not yet supported"), become real: `onSuccess`/`onFailure` can publish the result envelope to a statestore topic, and any `MessageQueueTrigger` on that topic fires downstream functions.
External brokers (Kafka et al.) remain the scale path: the same `Publisher` interface gains broker implementations behind a durable egress queue, so upgrading from built-in to Kafka is a one-line config change, not a rewrite.

## Motivation

Fission's eventing story today requires external infrastructure before the first event flows: an MQ trigger needs Kafka (or a KEDA-supported broker) deployed, secured, and wired in.
That is the single largest obstacle to an end-to-end bootstrap — `helm install fission` should yield a platform where HTTP, async invocation, retries, DLQ, destinations, **and** topic-based fan-out all work out of the box.

RFC-0021 deliberately built the three primitives this needs — a leased `Queue`, an append-only replayable `EventLog`, and a CAS `KVStore` — and RFC-0024 already ships a production consumer of the `Queue` (the async dispatcher).
What is missing is small and compositional: an outbound publish path, a topic convention on the `EventLog`, and an MQ-trigger provider that reads it.
With those, the full loop closes with no new infrastructure:

```
function A ──onSuccess──▶ topic "orders" (EventLog) ──MQ trigger──▶ function B
     ▲                                                      │
     └────────── async invoke, retries, DLQ ◀───────────────┘
```

- **Dev/CI/edge**: everything works on the embedded SQLite store — kind cluster, `helm install`, done.
- **Production**: the same pipeline runs on external Postgres; where throughput demands it, the topic switches to Kafka by changing `messageQueueType` — function code and trigger CRs are otherwise identical.

## Goals

- `Publisher` — one outbound publish interface, mirroring the inbound `MessageQueue` (Subscribe/Unsubscribe) interface; statestore implementation first, broker implementations behind it.
- Durable namespaced **topics** on the `EventLog` with per-subscriber cursors, replayable, with honest retention.
- `messageQueueType: statestore` as a first-class `MessageQueueTrigger` provider, honoring the existing spec fields (`Topic`, `ResponseTopic`, `ErrorTopic`, `MaxRetries`, `ContentType`).
- Un-defer RFC-0024 topic destinations for the statestore type: `onSuccess`/`onFailure` → topic publish of the result envelope.
- External-broker egress that keeps broker SDKs and credentials **out of the router**: a durable egress queue consumed by a publisher loop in the mqtrigger subsystem.
- At-least-once everywhere, with the same observability discipline as RFC-0024 (metrics for every drop, DLQ for every failure).

## Non-goals

- Shipping or operating a broker: the statestore provider rides the RFC-0021 store the operator already chose; Fission never deploys Kafka/NATS/Redis (the RFC-0021 stance, extended).
- Kafka-parity throughput or partitioned ordering: the built-in provider targets the long tail of small/medium eventing; brokers remain the high-scale path (see Limits).
- Exactly-once delivery: at-least-once with idempotent consumers, consistent with RFC-0024 and every visibility-timeout/cursor system.
- Cross-namespace topics: a topic belongs to one namespace, matching RFC-0024's same-namespace destination rule (R6).
- Schema registry / typed events: payloads stay opaque bytes.

## Design

### The core decision: Queue for work, EventLog for topics

A broker topic is pub/sub with fan-out — N independent subscribers each see every message.
A statestore `Queue` is competing-consumers — one message settles exactly once.
Forcing fan-out onto the `Queue` (per-subscriber enqueue at publish time) would mean write amplification, no replay, and subscribers having to exist before the publish — so topics map to the **`EventLog`** instead:

| Semantics | Primitive | Used by |
|---|---|---|
| Competing consumers, settle-once, retry/DLQ | `Queue` | async invocations (RFC-0024), broker **egress** jobs |
| Fan-out, per-subscriber cursor, replay | `EventLog` | **topics** (destinations + MQ triggers) |

A topic named `orders` in namespace `ns` is the stream `topic/<ns>/orders`.
Publishing appends one event (the publisher supplies `Type` + `Payload`; the store stamps `Seq` and `At`); each subscriber tracks its own position; `Trim` is retention.

**EventLog extension (Phase 1, explicit interface + wire + conformance change).**
The current contract is CAS-only: `Append(stream, expectedSeq, events)` succeeds only at the exact head, returns `ErrVersionConflict` otherwise, and offers no way to read the head — `Read` can only discover it by walking the backlog, and the HTTP client driver drops the head that the SQL drivers happen to return alongside a conflict.
CAS is right for RFC-0022's workflow folds (interleave prevention is the point) but wrong for topics: topic events are independent, so serializing publishers through client-side CAS retries would self-inflict O(n²) append attempts under fan-in to a hot topic.
Phase 1 therefore extends the contract in two small, documented ways, across the interface, both SQL drivers, the memory driver, the HTTP wire (`httpapi` + client), and the conformance suite:

- `Append(stream, AppendAny, events)` — the sentinel `AppendAny = -1` appends unconditionally at the current head (the SQL drivers' `head = head + n` update is already an atomic server-side increment, so this is a strict simplification, not a new mechanism).
  Topic publishes use this; a publish is a single store round-trip with no retry loop, and E1 holds because `Append` still returns only after the write is durable.
- `Head(ctx, stream) (int64, error)` — a cheap head read, needed by start-at-head subscription, the lag gauge, and any CAS caller that wants to resynchronize without walking the log.

### `TopicPublisher` — the outbound mirror of `MessageQueue`

```go
// pkg/mqtrigger/mqpub (extracted; shared by mqtrigger response/error topics,
// RFC-0024 topic destinations, and the egress loop). Named TopicPublisher to
// stay distinct from pkg/publisher, the existing router-POST webhook publisher.
type TopicPublisher interface {
    // Publish durably hands payload to topic on the given provider.
    // For statestore it returns only after the event is appended (E1);
    // for broker types it returns only after the broker acks.
    Publish(ctx context.Context, mqType fv1.MessageQueueType, topic string, contentType string, payload []byte) error
}
```

Two implementations in this RFC:

- **`statestore`**: `EventLog.Append` to `topic/<ns>/<topic>` (namespace supplied by the constructor — publishers are namespace-scoped, upholding R6).
- **Broker types (kafka first)**: extracted from the producer that already exists inside the kafka `Subscribe` path (`sarama.SyncProducer` in `pkg/mqtrigger/messageQueue/kafka`), so mqtrigger's own `ResponseTopic`/`ErrorTopic` publishing and this RFC share one broker implementation instead of duplicating it.

Constructors stay deterministic (no env reads) per the established publisher contract, so unit tests with fakes are trivial.

### `messageQueueType: statestore` — the built-in MQ-trigger provider

A new provider registered alongside kafka in `pkg/mqtrigger/messageQueue`, running in a classic `--mqt` fission-bundle head.
The classic head is one-deployment-per-MQ-type (`MESSAGE_QUEUE_TYPE` selects the provider at start), so this is a **new chart deployment** — `mqt-fission-statestore`, rendered when eventing is enabled, mirroring the existing `mqt-fission-kafka` template — not a change to the kafka head; it works in both embedded and external store modes (see Scaling for the KEDA story).
`Subscribe(trigger)` starts one consumer loop per trigger (subscriptions are a leader-only runnable, as in the existing providers, so double-consumption is confined to leadership transitions — which at-least-once already permits):

1. `events := EventLog.Read("topic/<ns>/<topic>", cursor, batch)`.
2. For each event: POST the payload to the router internal listener at `utils.UrlForFunction(...)` with the `ServiceRouterInternal` HMAC signature and the trigger's `ContentType` — byte-identical to how the kafka provider delivers, so executor admission, EndpointSlice resolution, and poolmgr accounting apply unchanged.
3. 2xx → if `ResponseTopic` is set, `Publish` the response body to it; advance the in-memory cursor.
4. Failure → retry up to `MaxRetries` with backoff; still failing → `Publish` the original payload to `ErrorTopic` (existing mqtrigger semantics) and advance anyway — **poison isolation (E5)**: one bad event cannot wedge the subscription.
5. Persist the cursor with a CAS `KVStore.Set` after each batch, under the house Scope convention: `Scope{Namespace: ns, Owner: "messagequeuetrigger/<name>", Keyspace: "cursor"}`, value = seq, `IfVersion` for the CAS.

A crash between delivery and cursor persist redelivers the tail of the batch — at-least-once (E2), the same contract as every other provider.
The CAS write makes a split-brain double-consumer (e.g. during a leadership transition) safe: both deliver (at-least-once permits it), but the cursor never regresses.
A new subscriber starts at the current head (`Head(stream)`); replay-from-beginning is a possible later spec knob, kept out of v1.

### Topic destinations (closing RFC-0024 D1)

`DestinationRef.Validate` drops the blanket "topic destinations are not yet supported" rejection **for `messageQueueType: statestore` only**; broker types stay rejected until the egress phase lands.
The RFC-0024 dispatcher's `fireDestination` gains the topic arm it was reserved for: a statestore topic destination `Publish`es the result envelope directly (the dispatcher already holds a statestore client — no extra hop, no new credentials).
A topic destination is a **leaf**: it terminates the chain, so the `MaxChainDepth` cap does not apply to it (a function consuming the topic starts a fresh chain at depth 0, which is correct — it is a new invocation, not a continuation).

Durability contract, stated plainly: destinations are best-effort after settle (the RFC-0024 contract — a destination failure cannot un-settle the primary), and a statestore topic destination is **one local store write** in that window, the same reliability class as the existing function-destination enqueue; a failure is dropped with a counted metric, exactly like `enqueue_error`.
Broker destinations get the durable egress queue below not to be *more* reliable than the built-in type, but because a broker is a remote, independently-failing system that needs retry-across-outage; the store, if it is down, fails both destination kinds equally.
Hardening both destination kinds into a pre-settle durable write is a possible future strengthening, deliberately out of scope here.

### External-broker egress (the scale path)

For broker-type topic destinations the dispatcher must not speak to brokers: that would pull SDKs and credentials into the cluster-scoped router hot path.
Instead it enqueues a small egress job — `{topic, contentType, payload}` — onto a **per-broker-type** statestore `Queue`, `mq-egress-<mqType>`, and a publisher loop in that broker type's classic mqt head (where its connectivity and credentials already live) leases → `Publish`es → settles.
Per-type queues matter because `Queue.Lease` has no type filter: heterogeneous broker heads competing on one shared queue would lease each other's jobs and burn attempts; one queue per type keeps each head leasing only work it can publish.
This is a second consumer of the exact lease/settle/retry/backoff/DLQ machinery RFC-0024 built: the shared consumer core is extracted from `pkg/router/asyncinvoke` into a reusable package, and a broker outage simply retries per the queue budget, then dead-letters visibly (E4) — inspectable through the DLQ admin API and `fission function dlq` with a queue parameter (a small, additive extension of the surface landing in PR #3580, which this phase sequences after).

### Retention

A reaper in the statestore mqt head trims each `topic/*` stream to `min(cursor)` over the topic's **registered subscribers** (the `MessageQueueTrigger` CRs of type statestore on that topic), so no live subscriber ever loses an unconsumed event (E3).
Streams with no registered subscriber fall back to an age-based trim (default 24h, Helm-tunable) using the store-stamped `Event.At` — the documented, bounded loss for fire-and-forget topics, matching broker retention semantics.
E3 needs a backstop: a registered subscriber whose consumer is down indefinitely would pin `min(cursor)` and grow the stream without bound (the EventLog sits outside the KV quota machinery), so a hard ceiling — max age (default 7d) and max events per stream, both Helm-tunable — trims even subscribed streams, counted by a dedicated metric and documented as the operational hazard it is (identical in kind to broker retention evicting a lagging consumer group).

### Configuration and defaulting

- The provider needs no per-trigger connection config: it reuses the statestore wiring the mqt head gets exactly as the router does today (embedded → HTTP client driver → `svc/statestore`; external → Postgres driver → secret).
- Chart change the embedded mode requires: the statestore NetworkPolicy `from` allowlist currently admits only `{workflow, statesvc, router}` — the statestore mqt head's `svc:` label must be added, or its Read/cursor/Trim calls are silently dropped (the documented `dial tcp ... i/o timeout` CI bite).
- Render gate: the dormant dependent-feature gate in `statestore/validate.yaml` already fails the chart when a statestore consumer is enabled without `statestore.enabled`; eventing joins it (`eventing.enabled`, default **on** when `statestore.enabled` — the whole point is out-of-the-box).
- CLI: `fission mqtrigger create --mqtype statestore --topic orders --function consumer` (validator gains the type); `fission topic publish|peek` as thin dev conveniences over the admin surface (optional, last phase).
- `MqtKind`: the statestore type is `fission` (classic) kind; `keda` kind is rejected at validation until/unless the scaler lands (see Scaling).

### Scaling and limits

The classic mqt head hosts one goroutine loop per statestore trigger — ample for the provider's target envelope (small/medium eventing; the store's `SKIP LOCKED`/append throughput, not Fission, is the ceiling).
In external mode, a KEDA `postgresql`-scaler variant (lag = stream head − cursor, both readable by SQL) can scale consumers later; it is deliberately a follow-up because the embedded mode cannot be reached by KEDA scalers and the classic head already covers the bootstrap story.
Documented limits: roughly-FIFO (no partitions), no consumer groups beyond one-cursor-per-trigger, throughput bounded by the store — "when you outgrow this, change `messageQueueType` to kafka; nothing else changes."

### Observability

RFC-0019 meters, mirroring the async family:
`fission_eventing_published_total{provider,outcome}`, `fission_eventing_delivered_total{trigger,condition}`, `fission_eventing_retries_total`, `fission_eventing_errortopic_total`, `fission_eventing_lag{trigger}` (head − cursor, the scaling and alerting signal), `fission_eventing_trimmed_total`, plus the egress queue's existing queue-depth/DLQ metrics.
Every drop path (publish failure, MaxRetries → ErrorTopic, backstop trim, egress dead-letter) is countable — nothing is silently lost.

## Invariants & verification

- **E1 (durable publish)**: `Publish` returns success only after the event is durably appended (statestore) or broker-acked / durably enqueued for egress; never a fake accept.
- **E2 (at-least-once per subscriber)**: every event at or above a trigger's starting cursor is delivered to its function at least once; the cursor advances only after terminal handling (success or MaxRetries→ErrorTopic).
- **E3 (no premature trim)**: retention never trims above any registered subscriber's cursor; age-based trim applies only to subscriber-less streams and is documented loss.
- **E4 (egress honesty)**: a broker-destined publish retries per the queue budget and dead-letters visibly; the egress queue inherits the RFC-0021 conservation invariant (T1) unchanged.
- **E5 (poison isolation)**: a message that exhausts `MaxRetries` moves to `ErrorTopic` and the cursor advances — one event cannot stall a topic.

Verification: cross-driver conformance for the Phase-1 EventLog extension (`AppendAny` durability + concurrent-publisher safety, `Head` correctness incl. over the HTTP wire); memory-driver unit tests for the subscriber loop (scripted delivery endpoint, `testing/synctest` for retry/backoff timing); property test (rapid) that random interleavings of publish/consume/crash/redeliver never violate E2/E3 on the memory driver; integration (kind): destination→topic→trigger→function pipeline e2e, poison-pill→ErrorTopic, cursor survival across an mqt-head restart.
The cursor protocol is single-writer CAS and needs no new TLA+ model; the egress path reuses the already-checked `queue.tla` lease/settle protocol.

## Security

No new listeners and no new credentials: publishers and consumers use the existing HMAC-signed statestore capability client, delivery uses the existing `ServiceRouterInternal`-signed internal-listener path, and broker credentials remain confined to the mqtrigger subsystem (the egress design exists precisely to keep them there).
Topics are namespace-scoped by stream naming; a trigger can only consume topics in its own namespace (validated at admission, enforced by the consumer's namespace-scoped wiring).

## Alternatives considered

- **Embed a broker (NATS/Redpanda)**: instant full semantics, but Fission would ship and operate a stateful broker — rejected on the RFC-0021 principle, operational surface, and image weight.
- **Queue-only topics (fan-out at publish time)**: per-subscriber enqueue duplicates every payload N times, loses replay, and requires subscribers to pre-exist — rejected; the EventLog is exactly the right primitive and already shipped.
- **Redis Streams as the topic store**: capable, but a new external dependency — contradicts the bootstrap goal; Redis remains a possible future statestore *driver* underneath the same interfaces.
- **Publish to brokers directly from the router/dispatcher**: fewer hops, but broker SDKs + credentials in the router and no durability across broker outages — rejected except for the statestore type, where the dispatcher already holds the (credential-free, in-cluster) client.

## Backward compatibility

Purely additive: existing kafka/KEDA triggers are untouched; `MessageQueueType` gains the `statestore` value; `DestinationRef` topic validation relaxes only for that value; the mqtrigger CRD schema is unchanged (all existing fields are honored, none added in v1).
Clusters without `statestore.enabled` see no behavior change — the provider validates/fails closed exactly as async invocation does.

## Rollout phases (one PR each, bisectable — zero-dependency path first)

1. **EventLog extension + `TopicPublisher` + statestore destinations**: `AppendAny` + `Head` across the interface, all drivers, the HTTP wire, and conformance; extract `pkg/mqtrigger/mqpub` with the statestore implementation; stream naming + namespace scoping; RFC-0024 `fireDestination` topic arm for the statestore type; admission relaxation; metrics.
2. **`messageQueueType: statestore` provider**: the `mqt-fission-statestore` chart deployment; subscriber loop (KV cursor, MaxRetries→ErrorTopic, ResponseTopic), retention reaper + backstop, statestore NetworkPolicy allowlist entry, CLI validator + `--mqtype statestore`; e2e integration pipeline test.
3. **Broker egress** (sequences after PR #3580's DLQ surface): extract the shared queue-consumer core from `pkg/router/asyncinvoke`; per-type `mq-egress-<mqType>` queues + publisher loop in each broker head; kafka `TopicPublisher` extraction; un-reject broker topic destinations; DLQ admin/CLI queue parameter.
4. **Scale & polish**: KEDA postgresql-scaler variant for external mode (lag-based), `fission topic publish|peek` dev commands, eventing benchmark scenario (publish→consume latency, fan-out, lag under load).

## Verification / test plan

Unit (memory driver, synctest, rapid) per the invariants above; conformance additions only if the EventLog contract needs sharpening (head-CAS retry behavior); kind integration: the full A→topic→B pipeline, poison isolation, restart-survival, retention (subscriber-less age trim + min-cursor safety); benchmark scenario in phase 4 with distinct metric prefixes per the RFC-0020 conventions.

# RFC-0024: Async invocation — retries, dead-letter queue, and destinations

- Status: Proposed
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.N+1
- Requires: RFC-0021 statestore (`Queue` capability); composes with RFC-0022 (workflow steps inherit retry/DLQ semantics) and reuses `pkg/mqtrigger` publishers for topic destinations.

## Summary

Add Lambda-style asynchronous invocation as a platform primitive: a caller marks a request async, the router durably enqueues it and returns `202` with an invocation id, and a dispatcher delivers it at-least-once with per-function retry policy, max age, a dead-letter queue inspectable and redrivable from the CLI, and on-success/on-failure **destinations** (chain another function or publish to an MQ topic).
No broker required: the statestore Queue (Postgres `SKIP LOCKED` lease pattern) is the backbone; where a broker already exists, destinations can publish to it via the existing mqtrigger plumbing.

## Motivation

Today a caller either blocks on synchronous HTTP or stands up a full MessageQueueTrigger plus a Kafka/NATS deployment — there is no middle ground for "fire this function reliably and tell me how it went".
This is Lambda's single most-used reliability feature set (async invoke + retries + DLQ + event destinations), and its absence shows up as user-built outboxes, cron-retry hacks, and lost webhooks.
Fission already has every ingredient except the durable queue: the router owns admission, the internal listener owns delivery, mqtrigger owns broker publishing, and RFC-0021 supplies the queue with visibility-timeout leases and a dead-letter table.

## Goals

- `X-Fission-Invoke-Mode: async` on any HTTP-triggered function → durable `202 {invocationId}`.
- Per-function `InvocationConfig`: retry attempts/backoff, max event age, DLQ, onSuccess/onFailure destinations carrying a structured result envelope.
- At-least-once delivery that survives router crashes and redeploys; horizontal scale with router replicas.
- `fission fn dlq list|show|redrive|purge` with no broker dependency.
- Observability: queue depth/age/attempt metrics via RFC-0019 OTel meters.

## Non-goals

- Exactly-once delivery (callers get an invocation id + `DedupKey` support; consumers dedup).
- Response retrieval for async calls in v1 (destinations carry results; a "get invocation result" API is a later phase decision).
- Large payloads: bodies over 256KiB (Lambda-parity bound) are rejected with `413` in v1; blob spillover is explicitly deferred — the one plausible storagesvc use, and deliberately kept out.
- Ordering guarantees between invocations (use a workflow or an MQ trigger when order matters).
- Replacing MessageQueueTrigger for broker-sourced events.

## Design

### CRD surface

```go
// FunctionSpec gains:
Invocation *InvocationConfig `json:"invocation,omitempty"`

type InvocationConfig struct {
    Retry    RetryPolicy       `json:"retry,omitempty"`    // MaxAttempts (default 3), backoff base/cap + jitter
    MaxAge   *metav1.Duration  `json:"maxAge,omitempty"`   // default 6h; enqueueTime+MaxAge exceeded → DLQ, reason=expired
    DeadLetter *DeadLetterConfig `json:"deadLetter,omitempty"` // nil = keep in statestore DLQ table (default)
    OnSuccess *DestinationRef  `json:"onSuccess,omitempty"`
    OnFailure *DestinationRef  `json:"onFailure,omitempty"`
}

type DestinationRef struct {
    // Exactly one of:
    Function *FunctionReference `json:"function,omitempty"` // invoked async through the same machinery (depth-capped)
    Topic    *TopicRef          `json:"topic,omitempty"`    // published via the mqtrigger publisher for the configured broker
}
```

A function without `Invocation` still accepts async mode with platform defaults (3 attempts, 6h max age, statestore DLQ) — the header alone is enough to get the safety net, configuration tunes it.
Webhook validation: destination exclusivity, backoff bounds, a destination-chain depth annotation cap (default 3) to stop accidental infinite chains (`X-Fission-Invocation-Depth` propagated and enforced at enqueue).

### Enqueue path (router)

- Trigger: request header `X-Fission-Invoke-Mode: async`, or `httptrigger.spec.invocationMode: async` forcing it per trigger (webhooks from third parties cannot set headers).
- The trigger works on **both** the public HTTPTrigger path and the internal direct-function path (`/fission-function/<ns>/<name>`), so a signed direct caller — notably `fission function test --async` — can enqueue too (`functionHandler.asyncRequested`).
  The one request that must never re-enqueue is the dispatcher's own delivery, which carries `X-Fission-Invocation-Id`; that header gates it back to a synchronous proxy regardless of any other header (the replay allowlist already strips `X-Fission-*`, so this is belt-and-suspenders).
- The router handler (a thin branch where the proxy handoff happens today, after route/auth/admission resolution so async requests still respect trigger auth) serializes `{fnRef, method, path, headers-allowlist, body, enqueueTime, depth}` and calls `Queue.Enqueue("asyncinv", msg, {DedupKey: X-Fission-Dedup-Key})`, returning `202 {"invocationId": id}` or `503` if the statestore is unreachable (fail loud, never fake-accept).
- Body cap enforced before buffering completes (wrap with `http.MaxBytesReader`), so oversized requests cannot balloon router memory — the same concern class the #3539/#3541 spill work handled for uploads, solved here by rejection instead of spilling.
- Async on a non-existent function 404s at enqueue time (route resolution already happened).

### Dispatcher

- v1 placement: a goroutine pool inside the router process (`pkg/router/asyncdispatch`), one lease loop per replica: `Lease("asyncinv/<ns>", batch, leaseFor)` → deliver → settle.
  Because delivery POSTs to `svc/router-internal` and may land on a *different* router replica, the router's own `svc:` label must be added to the internal-listener NetworkPolicy `from` allowlist (`charts/fission-all/templates/router/networkpolicy.yaml`) — it is not there today (the allowlist covers kubewatcher/timer/mqtrigger/keda/canary/executor/buildermgr/mcp only), and omitting it is the documented silent-drop bite.
  Multiple router replicas lease from the same queue safely (`SKIP LOCKED`); crash recovery is automatic via lease expiry.
  Extraction into its own `fission-bundle` head is a mechanical later step if router coupling proves noisy — the package boundary is drawn for it (Options-only constructor, injected Queue + delivery client).
- Delivery: POST to the router internal listener at `utils.UrlForFunction(...)` with the `ServiceRouterInternal` HMAC signature — byte-identical to how timer/mqtrigger publish, so executor admission, EndpointSlice resolution, and the poolmgr accounting split all apply unchanged.
  Replayed headers carry `X-Fission-Invocation-Id`, `-Attempt`, `-Depth`.
- Settle: 2xx → `Ack`, then fire `OnSuccess`; 4xx (except 408/429) → **no retry** (permanent), `Kill(reason=http_4xx)` + `OnFailure`; 5xx/timeout/dial error → `Nack(retryAfter=backoff(attempt))` until `MaxAttempts` or `MaxAge`, then `Kill` + `OnFailure`.
  The lease duration exceeds the function timeout so a slow-but-alive delivery is not double-sent.
- Destination envelope (Lambda-shaped):

```json
{
  "version": "1.0",
  "requestContext": {"invocationId": "...", "functionRef": "...", "condition": "Success|RetriesExhausted|EventAgeExceeded|Http4xx", "attempts": 3},
  "requestPayload": { "...original body if ≤64KiB, else omitted..." },
  "responseContext": {"statusCode": 502},
  "responsePayload": { "...truncated at 64KiB..." }
}
```

Function destinations are themselves enqueued async (depth+1); topic destinations go through the existing `publisher.MakeWebhookPublisher`-family mqtrigger publishers (deterministic constructors, no env reads — the established contract).

### CLI

- **Invoke:** `fission function test --async` invokes asynchronously — it POSTs to the router internal listener with the async header, HMAC-signing the request (`ServiceRouterInternal`, from `FISSION_INTERNAL_AUTH_SECRET`) so the internal verifier accepts it, and prints the invocation id from the `202` instead of awaiting a response.
- **Configure:** `fission function create|update` sets `FunctionSpec.Invocation` via `--async-retry-max-attempts`, `--async-max-age`, `--async-on-success <fn>`, `--async-on-failure <fn>` (update merges onto the existing config; an empty destination clears it).
- **Trigger mode:** `fission route create|update --invocation-mode async` sets `httptrigger.spec.invocationMode`, forcing async for every request through that trigger (for callers that cannot set the header).
- **DLQ:** default DLQ is the statestore dead-letter table (`Queue.DeadLetters`/`Redrive`/`Purge`), so the feature is complete with zero brokers.
  `fission function dlq list [--namespace <ns>] [--limit N]` (id, namespace, function, reason, attempts, died), `show --id <id>` (full envelope), `redrive --id <id>|--all` (re-enqueue with attempts reset), `purge`.
  Served by the router admin endpoints `/v1/async/dlq/{list,show,redrive,purge}` on the public listener, gated by the same JWT `authMiddleware` as the login endpoint (operator JWT when `authentication.enabled`; the paths are deliberately not in the auth exemption list) — a coarse gate, with per-namespace JWT scoping a follow-up.

### Observability

`fission_async_queue_depth`, `_oldest_age_seconds`, `_deliveries_total{condition}`, `_retries_total`, `_dlq_total{reason}`, `_destinations_total{outcome}`, `_depth_cap_total` via RFC-0019 meters.
Queue depth is the KEDA scaling hook: an opt-in `router.keda.enabled` ScaledObject scales the router Deployment on the visible backlog via the `postgresql` scaler (requires `statestore.mode=external`, mutually exclusive with `router.autoscaling`).
Invocation id joins the RFC-0015 correlation story (one id from 202 through delivery attempts to destination).

## Invariants & verification

**Invariants.**

- A1 *(no fake accept)*: a `202` is returned only after the message is durably enqueued; statestore unavailability yields `503`, never a silently dropped `202`.
- A2 *(settled exactly once)*: every accepted invocation reaches exactly one terminal settle — acked, or dead-lettered with a reason.
- A3 *(current lease decides)*: a stale dispatcher (lease expired, still working) can never ack, requeue, or dead-letter an invocation a newer lease owns.
- A4 *(honest DLQ)*: a dead-lettered invocation carries the true reason — retries exhausted, MaxAge exceeded, or permanent 4xx — and reached the corresponding condition.
- A5 *(conservation)*: enqueued = in-flight + acked + dead at all times.
- A6 *(bounded chains)*: destination depth never exceeds the cap.
- A7 *(lease covers work)*: lease duration strictly exceeds the function timeout — see below for why this is an invariant and not a tuning suggestion.

**Design-time model checking.** The dispatcher's lease/settle protocol is [`specs/queue.tla`](specs/queue.tla) (shared with RFC-0021): A2/A3/A4 are its I1/I2/I3, TLC-verified, with the negative model demonstrating A3's collapse without the lease-epoch settle guard.
Model checking also surfaced the semantics behind A7: a delivery that *succeeds* slower than its lease has its ack correctly rejected as stale, and the retry can legitimately dead-letter work that already succeeded once.
That is unavoidable in any at-least-once visibility-timeout queue (SQS behaves identically) — so the webhook enforces lease > function timeout at admission, delivery handlers must be idempotent (documented, with the `X-Fission-Invocation-Id` dedup key), and DLQ redrive must tolerate already-succeeded work.

**Implementation-time verification.**

- Settle matrix as properties: `pgregory.net/rapid` generates response sequences (2xx/408/429/4xx/5xx/timeout/dial-error) and asserts the settle decision table (ack / nack-with-backoff / kill) plus backoff jitter bounds and monotone attempt counts.
- Dispatcher timing — backoff schedules, MaxAge expiry, lease renewal-vs-expiry races — runs in `testing/synctest` bubbles against the memory Queue: a "6-hour MaxAge" test is instant and deterministic, no clock seam, no sleeps.
- Enqueue body handling uses `testing/iotest` readers (`ErrReader`, `TimeoutReader`, `OneByteReader`, `HalfReader`) composed with `http.MaxBytesReader` to cover the cap and mid-body failure edges (A1: a failed read must produce an error response, never a partial enqueue).
- Envelope encode/decode and the depth-cap header parser get `go test -fuzz` targets (round-trip + never-panic).
- Crash recovery: integration test kills the router mid-lease and asserts redelivery (A2 via lease expiry) — the serial suite owns it.

**Runtime gates.** A5 is exported as a conservation-drift metric with a CI bar of zero (same pattern as the route-resync drift gate); A6 violations are a counter that must stay zero outside the dedicated abuse test.

## Alternatives considered

- **Require an MQ broker (build on mqtrigger/KEDA)** — punts the durable core to an optional dependency; the whole point is reliability-by-default on a stock install.
  Brokers stay first-class for high-throughput sourcing; destinations bridge to them.
- **Kubernetes Jobs per invocation** — pod-per-invocation latency and apiserver churn at data-plane rates; absurd for 50ms functions.
- **In-router in-memory queue with bounded retries** — loses everything on restart; "async but sometimes vanished" is worse than nothing.
- **Spill large bodies to storagesvc** — explicitly rejected (maintainer direction; storagesvc fragility), 413 instead; revisit against OCI/blob substrate if real demand shows.
- **Separate dispatcher deployment from day one** — one more pod and NetworkPolicy row before load proves the need; the in-router pool with a clean package boundary defers it without lock-in (the extraction risk is contained by the Options-only constructor seam).

## Backward compatibility

Additive: no behavior change without the header/trigger field; sync invocation path untouched (the async branch is after resolution, before proxy).
Render-gated on `statestore.enabled` (`asyncInvocation.enabled` Helm flag, off by default).

## Rollout phases (one PR each, bisectable)

1. `InvocationConfig` CRD field + codegen + webhook validation; enqueue path in the router (202 + id, caps, dedup); dispatcher with retry/backoff and statestore DLQ; defaults-only (no destinations); metrics.
2. Destinations (function-only; topic declared but admission-rejected) with the result envelope and depth cap; `httptrigger.invocationMode` CEL enum.
3. `Queue.Purge`; router DLQ admin endpoints `/v1/async/dlq/{list,show,redrive,purge}`; CLI `fission function dlq` suite.
4. Opt-in KEDA `postgresql` ScaledObject scaling the router on queue depth; RFC-0020 async bench scenario (enqueue overhead, drain throughput, DLQ under saturation).
5. CLI/test integration: direct-path async in the router (`functionHandler.asyncRequested`), `fission function test --async`, `fn create|update --async-*` config flags, `route --invocation-mode`.

## Verification / test plan

- Unit: settle matrix (2xx/4xx/429/5xx/timeout → ack/kill/nack), backoff jitter bounds, depth-cap enforcement, MaxAge expiry to DLQ, dedup-key collapse — all against the memory Queue.
- Integration: async invoke → 202 → eventual execution (poll a side-effect function); kill the router mid-lease and assert redelivery (serial suite, rollout helpers); destination chain fn→fn→topic; DLQ redrive round-trip via CLI.
- Failure honesty: statestore down → 503 on enqueue (never 202), covered by an integration case with the statestore Service scaled to zero.
- NetworkPolicy drift test: assert the `svc: router` row is present in the internal-listener allowlist when `asyncInvocation.enabled` (phase-1 chart change), and a second row when the dispatcher extracts to its own pod.

## Open questions

- Result retrieval API (`GET /v1/invocations/<id>`) in v1 or defer entirely to destinations (leaning: defer; it needs result storage decisions that destinations avoid).
- Whether `httptrigger.invocationMode: async` should also support `dual` (client chooses via header, trigger sets the ceiling).
- Per-namespace enqueue rate limits (protect the shared queue from one noisy tenant) — v1 constant default vs. tenant-CRD knob once multi-namespace tenancy lands.

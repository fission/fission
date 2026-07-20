# RFC protocol specs (TLA+)

Machine-checked models of the genuinely concurrent protocols in the RFC-0021…0027 feature set.
The specs are the design source of truth for these protocols: change the protocol → change the spec → TLC must pass before implementation follows.

| Spec | Models | For |
|------|--------|-----|
| `queue.tla` | Queue lease/settle lifecycle: visibility timeout, re-lease, epoch-guarded ack/nack/kill | RFC-0021 `Queue`, RFC-0024 dispatcher |
| `workflowfold.tla` | CAS-append event-log fold: racing reconcilers, crash/replan, retries, cancel, terminal stability | RFC-0021 `EventLog`, RFC-0022 engine |
| `workflowbranch.tla` | Parallel-region protocol: concurrent branches over one CAS log, join discipline (unique, all-branches-ok), fail-fast, nothing-after-join | RFC-0022 phase 3 Parallel/Map |
| `eventlogsub.tla` | Topic subscription: AppendAny publishers, overlapping consumers with a version-CAS KV cursor, poison→ErrorTopic, min-cursor retention | RFC-0027 statestore MQ provider |
| `quota.tla` | Keyspace quota under concurrent writers: check-then-act vs atomic reserve+commit against a `MaxKeys` budget | RFC-0023 `statesvc` scoped KV quota (invariant S3) |
| `aliasgc.tla` | Retention-GC vs alias-create race: two-phase sweep, delete-time alias re-check | RFC-0025 versions/aliases GC (invariants V2/V3) |

## RFC-0024 async dispatcher

The dispatcher's settle matrix maps onto `queue.tla` directly: a 2xx delivery is `Ack`, a permanent 4xx is `Kill`, and a 5xx / timeout / transport error is `Nack` (which the model dead-letters once the attempt budget is spent).
Its invariants A2/A3/A4 are the model's I1/I2/I3 — settled-exactly-once, current-lease-decides (the epoch guard), and honest dead-letter — so the dispatcher inherits them rather than re-proving them.
MaxAge expiry is an immediate `Kill`, which the model already covers; the age clock itself is not simulated (it is checked in the `testing/synctest` layer instead, per the simplifications below).

## Running TLC

Requires Java 11+ and `tla2tools.jar` from the official release (https://github.com/tlaplus/tlaplus/releases).

```sh
java -jar tla2tools.jar -deadlock -config queue.cfg queue.tla
java -jar tla2tools.jar -deadlock -config workflowfold.cfg workflowfold.tla
java -jar tla2tools.jar -deadlock -config eventlogsub.cfg eventlogsub.tla
```

`hack/run-tlc.sh` (the CI `tlc` job) runs all of the above plus the negative models.
`-deadlock` disables deadlock reporting — the models intentionally quiesce (all messages settled / run terminal) rather than loop forever.

## The negative models

```sh
java -jar tla2tools.jar -deadlock -config queue-unguarded.cfg queue.tla
java -jar tla2tools.jar -deadlock -config eventlogsub-blindwrite.cfg eventlogsub.tla
java -jar tla2tools.jar -deadlock -config quota-nonatomic.cfg quota.tla
java -jar tla2tools.jar -deadlock -config aliasgc-norecheck.cfg aliasgc.tla
```

are **expected to fail** — each documents why a guard exists:

- `queue-unguarded.cfg` checks the queue with `EpochGuard = FALSE` (settles keyed by message id alone, no lease-epoch check) and TLC produces the counterexample trace — a slow dispatcher's stale Kill dead-letters a message whose newer delivery already succeeded (`NoSuccessfulDeadLetter` violated).
  This is the documented reason the Postgres driver's settle statements must be guarded on the epoch column (`... WHERE id = $1 AND epoch = $2`), not the id alone.
- `eventlogsub-blindwrite.cfg` checks the topic subscription with `CasGuard = FALSE` (cursor commits as blind last-writer-wins Sets) and TLC finds the leadership-overlap trace — a consumer holding an older cursor view commits its smaller progress over a newer commit and regresses the cursor (`CursorMonotonic` violated).
  This is the documented reason RFC-0027's cursor writes must be KV version-CAS (`SetOptions.IfVersion`), not blind `Set`s.
- `quota-nonatomic.cfg` checks the keyspace quota with `AtomicQuota = FALSE` (the budget check and the counter increment are separate steps) and TLC finds the check-then-act trace — two writers both observe `count < MaxKeys`, both pass the check, and both commit past the budget (`QuotaNeverExceeded` violated).
  This is the documented reason RFC-0023's `statesvc` must enforce `MaxKeys` / the namespace byte budget with an atomic counter operation (a KV CAS or a counted transaction), not a plain read-check-then-write.
- `aliasgc-norecheck.cfg` checks the alias/GC race with `RecheckGuard = FALSE` (retention GC deletes on the snapshot its sweep took) and TLC finds the interleaving where an alias-create commits between GC's scan and its delete, stranding the alias on a deleted version (`NoDanglingAlias` violated).
  This is the documented reason RFC-0025's retention GC must re-check alias references inside the delete step (or gate delete on an alias-held finalizer/ownerRef), not act on the start-of-sweep snapshot.

## Scope and honesty

- The specs check **safety** on small bounds (1–2 messages, 2 dispatchers/reconcilers, 2 steps, 2 attempts, short clocks); protocol bugs of this shape almost always show up at tiny sizes.
- Liveness (every message eventually settles, every run eventually terminates) is deliberately left to the deterministic-simulation and `testing/synctest` layers described in each RFC's "Invariants & verification" section — bounded-clock TLC liveness adds noise for little insight here.
- The models simplify: single queue, linear workflows (no parallel/map), no MaxAge expiry, integer backoff of one tick; `eventlogsub` collapses a poison event's retry loop into its terminal ErrorTopic handling, models one subscription (per-subscription cursors are independent; min-cursor across subscriptions only tightens the trim bound), and leaves the age/size retention backstop out of the green model (it is documented loss by design).
  Extend the model first when implementing the features that break these simplifications (e.g. parallel branches in RFC-0022 phase 2).

## CI

Phase-1 PRs of RFC-0021/0022 add a `tlc` job (pinned tla2tools version, both green configs plus an assertion that the unguarded config fails) so the specs cannot rot silently.

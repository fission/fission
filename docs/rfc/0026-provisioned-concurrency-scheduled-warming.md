# RFC-0026: Provisioned concurrency and scheduled warming

- Status: Proposed
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.N (smallest of the set; no statestore dependency)
- Requires: RFC-0002 EndpointSlice data plane (shipped, default-on) for instant router visibility of pre-specialized pods; reuses `robfig/cron` parsing conventions from `pkg/timer`.

## Summary

Let a function declare a floor of always-warm capacity: `FunctionSpec.ProvisionedConcurrency` keeps N pods **pre-specialized** (package loaded, ready to serve) at all times, with optional cron-scheduled windows that raise or lower the floor for known traffic patterns (business hours, batch windows, product launches).
The executor specializes eagerly from the generic pool instead of waiting for the first request, exempts these pods from the idle reaper, and publishes them through the existing `fission.io/served` label + headless-Service path, so the router's EndpointSlice index sees them as instantly hittable.
This is Lambda provisioned concurrency, and the cheapest, broadest lever in this RFC set: cold starts are the #1 FaaS complaint (the RFD-2026-06 ranking lens) and this removes them entirely for opted-in functions.

## Motivation

Poolmgr's warm pool is *generic*: pods idle un-specialized, and the first request per pod still pays package fetch + load (~100ms best case, seconds for heavy packages), then the idle reaper un-warms quiet functions so off-hours or bursty traffic pays it again.
The recent perf waves squeezed the specialization path itself (async svc-host patch, readyPod debounce, adaptive waits); what remains is *when* specialization happens, and that is a policy question the user can answer better than any heuristic: "my checkout function must never cold start between 9 and 21".
Every ingredient exists: the specialization machinery, the served-label publishing (RFC-0002 `gp_service.go`), the reaper, and cron parsing in the timer subsystem — this RFC only adds the reconcile loop that connects them to a declared target.

## Goals

- Per-function warm floor: `Target` pre-specialized, reaper-exempt pods, continuously reconciled against pod churn and node loss.
- Cron-scheduled windows overriding the base target for a duration.
- Zero cold starts for requests within the provisioned floor; overflow beyond the floor behaves exactly as today (on-demand specialization).
- Honest status: the Function reports how many provisioned pods are actually ready.

## Non-goals

- Predictive/metrics-driven autowarming (explicitly out; a later RFC can feed the same target from router RPS metrics).
- Provisioned concurrency for newdeploy/container types in v1 — newdeploy already owns `MinScale` on its Deployment; this targets poolmgr, where the gap is.
- Request-level reservation/queuing semantics (the floor is capacity, not admission control; poolmgr concurrency accounting is untouched).
- SnapStart-style checkpoint/restore (interesting, orthogonal, much deeper).

## Design

### CRD surface

```go
// FunctionSpec gains (poolmgr executor type only; webhook rejects others in v1):
ProvisionedConcurrency *ProvisionedConcurrencyConfig `json:"provisionedConcurrency,omitempty"`

type ProvisionedConcurrencyConfig struct {
    Target    int                  `json:"target"`              // base floor, ≥1
    Schedules []ProvisionedWindow  `json:"schedules,omitempty"` // highest matching window wins
}

type ProvisionedWindow struct {
    Cron     string          `json:"cron"`     // robfig/cron spec, validated in webhook like TimeTrigger's
    Duration metav1.Duration `json:"duration"` // window length from each cron fire
    Target   int             `json:"target"`   // may be 0 (explicitly un-warm off-hours)
}
```

Webhook validation mirrors the TimeTrigger cron validation (server-side, covering raw kubectl/GitOps writes — the same gap that bit timers before the reconciler-side validation landed); `Target` bounded by a namespace-level cap (`provisionedConcurrency.maxPerFunction`, Helm default 20) so one function cannot silently reserve a cluster.

### Executor reconcile loop (poolmgr)

A `provisioner` component inside `pkg/executor/executortype/poolmgr`, one desired-state entry per opted-in function:

1. **Effective target**: base `Target`, overridden while inside any schedule window (evaluate cron fires + duration; multiple overlapping windows → max).
   Cron evaluation reuses the `robfig/cron` parser already vendored for the timer; the provisioner keeps next-transition timers in memory and re-derives them on restart from the specs (no persistence needed — desired state is pure function of spec + wall clock).
2. **Reconcile**: count ready specialized pods for the function (the pool controller's existing view); if below target, specialize `delta` generic pods eagerly — the same specialize call the cold-start path uses, so fetch/load behavior and failure handling are byte-identical; if above (window closed), strip the provisioned annotation from the excess and let the normal idle reaper retire them gracefully (never hard-kill serving pods).
3. **Warm-up pacing**: eager specializations go through the shared specialization path with a pacing throttle (batch + the existing 10ms readyPod debounce), and must not saturate `EXECUTOR_SPECIALIZATION_CONCURRENCY` — the known head-of-line hazard: a launch-window warm-up of 20 pods must not starve on-demand specializations for other functions.
   The provisioner therefore self-limits (default: max 4 in-flight eager specializations per function, tunable) rather than relying on the global semaphore staying unbounded.
4. **Publishing**: pre-specialized pods get the same `fission.io/served` + `fission.io/function-generation` labels and join the function's headless selector Service (`gp_service.go`), so the router's EndpointSlice index picks them up with no router changes at all.
5. **Reaper exemption**: provisioned pods carry `fission.io/provisioned: "true"`; the idle reaper skips them while the annotation holds and the function's spec still wants them (spec deletion or target drop clears the annotation first — no orphaned immortal pods).
   The wave-3 strike-based quarantine still applies to provisioned pods (a sick pod is a sick pod) — but note it is **router-replica-local soft state with a short TTL**, invisible to the executor, so a router-quarantined pod still counts toward the provisioner's ready count and self-heals via the TTL rather than being replaced; the provisioner only replaces pods that are unhealthy in the executor's own view (not ready, failed probes, deleted).

### Interaction with updates and versions

A runtime-affecting function update invalidates old-generation provisioned pods; the provisioner treats generation mismatch as shortfall and re-specializes on the new generation before the old pods drain (make-before-break, bounded by the pacing limit), which also gives RFC-0025 warm rollbacks a knob: an aliased previous version with `Target: 1` stays rollback-warm by policy.

### Status and observability

- `FunctionStatus` (or a condition) gains `provisionedReady/provisionedTarget`; `fission fn get` shows it.
- Metrics via RFC-0019 meters: `fission_provisioned_target`, `_ready`, `_eager_specializations_total{outcome}`, `_window_transitions_total`.
  The RFC-0020 bench gains a scenario asserting p99 ≈ warm latency for traffic ≤ floor after an idle period (the exact case that cold-starts today).

### Costs, made explicit

Provisioned pods hold memory/CPU requests continuously — that is the point, and the trade is the user's to make; the namespace cap plus NOTES.txt guidance keep it deliberate.
The generic pool must be sized to absorb eager draws (`poolsize` guidance in docs); the provisioner backs off (with a status condition, not silent retry) when the pool cannot supply generic pods.

## Invariants & verification

**Invariants.**

- P1 *(pure target)*: the effective target is a pure function of (spec, wall clock) — no hidden state, so any replica or a restarted executor computes the same answer.
- P2 *(convergence)*: with a stable spec and healthy pool, ready provisioned pods reach the target within a bounded number of reconcile rounds and stay there.
- P3 *(pacing bound)*: never more than the per-function in-flight limit of eager specializations, regardless of target size or window transitions.
- P4 *(no immortal pods)*: every reaper-exemption annotation is cleared when the spec no longer wants the pod (target drop, window close, spec delete) — an orphaned exempt pod is a bug, not a leak to tolerate.
- P5 *(no starvation)*: one function's warm-up burst never regresses another function's on-demand cold-start latency beyond the agreed bound.

**Verification.** No model checking — the reconcile loop is single-owner (the executor) with no cross-writer protocol; the risk is time arithmetic and pacing, which property tests and virtual time cover better.

- P1: `pgregory.net/rapid` properties over generated schedule sets — overlapping windows take the max, `Target: 0` windows un-warm, evaluation is total for every instant (no gaps at window edges), and golden cases for DST transitions (the classic cron trap).
- P2/P3/P4: the reconcile loop runs against a fake pool inside `testing/synctest` bubbles (Go 1.26) — the virtual clock drives cron fires and reaper timers deterministically, so "window opens at 09:00, 20 pods warm at ≤4 in flight, window closes, reaper retires them" is one instant, sleep-free test; `robfig/cron` timers virtualize inside the bubble because they use the standard `time` package.
- P4 additionally gets a lifecycle table test: every path that stops wanting a pod (delete, target drop, generation bump, quarantine replacement) asserts the annotation is cleared before retirement.
- P5 is a CI guardrail in the RFC-0020 bench: warm 20 pods for function A under the c500-style saturation harness while measuring function B's cold-start p95 against baseline.
- Integration (`suites/common` + serial): floor established with zero requests; floor survives reaper timeout and pod kill; floor re-established after executor restart (adopt-pass contract); overflow beyond the floor pays only normal cold starts.

## Alternatives considered

- **Ping-based keep-warm (cron TimeTrigger hitting the function)** — today's folk remedy: keeps exactly one pod warm per ping stream, races the reaper, generates junk invocations in logs/metrics, and cannot express N>1; formalizing the real primitive removes the hack.
- **Reaper tuning (`idletimeout` per function)** — prevents un-warming but never pre-warms (first request still cold) and cannot express schedules.
- **newdeploy with `MinScale` as the answer** — real for steady high-QPS services, but loses poolmgr's pool economics and forces a per-function Deployment; poolmgr users asked to switch executors is a non-answer.
- **Predictive autowarming first** — strictly harder (needs history, risks oscillation) and still wants this RFC's actuator underneath; declarative floor first, prediction as a later producer of the same target.
- **HPA on custom metrics** — HPA scales Deployments, not specialized-pod counts inside a shared generic pool; wrong actuator for poolmgr.

## Backward compatibility

Additive: nil config = today's behavior exactly; no router or data-plane changes (the EndpointSlice index simply sees more ready endpoints).
Works with gates `off` (legacy data plane) too — pre-specialized pods serve via the executor-RPC path just as warm pods do today — but the instant-visibility benefit is best on the default plane.

## Rollout phases (one PR each, bisectable)

1. CRD field + codegen + webhook validation (cron, caps, poolmgr-only); provisioner with base `Target` (no schedules): eager specialize, reaper exemption, status, metrics.
2. Scheduled windows (cron transitions, overlapping-window max, restart re-derivation).
3. Generation make-before-break on updates; RFC-0025 warm-rollback integration once that RFC lands.
4. Bench scenario + docs (sizing the generic pool, cost guidance).

## Verification / test plan

- Unit: effective-target evaluation table (base, single window, overlap, `Target: 0` window, DST-boundary crons), pacing limiter, exemption-annotation lifecycle.
- Integration (`suites/common`): set `Target: 2` → assert 2 specialized ready pods with zero requests sent; idle past reaper timeout → still 2; kill one pod → reconciled back; drop target → pods retired by reaper (poll with the framework's pod-label conventions per the integration-quirks doc).
- Serial suite: executor restart → provisioned floor re-established after adopt pass (`AdoptExistingResources` contract: rollout-complete ⟹ adopt ran).
- Perf: RFC-0020 scenario — idle 10 minutes, then burst ≤ floor: p99 must match warm baseline (no cold-start signature); burst > floor: overflow pays normal cold start only for the excess.
- Guardrail: CI assertion that eager warm-up of one function does not regress another function's cold-start p95 (the head-of-line scenario, run under the c500-style saturation harness).

## Open questions

- Whether `Target` counts *pods* (as drafted, simplest) or *concurrent requests* (Lambda counts concurrency; poolmgr pods ≈ concurrency slots only when `Concurrency=1`) — needs a decision against poolmgr's per-pod concurrency semantics before phase 1.
- Namespace cap enforcement point: webhook constant vs. tenant-CRD knob once multi-namespace tenancy lands (same question as RFC-0024's rate limits; keep the answers aligned).
- Whether a `fission fn warm --name x --target n --for 2h` imperative CLI (ephemeral window) is worth shipping in phase 2 for incident/live-event use.

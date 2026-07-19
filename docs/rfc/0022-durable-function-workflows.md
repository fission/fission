# RFC-0022: Durable function workflows

- Status: Proposed (revised 2026-07-16, pre-implementation: design review against the shipped RFC-0021/0024/0027 implementations — spec-snapshot-in-stream, error model, worker-pool invocation, fold checkpoints, UX surface)
- Tracking issue: TBD
- Supersedes: the abandoned pre-2020 `fission-workflows` project (external repo, NATS-backed, unmaintained)
- Targets: Fission v1.N+1
- Requires: RFC-0021 statestore (`EventLog` + `Queue` + `KVStore` capabilities — KV holds oversized step I/O), implemented ([#3574](https://github.com/fission/fission/pull/3574)).
  Composes with RFC-0024 async invocation and RFC-0027 eventing but requires neither: a task step may optionally run as an async invocation whose on-success/on-failure statestore topic the engine subscribes to (DLQ/retry parity for that step), and steps can publish to topics; a "start a run on a topic event" adapter is a phase-5 item.
  The default task path stays a direct signed invocation — the async→topic→subscription round trip costs 100–200ms of control-plane hops per step, incompatible with the <15ms per-step overhead target below.

## Summary

Add a first-class orchestration layer: a `Workflow` CRD describing a state machine whose task states are Fission functions, and a `WorkflowRun` CRD representing one execution.
A new `pkg/workflow` controller (a `fission-bundle` head) executes runs by invoking functions on the router internal listener, persisting every step transition to the statestore `EventLog` so a controller restart resumes executions instead of losing them.
This is the AWS Step Functions / Azure Durable Functions analog, rebuilt Kubernetes-native and small: no separate event-store service, no always-on broker, no bespoke DSL runtime in function pods.

## Motivation

Chaining functions today means hand-writing HTTP calls inside function code: no declarative retries, no branching, no fan-out, no execution history, no way to answer "where did order 4711's pipeline stop".
Every serious FaaS ships an orchestrator because multi-step business processes are the natural next request after single functions; users who hit this wall on Fission currently bolt on Temporal or Argo Workflows, both of which treat Fission as opaque HTTP and duplicate its auth/routing.
Fission already owns the invocation path (HMAC-gated internal listener), CRD reconciler patterns across seven subsystems (post RFC-0005 WS3), and — with RFC-0021 — a durable event log.
The old `fission-workflows` failed on operational weight (its own NATS-STAN event store, its own gRPC API server, an interpreter baked into environments); this design keeps exactly one new pod and zero new protocols.

## Goals

- Declarative sequencing, branching, parallel fan-out/fan-in, per-step retry/backoff, and error routing across existing functions, without touching function code.
- Durable, resumable executions: controller restart or node loss never loses or forks a run.
- Full step-level history queryable from the CLI (`fission workflow runs history --name <run>`).
- At-least-once step semantics with documented idempotency expectations (identical to Step Functions' contract).

## Non-goals

- A Turing-complete DSL or embedded scripting; states are data, logic lives in functions.
- Exactly-once step execution.
- Long "wait for external callback" states in v1 (a phase-5 decision).
- Cross-cluster or cross-namespace workflows (a workflow and its functions share a namespace in v1).
- A visual designer/UI (phase 4 exposes the data; UI is out of scope here).

## Design

### CRDs

Both types follow the 10-step new-CRD checklist in `pkg/apis/core/v1/types.go` (spec → type → list → register → CRUD interface → `make codegen` + `make generate-crds`), with validation in `pkg/apis/core/v1/validation.go` and the admission webhook.

```go
type WorkflowSpec struct {
    StartAt string                   `json:"startAt"`
    States  map[string]WorkflowState `json:"states"`
    // DefaultRetry applies to task states that do not override it.
    DefaultRetry *RetryPolicy `json:"defaultRetry,omitempty"`
    // Timeout bounds a whole run; expiry fails it with errorType
    // Fission.Timeout (a mis-authored graph or endlessly caught-and-retried
    // loop must not hold an active run forever). Default 24h.
    Timeout *metav1.Duration `json:"timeout,omitempty"`
    // HistoryRetention bounds stored history (count + age) per run.
    HistoryRetention *RetentionPolicy `json:"historyRetention,omitempty"`
}

type WorkflowState struct {
    Type StateType `json:"type"` // Task | Choice | Parallel | Map | Wait | Succeed | Fail
    // Task
    Function   *FunctionReference `json:"function,omitempty"` // reuses the existing FunctionReference type
    Timeout    *metav1.Duration   `json:"timeout,omitempty"`
    Retry      *RetryPolicy       `json:"retry,omitempty"`   // maxAttempts, backoff base/cap, jitter
    Catch      []CatchRoute       `json:"catch,omitempty"`   // on matched error class → next state
    // Choice
    Choices    []ChoiceRule       `json:"choices,omitempty"`
    Default    string             `json:"default,omitempty"`
    // Parallel / Map
    Branches   []WorkflowBranch   `json:"branches,omitempty"`
    ItemsPath  string             `json:"itemsPath,omitempty"`
    // MaxConcurrency throttles Map fan-out. Defaults to 10, NOT unbounded: an
    // unthrottled 1000-item Map against poolmgr is a self-inflicted cold-start
    // burst (the specialization head-of-line class); raising it is a
    // deliberate act, and the engine's invocation worker pool is the global
    // ceiling regardless.
    MaxConcurrency int            `json:"maxConcurrency,omitempty"`
    // Wait
    Duration   *metav1.Duration   `json:"duration,omitempty"`
    // shared
    InputPath, ResultPath, OutputPath string // JSONPath shaping, Step Functions semantics
    Next  string `json:"next,omitempty"`
    End   bool   `json:"end,omitempty"`
}

type WorkflowRunSpec struct {
    WorkflowRef string               `json:"workflowRef"`
    // WorkflowGeneration records (for observability) which Workflow generation
    // this run executes. It is NOT the pinning mechanism: Kubernetes does not
    // retain old generations of a CR, so an edited Workflow's prior spec is
    // unrecoverable from etcd. The AUTHORITATIVE spec is the snapshot the
    // engine embeds in the run's own event stream at RunStarted (see
    // Execution engine) — a run is self-contained, replay is deterministic,
    // and a Workflow edit or even deletion mid-run can neither fork nor
    // strand it.
    WorkflowGeneration int64         `json:"workflowGeneration,omitempty"` // set by webhook if empty
    // Input is webhook-capped at 256KiB (Step Functions parity; etcd objects
    // cap at ~1.5MiB) — larger inputs are passed by reference.
    Input   *runtime.RawExtension    `json:"input,omitempty"`
}

type WorkflowRunStatus struct {
    Phase        RunPhase      `json:"phase"` // Pending|Running|Succeeded|Failed|Cancelled|TimedOut
    // Conditions follow the house metav1.Condition convention (like the
    // MessageQueueTrigger BindingReady pattern): notably an Accepted/Running
    // condition a run STUCK in Pending can be alerted on — CRDs install
    // regardless of workflows.enabled, so a run created with the head disabled
    // must say "no workflow controller is running" on its status rather than
    // sit silent forever (the admission-accepts-what-runtime-can't-service
    // lesson from RFC-0027's consumer-less egress queue).
    Conditions   []metav1.Condition `json:"conditions,omitempty"`
    ActiveStates []string      `json:"activeStates,omitempty"`
    StartedAt, FinishedAt *metav1.Time
    // Output holds the final output inline up to the step-I/O spill threshold;
    // larger outputs spill to the same KV keyspace as step I/O and OutputRef
    // points there (the CLI dereferences) — a big final result must not turn
    // the terminal status write into the failure.
    Output    *runtime.RawExtension `json:"output,omitempty"`
    OutputRef string                `json:"outputRef,omitempty"`
    // RecentEvents is a bounded (≤20) tail for kubectl visibility; the full
    // history lives in the statestore EventLog, never in etcd.
    RecentEvents []RunEventSummary `json:"recentEvents,omitempty"`
}
```

Both CRDs ship `additionalPrinterColumns` from phase 1 (`kubectl get workflowruns` shows WORKFLOW, PHASE, STARTED, DURATION) — the zero-cost half of debugging UX.

### Expression grammar (pinned, because the webhook freezes it)

- **JSONPath dialect**: one library, one documented dialect — `ojg/jp` proposed (final call at phase 1, but the RFC-level requirement is a single pinned dialect; Go JSONPath libraries diverge on filters, unions, and no-match behavior, and "whatever the library does" is not a contract).
  No-match semantics are explicit: a no-match on `InputPath`/`OutputPath` yields JSON `null`; a no-match on a `ResultPath` write is a step error (`Fission.InvalidPath`) — silently dropping a result is the worst possible default.
  Deliberately recorded: AWS moved Step Functions to JSONata + variables in 2024 because JSONPath shaping was its most-complained-about UX; v1 stays with plain JSONPath for smallness, and a JSONata-style upgrade is a later, additive decision — nothing in the event model depends on the shaping language.
- **Choice rules** are typed comparisons, not bare JSONPath (JSONPath alone cannot express `x > 5` in most dialects): `variable` (a JSONPath into the state input) plus exactly one of `stringEquals`, `numericEquals`, `numericGreaterThan`, `numericLessThan`, `booleanEquals`, `isPresent`, `isNull`, composable with `and`/`or`/`not` — the small orthogonal core of the Step Functions operator set; more operators are additive later.

### Error model (the wire contract Catch and Retry route on)

Step outcomes adopt the RFC-0024 settle matrix so the platform behaves uniformly:

| Function response | Classification | Behavior |
|---|---|---|
| 2xx | success | result recorded, `Next` proceeds |
| 4xx | permanent (`Fission.PermanentError`) | no retry — straight to `Catch` |
| 5xx / transport error | retryable (`Fission.FunctionError`) | retry per policy, then `Catch` |
| step timeout | retryable (`Fission.Timeout`) | retry per policy, then `Catch` |

A function signals a **typed** error by returning non-2xx with a JSON body `{"errorType": "<Name>", "cause": ...}` — `Catch` matches on `errorType` first, falling back to the status-class built-ins above when the body doesn't parse; `Fission.All` matches anything.
On a matched catch the error object **replaces** the flowing document (Step Functions parity); a route's optional `resultPath` instead merges it into the document at that JSONPath, so the catch target keeps the business data (a dunning flow retrying a charge after a grace period needs the original subscription, not just the error).
This convention is the function author's whole error API and appears in the docs example below.

### Worked example

The canonical three-step pipeline — sequential tasks, a typed-error catch, per-step retry (this exact YAML is the first thing a user sees; it is part of the design, not an afterthought):

```yaml
apiVersion: fission.io/v1
kind: Workflow
metadata:
  name: order-pipeline
spec:
  startAt: validate
  timeout: 1h
  defaultRetry: { maxAttempts: 3, backoffBase: 2s, backoffCap: 30s }
  states:
    validate:
      type: Task
      function: { name: validate-order }   # FunctionReference; type defaults to "name"
      timeout: 30s
      catch:
        - errorType: Fission.PermanentError   # bad input: don't retry, reject
          next: reject
      next: charge
    charge:
      type: Task
      function: { name: charge-card }
      retry: { maxAttempts: 5, backoffBase: 1s, backoffCap: 60s }
      catch:
        - errorType: PaymentDeclined          # typed error from the function body
          next: reject
      resultPath: $.charge
      next: fulfil
    fulfil:
      type: Task
      function: { name: fulfil-order }
      end: true
    reject:
      type: Task
      function: { name: notify-rejection }
      end: true
```

Webhook validation: `StartAt`/`Next`/`Default`/catch targets resolve to declared states, the graph reaches a terminal state, per-type field exclusivity (a Choice state cannot carry `Function`), JSONPath expressions parse, and referenced functions exist (warning, not error, to allow GitOps ordering).

### Controller (`pkg/workflow`)

- New dispatch entry in `cmd/fission-bundle` (`--workflowPort`, table-dispatch row like the other heads); package layout mirrors `pkg/timer`: Options-only `Start` with injectable listener and injected `statestore` capabilities, no env reads in constructors.
- Two reconcilers on one controller-runtime manager: `WorkflowRun` (the engine) and `Workflow` (validation status), both keyed on Generation via `GenerationChangedPredicate`, matching the router reconcilers' convention.
- **Wake path**: the fold is advanced by events (`StepSucceeded`, `TimerFired`, `BranchesJoined`) appended by workers, the timer lease loop, and future callbacks — none of which is a CR change, so without an explicit wake a run would only progress on the periodic resync.
  Every appender therefore pushes the run's key onto a `source.Channel` (`GenericEvent`) wired into the `WorkflowRun` controller, so appends enqueue an immediate reconcile.
- **Invocation worker pool**: the reconciler NEVER performs a function invocation inline (a 5-minute task step would pin a bounded reconcile worker for 5 minutes; a `Parallel` fan-out or a handful of long steps would starve `MaxConcurrentReconciles` and stall every other run — the head-of-line class the executor's specialization semaphore documents and RFC-0027's egress review re-found).
  The reconcile appends `StepScheduled` and returns; a dedicated worker pool (the RFC-0024 dispatcher's `wg.Go` fan-out shape) performs the POST with the per-step timeout and CAS-appends the completion — with the RFC-0024 A7 discipline that the delivery timeout sits strictly below any lease-like bound.
- `/readyz` gates on cache sync plus `statestore.Capabilities.Ping` (RFC-0021), following the MCP `RunnableFunc` pattern, so a warming replica is not serviced.

### Execution engine

Each `WorkflowRun` maps to EventLog stream `wfrun/<uid>`.
The first event is `RunStarted{workflowSpecSnapshot, input}` — the run **embeds the workflow spec it executes** (see `WorkflowGeneration` above): the stream alone determines the run's semantics, forever.
The reconcile loop is a pure fold:

1. Load the fold **checkpoint** (folded machine state + last-consumed seq, a KV entry at `Scope{Namespace: ns, Owner: "workflowrun/<name>", Keyspace: "checkpoint"}`), then `Read` only the stream tail past it and fold forward — re-folding a whole stream on every tick is O(events) and a 1000-item Map would re-fold 1000+ branch events per reconcile.
   Checkpoints are refreshed opportunistically (every N events); losing one only costs a longer re-fold, never correctness.
2. Compute the next actions (schedule step, complete run, arm timer).
3. For each action, `Append` a `StepScheduled{state, attempt, inputHash}` event with `expectedSeq` = the fold's last seq.
   A concurrent writer loses the CAS and simply re-reconciles — **correctness never depends on leader election**; the deployment still runs a single replica in v1 for simplicity (leader election can be added for HA later, purely as an optimization).
4. The invocation worker pool (above — never the reconciler) invokes the function: POST to the router internal listener at `utils.UrlForFunction(name, namespace)` (which folds the default namespace — never hand-build the path), signed with the `ServiceRouterInternal` HMAC key derived from `FISSION_INTERNAL_AUTH_SECRET`, exactly like the timer/mqtrigger publishers.
   Request body = the state's shaped input; per-step `Timeout` on the request context.
5. The worker `Append`s `StepSucceeded{outputRef}` or `StepFailed{errorType, cause}` (classified per the error model above) with CAS again, and pushes the wake channel; the next reconcile continues the fold.

Crash safety: if the controller dies between `StepScheduled` and the completion event, restart re-reads the log, sees a scheduled-but-unfinished step, and re-invokes it — hence at-least-once, hence documented idempotency expectations (`X-Fission-Workflow-Run` + `X-Fission-Workflow-Attempt` headers are sent so functions can dedup).
Retry policy failures append `StepFailed` and either reschedule (attempt+1, backoff delay via a Queue message, below) or route through `Catch`; exhausted retries with no catch fail the run.
Large step outputs (> 64KiB) are stored as a KV entry (`Scope{Namespace: ns, Owner: "workflowrun/<name>", Keyspace: "io"}`, matching RFC-0021's `<kind>/<name>` owner format) and referenced from the event, keeping the log lean.

### Timers, waits, and backoff

Wait states and retry backoffs must not hold goroutines or in-memory timers (they would die with the pod).
Both enqueue a delayed message on statestore Queue `wf-timers` (`EnqueueOptions.Delay`); the controller runs a small lease loop that turns fired messages into `TimerFired` events (CAS-appended), which the fold consumes.
`Wait` with `robfig/cron`-style absolute schedules is not in v1; only durations (the timer subsystem remains the cron owner).

### Parallel and Map

`Parallel` appends one `BranchScheduled` per branch; branches execute as independent sub-folds inside the same stream (events carry a `branchPath` discriminator), with `MaxConcurrency` throttling Map fan-out (default 10 — see the field comment).
A `BranchesJoined` event fires when all branch terminal events are present; join output is the ordered array of branch outputs.
**Branch-failure semantics are fail-fast at the fold level**: when one branch fails terminally (retries exhausted, no catch), the region fails with error class `Fission.BranchFailed` — a `Catch` on the Parallel/Map state may route it (the error object carries the branch and its inner error); without a catch the run fails.
There is no function kill signal, so sibling in-flight invocations drain, and their late completion appends lose the CAS against the terminal event and are discarded — W4 holds, nothing lands after a terminal event.
`Retry` on a Parallel/Map state is rejected in v1 (region-retry would re-execute every branch's side effects; the Catch route is the failure surface), and `ToleratedFailurePercentage`-style partial-failure Maps are deferred.
Nested fan-out (a Parallel/Map state inside a branch) is rejected in v1 — branches carry a bounded state type, which is also what keeps the CRD schema non-recursive.
The parallel-region protocol is modeled in [`specs/workflowbranch.tla`](specs/workflowbranch.tla) (join uniqueness W7, nothing-after-join W8, fail-fast) **before** phase-3 code, per the spec-first rule below.

### Cancellation and history

- `fission workflow runs cancel --name <run>` sets the metadata annotation `fission.io/cancel-requested` on the run → controller appends `RunCancelled`, stops scheduling, and lets in-flight invocations finish (no function kill signal exists; documented).
- History GC: a retention sweeper trims streams for finished runs past `HistoryRetention` via `EventLog.Trim`, and a `WorkflowRun` TTL (like Job `ttlSecondsAfterFinished`) deletes old run CRs.
- **A finalizer on `WorkflowRun` trims the run's stream and deletes its KV scopes (io, checkpoint) before the CR is deleted** — whether by TTL, `kubectl delete`, or namespace deletion.
  This guarantees no orphaned run history by construction; RFC-0027 had to defer exactly this problem (a published-but-never-consumed topic has no CR to hang cleanup on, and `EventLog` has no stream listing) — workflows have the CR, so the hole never opens.
  Precisely: `EventLog.Trim` reclaims every event payload, but the stream-head marker (one tiny row per deleted run) remains — an `EventLog.DeleteStream` capability is an RFC-0021 follow-up, not a growing leak.

### CLI

`fission workflow create|update|delete|list`, `fission workflow run --input @file.json`, `fission workflow runs`, `fission workflow runs history --name <run>` (renders the EventLog fold), `fission workflow runs cancel --name <run>`.
Debugging is a first-class surface, not just the raw log:

- `fission workflow runs describe --name <run>` — phase, active states, last error (`errorType` + cause), per-state attempt counts, next armed timer: the one-command answer to the motivating "where did order 4711's pipeline stop".
- `fission workflow runs history --name <run> --step <state> --io` — the step's actual (shaped) input and output, dereferencing KV-spilled payloads; debugging JSONPath shaping without seeing real payloads is misery.
- `fission workflow validate -f wf.yaml` — offline lint (graph reachability, expression parse, function existence) before anything touches the cluster.
- `fission workflow graph --name <name>` — render the state machine as mermaid (Parallel/Map branches as concurrent regions, states colored by type); `--open` renders it in a browser from an ephemeral local server, so the graph never leaves the machine.
- `fission workflow runs graph --name <run>` — the same diagram with each state colored by what THIS run did (succeeded/active/failed/unreached), drawn against the run's own spec snapshot: the visual form of "where did order 4711 stop". Choice/Succeed/Fail keep their type color — they resolve in the fold and emit no events, so the log cannot say whether the run passed through them.
- **Redrive** (resume a failed run from its failed state, preserving history — Step Functions' most-requested feature, shipped by AWS in 2023) is phase 5, but is named here so the event model never precludes it: it is one `RunRedriven` event resetting the failed state's attempt counter, which the fold already knows how to consume.

The CLI talks CRDs directly (house style); `history`/`describe --io` read through a small read-only endpoint on the workflow head (CRDs do not hold full history), signed like other internal calls.

### Security and networking

- The workflow pod is added to the router-internal NetworkPolicy `from` allowlist by its `svc:` label (`charts/fission-all/templates/router/networkpolicy.yaml`) — the known silent-drop bite otherwise; the statestore NetworkPolicy allowlist likewise.
- RBAC: read on workflows/functions, write on workflowruns/status; statestore DSN Secret mounted only here (and other RFC-0021 consumers).
  The head also needs `functions` list for the `crd.WaitForFunctionCRDs` boot probe (Forbidden there crash-loops the pod after the 30s gate — the RFC-0027 statestore-mqt lesson).
- **Tenancy cluster-role checklist** (a new CRD-watching head caches cluster-wide when `tenancy.mode != static`, and this bit RFC-0027's head with a Forbidden crash cycle on two of three CI tenancy legs): the workflow component must be registered in all three RBAC places — the namespaced role-generator includes, the `dynamic-cluster-roles.yaml` component list, and the `fission-cluster-role-generator` dispatch — or its reflector spins on cluster-scope Forbidden until the informer-sync timeout kills the pod.
- Input/output payloads may contain user data: history endpoint requires the same JWT auth as other authenticated surfaces when `authentication.enabled`.

## Invariants & verification

The engine is a fold over an append-only log, which makes its correctness unusually checkable: the entire protocol is "who may append what, when".

**Invariants** (checked by TLC in [`specs/workflowfold.tla`](specs/workflowfold.tla), which models racing reconcilers, CAS appends, crash/replan, retries, and cancellation):

- W1 *(NoDupSched)*: a (state, attempt) is scheduled at most once — execution is at-least-once, but the log never opens the same attempt twice.
- W2 *(AtMostOneResult)*: at most one result event per (state, attempt) — raced duplicate completions from re-invocation never both land.
- W3 *(ResultHasSched)*: every result is preceded by its schedule event.
- W4 *(TerminalIsLast)*: a terminal event (`done`/`failed`/`cancelled`) is the last event — nothing is ever appended after it; this is precisely what CAS-on-seq buys, and TLC confirms it holds even with a cancel racing in-flight completions.
- W5 *(RetryAfterFail)*: attempt a+1 is scheduled only after a recorded failure of attempt a.
- W6: scheduled attempts never exceed the retry budget.

TLC has verified W1–W6 at 2–3 steps × 2 attempts × 2–3 reconcilers (≈490k distinct states); the spec is the design authority — protocol changes (parallel/map branch events in phase 3 especially) extend the spec before the code, and CI runs TLC on every change under `docs/rfc/specs/` and `pkg/workflow/`.

**Implementation-time verification.**

- *Crash-point enumeration, not random crash injection*: the engine has a finite set of crash points per step (before append, between `StepScheduled` and invoke, between invoke and completion append); a table-driven test kills and resumes at every one and asserts the fold converges with attempts preserved — exhaustive and deterministic.
- *Replay determinism as a property*: `pgregory.net/rapid` generates valid event sequences and asserts fold(replay) is identical N times and prefix-monotone (folding a prefix then the rest equals folding the whole).
- *Virtual time via `testing/synctest`* (Go 1.26): wait-state timers, retry backoff delays, and step timeouts run inside a synctest bubble against the memory statestore — a "wait 6h then resume" test completes in microseconds with the engine using the standard `time` package, no clock seam and no flaky sleeps.
- *Fuzzing*: `go test -fuzz` on JSONPath input/output shaping and on WorkflowRun input parsing (the two grammar boundaries).
- *Trace validation (post-ship)*: the EventLog **is** an execution trace, so real histories from CI/integration runs are checked offline against the TLA+ spec (the MongoDB/CCF technique) — closing the loop between the model and the running engine.

## Alternatives considered

- **Revive `fission-workflows`** — its architecture (separate NATS-STAN event store, gRPC API server, per-environment interpreter) is exactly the operational weight that killed it; nothing above needs any of that.
- **Argo Workflows / Temporal integration docs instead of a native engine** — both work today for power users, but treat functions as opaque HTTP, require their own control planes, and leave the 80% case (a five-step pipeline with retries) paying a second system's operational bill.
  A native `Workflow` CRD is also what makes RFC-0011's agent story composable (a workflow is itself exposable as an MCP tool later).
- **Choreography via RFC-0024 destinations only** — onSuccess/onFailure chaining covers linear pipelines but cannot express join/fan-in, choice, or run-level history; the two compose rather than compete.
- **Storing history in WorkflowRun status** — etcd write-amplification per step and the 1.5MiB cap; rejected (bounded `RecentEvents` tail only).

## Backward compatibility

Purely additive: two new CRDs, one new optional head (`workflows.enabled` in Helm, off by default, render-gated on `statestore.enabled` per RFC-0021).
No existing subsystem changes behavior.

## Rollout phases (one PR each, bisectable)

1. CRDs + codegen + webhook validation (incl. input cap, expression grammar, choice operators) + CLI CRUD + `validate -f`/`graph` + printer columns + status conditions (no engine); `Workflow` status reports graph validation; a `WorkflowRun` with no running controller carries the "not accepted" condition.
2. Engine v1: Task/Choice/Succeed/Fail with the error model above, spec-snapshot `RunStarted`, invocation worker pool + wake channel, fold checkpoints, retries with Queue-backed backoff, EventLog persistence, resume-on-restart, run timeout, `fission workflow run/history/describe`.
3. Parallel/Map with join, fail-fast branch semantics, and `MaxConcurrency`; cancellation; retention/GC sweeper + the `WorkflowRun` finalizer.
4. Wait states (duration), idempotency headers, observability: metrics (`fission_workflow_runs_total`, `_step_duration_seconds`, `_active_runs` via RFC-0019 OTel meters — labeled by workflow and state name only, NEVER by run UID or any per-run value: unbounded label values mint unbounded series, the RFC-0027 lesson) **and traces** — a run is literally a trace: root span per run, child span per step attempt, linked to the function's own spans via the RFC-0015/0019 correlation machinery, so one trace view answers "which step was slow" and `fission logs --request-id` reaches workflow steps; Grafana dashboard.
5. (Later, separate RFC-sized decisions) callback states, redrive, an HTTPTrigger/topic-event → `WorkflowRun` adapter (event-driven starts via RFC-0027), workflow-as-MCP-tool, HA leader election.

Every phase ends with the three-lens review battery (code-review + silent-failure + security agents) before its PR: on RFC-0024/0027 every CRITICAL found post-spec lived **outside** the TLA-modeled protocol (unsigned-client feature-break; consumer-less egress queue; tenancy RBAC) — exactly the classes the battery catches and TLC cannot.

## Verification / test plan

- Engine unit tests against the memory statestore: fold determinism (replay N times → same state), CAS conflict on concurrent reconciles yields no double-schedule, crash-between-schedule-and-complete resumes with attempt preserved.
- Integration suite (`test/integration/suites/common/workflow_test.go`): 3-step sequential pipeline via real functions, choice branching, parallel join, retry-then-catch on a deliberately failing function, controller-restart resume (serial suite, via `framework.SetExecutorEnv`-style rollout helpers on the workflow deployment).
- Chart drift test: workflow pod present in the router-internal NetworkPolicy allowlist.
- Bench: RFC-0020 scenario measuring step overhead (target: <15ms controller overhead per step beyond function latency, given the ~100ms cold-start budget context).

## Open questions

- Whether `WorkflowRun` creation goes through the CLI only or also an HTTP trigger form ("start workflow on POST") in v1 (leaning: CLI/CRD only; HTTPTrigger→WorkflowRun and topic-event→WorkflowRun adapters are clean phase-5 items now that RFC-0027 is shipped).
- Step output size cap before spilling to KV (64KiB proposed; measure against real payloads).
- Whether `Map` items iterate inline (one branch per item) or chunked for very large arrays (cap `ItemsPath` length in v1, chunking later).
- The max-active-runs admission guard's default (new runs beyond it queue in `Pending` with a condition; `_active_runs` is the alert signal) — the same bounded-resource posture as RFC-0027's backlog cap, sized during phase-2 load testing.
- Final JSONPath library confirmation at phase 1 (`ojg/jp` proposed; the pinned-dialect requirement itself is settled above).

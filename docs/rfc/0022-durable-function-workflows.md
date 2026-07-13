# RFC-0022: Durable function workflows

- Status: Proposed
- Tracking issue: TBD
- Supersedes: the abandoned pre-2020 `fission-workflows` project (external repo, NATS-backed, unmaintained)
- Targets: Fission v1.N+1
- Requires: RFC-0021 statestore (`EventLog` + `Queue` + `KVStore` capabilities — KV holds oversized step I/O); composes with RFC-0024 async invocation (task-step retries) but does not require it.

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
- Full step-level history queryable from the CLI (`fission workflow history <run>`).
- At-least-once step semantics with documented idempotency expectations (identical to Step Functions' contract).

## Non-goals

- A Turing-complete DSL or embedded scripting; states are data, logic lives in functions.
- Exactly-once step execution.
- Long "wait for external callback" states in v1 (phase 3).
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
    Choices    []ChoiceRule       `json:"choices,omitempty"` // JSONPath condition → next
    Default    string             `json:"default,omitempty"`
    // Parallel / Map
    Branches   []WorkflowBranch   `json:"branches,omitempty"`
    ItemsPath  string             `json:"itemsPath,omitempty"`
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
    // WorkflowGeneration pins the spec generation this run executes, so a
    // Workflow edit never changes in-flight runs.
    WorkflowGeneration int64         `json:"workflowGeneration,omitempty"` // set by webhook if empty
    Input   *runtime.RawExtension    `json:"input,omitempty"`
}

type WorkflowRunStatus struct {
    Phase        RunPhase      `json:"phase"` // Pending|Running|Succeeded|Failed|Cancelled
    ActiveStates []string      `json:"activeStates,omitempty"`
    StartedAt, FinishedAt *metav1.Time
    Output  *runtime.RawExtension `json:"output,omitempty"`
    // RecentEvents is a bounded (≤20) tail for kubectl visibility; the full
    // history lives in the statestore EventLog, never in etcd.
    RecentEvents []RunEventSummary `json:"recentEvents,omitempty"`
}
```

Webhook validation: `StartAt`/`Next`/`Default`/catch targets resolve to declared states, the graph reaches a terminal state, per-type field exclusivity (a Choice state cannot carry `Function`), JSONPath expressions parse, and referenced functions exist (warning, not error, to allow GitOps ordering).

### Controller (`pkg/workflow`)

- New dispatch entry in `cmd/fission-bundle` (`--workflowPort`, table-dispatch row like the other heads); package layout mirrors `pkg/timer`: Options-only `Start` with injectable listener and injected `statestore` capabilities, no env reads in constructors.
- Two reconcilers on one controller-runtime manager: `WorkflowRun` (the engine) and `Workflow` (validation status + generation snapshots), both keyed on Generation via `GenerationChangedPredicate`, matching the router reconcilers' convention.
- `/readyz` gates on cache sync plus `statestore.Capabilities.Ping` (RFC-0021), following the MCP `RunnableFunc` pattern, so a warming replica is not serviced.

### Execution engine

Each `WorkflowRun` maps to EventLog stream `wfrun/<uid>`.
The reconcile loop is a pure fold:

1. `Read` the stream, fold events into the current machine state (active states, attempt counts, pending timers, gathered branch results).
2. Compute the next actions (schedule step, complete run, arm timer).
3. For each action, `Append` a `StepScheduled{state, attempt, inputHash}` event with `expectedSeq` = the fold's last seq.
   A concurrent writer loses the CAS and simply re-reconciles — **correctness never depends on leader election**; the deployment still runs a single replica in v1 for simplicity (leader election can be added for HA later, purely as an optimization).
4. Invoke the function: POST to the router internal listener at `utils.UrlForFunction(name, namespace)` (which folds the default namespace — never hand-build the path), signed with the `ServiceRouterInternal` HMAC key derived from `FISSION_INTERNAL_AUTH_SECRET`, exactly like the timer/mqtrigger publishers.
   Request body = the state's shaped input; per-step `Timeout` on the request context.
5. `Append` `StepSucceeded{outputRef}` or `StepFailed{errorClass, body}` with CAS again; then reconcile continues the fold.

Crash safety: if the controller dies between `StepScheduled` and the completion event, restart re-reads the log, sees a scheduled-but-unfinished step, and re-invokes it — hence at-least-once, hence documented idempotency expectations (`X-Fission-Workflow-Run` + `X-Fission-Workflow-Attempt` headers are sent so functions can dedup).
Retry policy failures append `StepFailed` and either reschedule (attempt+1, backoff delay via a Queue message, below) or route through `Catch`; exhausted retries with no catch fail the run.
Large step outputs (> 64KiB) are stored as a KV entry (`Scope{Namespace: ns, Owner: "workflowrun/<name>", Keyspace: "io"}`, matching RFC-0021's `<kind>/<name>` owner format) and referenced from the event, keeping the log lean.

### Timers, waits, and backoff

Wait states and retry backoffs must not hold goroutines or in-memory timers (they would die with the pod).
Both enqueue a delayed message on statestore Queue `wf-timers` (`EnqueueOptions.Delay`); the controller runs a small lease loop that turns fired messages into `TimerFired` events (CAS-appended), which the fold consumes.
`Wait` with `robfig/cron`-style absolute schedules is not in v1; only durations (the timer subsystem remains the cron owner).

### Parallel and Map

`Parallel` appends one `BranchScheduled` per branch; branches execute as independent sub-folds inside the same stream (events carry a `branchPath` discriminator), with `MaxConcurrency` throttling Map fan-out.
A `BranchesJoined` event fires when all branch terminal events are present; join output is the ordered array of branch outputs.

### Cancellation and history

- `fission workflow cancel <run>` sets the metadata annotation `fission.io/cancel-requested` on the run → controller appends `RunCancelled`, stops scheduling, and lets in-flight invocations finish (no function kill signal exists; documented).
- History GC: a retention sweeper trims streams for finished runs past `HistoryRetention` via `EventLog.Trim`, and a `WorkflowRun` TTL (like Job `ttlSecondsAfterFinished`) deletes old run CRs.

### CLI

`fission workflow create|update|delete|list`, `fission workflow run --input @file.json`, `fission workflow runs`, `fission workflow history <run>` (renders the EventLog fold), `fission workflow cancel <run>`.
The CLI talks CRDs directly (house style); `history` reads through a small read-only endpoint on the workflow head (CRDs do not hold full history), signed like other internal calls.

### Security and networking

- The workflow pod is added to the router-internal NetworkPolicy `from` allowlist by its `svc:` label (`charts/fission-all/templates/router/networkpolicy.yaml`) — the known silent-drop bite otherwise.
- RBAC: read on workflows/functions, write on workflowruns/status; statestore DSN Secret mounted only here (and other RFC-0021 consumers).
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

TLC has verified W1–W6 at 2–3 steps × 2 attempts × 2–3 reconcilers (≈490k distinct states); the spec is the design authority — protocol changes (parallel/map in phase 2 especially) extend the spec before the code, and CI runs TLC on every change under `docs/rfc/specs/` and `pkg/workflow/`.

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

1. CRDs + codegen + webhook validation + CLI CRUD (no engine); `Workflow` status reports graph validation.
2. Engine v1: Task/Choice/Succeed/Fail, retries with Queue-backed backoff, EventLog persistence, resume-on-restart, `fission workflow run/history`.
3. Parallel/Map with join and `MaxConcurrency`; cancellation; retention/GC sweeper.
4. Wait states (duration), idempotency headers, metrics (`fission_workflow_runs_total`, `_step_duration_seconds`, `_active_runs` via RFC-0019 OTel meters), Grafana dashboard.
5. (Later, separate RFC-sized decisions) callback states, workflow-as-MCP-tool, HA leader election.

## Verification / test plan

- Engine unit tests against the memory statestore: fold determinism (replay N times → same state), CAS conflict on concurrent reconciles yields no double-schedule, crash-between-schedule-and-complete resumes with attempt preserved.
- Integration suite (`test/integration/suites/common/workflow_test.go`): 3-step sequential pipeline via real functions, choice branching, parallel join, retry-then-catch on a deliberately failing function, controller-restart resume (serial suite, via `framework.SetExecutorEnv`-style rollout helpers on the workflow deployment).
- Chart drift test: workflow pod present in the router-internal NetworkPolicy allowlist.
- Bench: RFC-0020 scenario measuring step overhead (target: <15ms controller overhead per step beyond function latency, given the ~100ms cold-start budget context).

## Open questions

- Whether `WorkflowRun` creation goes through the CLI only or also an HTTP trigger form ("start workflow on POST") in v1 (leaning: CLI/CRD only; an HTTPTrigger→WorkflowRun adapter is a clean phase-5 item).
- Step output size cap before spilling to KV (64KiB proposed; measure against real payloads).
- Whether `Map` items iterate inline (one branch per item) or chunked for very large arrays (cap `ItemsPath` length in v1, chunking later).

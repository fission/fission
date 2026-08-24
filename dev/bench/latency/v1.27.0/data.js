window.BENCHMARK_DATA = {
  "lastUpdate": 1787559787915,
  "repoUrl": "https://github.com/fission/fission",
  "entries": {
    "Fission latency (v1.27.0)": [
      {
        "commit": {
          "author": {
            "name": "Sanket Sudake",
            "username": "sanketsudake",
            "email": "sanketsudake@gmail.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "12dbcbc686ba7813c2cfc7a3a3866dca6f4ad51f",
          "message": "feat: RFC-0022 durable function workflows (#3587)\n\n* feat(workflow): Workflow and WorkflowRun CRD types (RFC-0022 phase 1)\n\nTwo new namespaced CRDs following the house kubebuilder+client-gen pattern:\nWorkflow (a declarative state machine whose Task states are Fission\nfunctions) and WorkflowRun (one execution, with a bounded status event tail;\nfull history lives in the statestore EventLog).\n\nType-level decisions per the revised RFC and plan review:\n- Reuses the RFC-0024 RetryPolicy for per-state/default retry.\n- Choice numeric comparisons use resource.Quantity (CRDs cannot carry\n  floats; Quantity accepts YAML numbers and strings).\n- WorkflowChoiceCondition.Variable is schema-optional because the type is\n  inline-embedded in WorkflowChoiceRule; the webhook enforces leaf presence.\n- Parallel/Map/Wait fields arrive with their engine phases so admission\n  never accepts a state the engine cannot run.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): pinned JSONPath dialect package (ojg/jp)\n\nRFC-0022 requires one library, one documented dialect for workflow JSONPath\n(Go JSONPath libraries diverge on filters, unions, and no-match behavior).\npkg/workflow/expr is the single entry point; the parse table in its test is\nthe dialect contract admission enforces. Evaluation semantics (no-match ->\nnull on read paths, Fission.InvalidPath on ResultPath writes) land with the\nphase-2 engine.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): graph and spec validation for Workflow/WorkflowRun\n\nThe rule set the admission webhook enforces (nothing here is\nCEL-expressible: reachability needs a traversal, JSONPath needs a parser):\n\n- StartAt/Next/Default/Catch targets resolve to declared states.\n- BFS reachability: unreachable states and terminal-unreachability are\n  errors; cycles are legal (the run Timeout bounds them).\n- Per-type field exclusivity (a Choice cannot carry Function/Retry/Catch;\n  Succeed/Fail are bare terminals; Task sets exactly one of Next/End).\n- Choice rules: leaf XOR composite (depth-1 and/or/not), Variable required\n  and parseable on every leaf, exactly one comparison operator.\n- Catch: errorType required, duplicates rejected (first match wins, a\n  duplicate is dead), targets resolve.\n- Workflow retries bound to MaxWorkflowAttempts=10 (fold-level attempts,\n  deliberately not the async queue's MaxAsyncAttempts=3 clamp).\n- WorkflowRun input capped at 256KiB (raw-bytes fields break CEL).\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): admission webhooks for Workflow and WorkflowRun\n\nValidating-only webhooks (nothing to default): the whole workflow rule set\nis non-CEL, so admission enforces the graph/expression validation added in\nthe previous commit. Referenced-function existence is deliberately a\ncontroller status condition, not an admission error, so GitOps apply order\nstays legal.\n\nWebhook manifests synced in all three places: service.go injectors, the\nHelm chart's webhooks.yaml, and the e2e framework manifest.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): default Task function-reference type to \"name\"\n\nThe RFC's worked example writes function: {name: charge-card} with no type\n— \"type defaults to name\" is part of the spec's UX contract, and a\nkubectl-applied manifest must not fail admission over it. Defaulting lives\non the API type (WorkflowSpec.ApplyDefaults), applied by a mutating webhook\non Workflow and by the CLI manifest loader so offline validation matches\nadmission. Mutating manifest synced to the chart and e2e framework copies.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(fission-cli): workflow command group\n\nfission workflow create/update/delete/list/validate/graph. Manifests are\nfile-based (-f): a full Workflow or a bare WorkflowSpec (--name then\nrequired) — a state machine is not expressible as flags. create/update run\nclient-side validation for fast feedback (the webhook still gates), and\nhonor --spec save/dry-run. validate lints offline (same rule set as\nadmission) and warns — never errors — on missing referenced functions so\nGitOps ordering stays legal; graph renders deterministic mermaid\nstateDiagram-v2. validate/graph are cluster-optional commands (RFC-0018's\nannotation) so they work without a kubeconfig.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(fission-cli): spec apply support for Workflow\n\nWorkflow is spec-managed like TimeTrigger (a GitOps object; WorkflowRun is\nruntime data and deliberately not spec-managed, like CanaryConfig). Wires\nall seven touchpoints: FissionResources slice, marshal/parse/dedup\nswitches, namespace forcing, the resourceOps apply block, and the validate\nloop — which checks every Task state's function reference against the spec\nset, one reference per state.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* test(workflow): integration coverage for workflow CRUD and admission\n\nCLI create/list/validate/graph/delete on the RFC's worked-example manifest\nverbatim (pinning the mutating webhook's function-type defaulting), webhook\nrejection of an unresolvable Next target, and the WorkflowRun 256KiB input\ncap. No engine in phase 1, so an accepted run is asserted to sit\nun-serviced. Also adds workflows/workflowruns to the preupgrade RBAC list.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* fix(workflow): bound states map and FunctionReference.Name for CEL cost\n\nenvtest caught the generated workflows CRD failing apiserver admission:\nFunctionReference carries a CEL XValidation rule, and embedding it under\nthe unbounded states map blew the per-CRD cost budget >100x (the estimator\ncannot price a regex over an unbounded string times an unbounded map).\nmaxProperties=100 on states (mirrors MaxWorkflowStates) and maxLength=63\non FunctionReference.Name (a DNS-1123 label bound the CEL rule already\nimplies) bring the estimate under budget; test/cel and preupgradechecks\nenvtest suites now install the CRD successfully.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* refactor(workflow): phase-1 review battery findings\n\nReview battery (code-reviewer, silent-failure-hunter, thermo-nuclear audit)\nfindings applied:\n\nSilent failures fixed:\n- fission spec destroy now deletes Workflows (all three destroy paths);\n  previously a spec-created Workflow survived destroy with exit 0.\n- spec list / spec validate conflict-check / resource summary now include\n  Workflows instead of silently omitting them.\n- The spec parse path applies the same function-type defaulting as kubectl\n  and workflow create, so spec validate/apply accepts the RFC's worked\n  example verbatim.\n- A Task function reference of type function-weights (an HTTPTrigger canary\n  concern the engine cannot execute) is rejected at admission.\n- Multi-document manifests are rejected explicitly: sigs.k8s.io/yaml decodes\n  only the first document, which silently dropped the rest.\n- workflow validate/graph guard the nil client on cluster-optional runs\n  (previously a panic), validate checks functions in the manifest's\n  namespace (not the flag fallback) and validates the name when present,\n  and graph refuses empty specs and --file+--name together.\n- The Workflow webhook warns on Fission.*-prefixed catch errorTypes outside\n  the built-in set (a typo'd built-in is a silently dead route).\n\nStructure:\n- Workflow validation moved to its own workflow_validation.go (validation.go\n  was already past the size bar).\n- Shared RetryPolicy backoff-bounds check extracted; async keeps its\n  MaxAsyncAttempts clamp, workflows their MaxWorkflowAttempts.\n- State names pinned to ^[A-Za-z0-9_-]{1,64}$ before they become durable\n  identifiers in run history.\n- ValidateForAdmission identity wrappers dropped; webhooks call Validate.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* chore(workflow): drop unused expr.Path.String\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* refactor(workflow): /simplify pass — reuse and efficiency fixes\n\n- WorkflowState.IsTerminal() is the single source of truth for terminal\n  states, shared by the graph validator and the mermaid renderer (and the\n  phase-2 fold).\n- spec.SplitYAMLDocuments is the one YAML document splitter, shared by the\n  spec reader and the workflow manifest loader so delimiter handling never\n  drifts.\n- workflow validate lists functions once instead of one Get per state (up\n  to 100 sequential round-trips before).\n- The webhook's built-in error-class set is a package-level var, not a\n  per-admission-call allocation.\n\nSkipped (noted): a generic client-side Defaulter hook in parseResource —\nonly Workflow defaults today; generalize when a second kind needs it.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): engine event schema\n\nThe wire/log contract of the RFC-0022 fold, mirroring workflowfold.tla's\nvocabulary (sched/ok/fail/done/failed/cancelled) plus RunStarted (the\nauthoritative spec snapshot — a run is self-contained forever) and\nTimerFired (Queue-armed backoff). Decoding is strict: an unknown event type\nfails loud instead of being silently skipped. Streams are keyed on run UID\nso a delete-and-recreate never resumes the old log.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): expression evaluation with pinned no-match semantics\n\nPath.Get pins first-match, distinguishes matched-null from no-match (the\nRFC maps no-match to JSON null on read paths). Path.SetResult never mutates\nits input, auto-creates missing map parents (Step Functions parity for\nresultPath), and errors on genuinely unwritable paths — Fission.InvalidPath\nat the engine layer, because silently dropping a result is the worst\npossible default.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): step I/O shaping and choice evaluation\n\nInputPath/ResultPath/OutputPath with the pinned dialect semantics (no-match\nreads as null; an unwritable ResultPath is errInvalidPath ->\nFission.InvalidPath) and typed choice-rule evaluation: numeric compares go\nthrough resource.Quantity so 42 == \"42\" == 42.0, missing values never\nmatch a comparison operator (Step Functions parity), isPresent/isNull\ndistinguish matched-null from no-match.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): deterministic run-state fold\n\nThe pure fold of a run's event stream into RunState. Choice states resolve\nduring advancement and never append events — only Task states hit the log,\nmatching workflowfold.tla's task-step model. Workers append the already-\nshaped next document, so the fold needs no shaping; spilled documents\ndereference through an immutable-KV resolver, preserving determinism.\n\nThe fold re-verifies the TLA invariants structurally and fails loud on any\nimpossible sequence (dup schedule W1, dup result W2, result-without-sched\nW3, post-terminal event W4, attempt skip W5) — the log is CAS-protected,\nso an impossible sequence means corruption, never something to skip.\nReplay determinism (fold(prefix)+fold(rest) == fold(all), repeatable) is\npinned as a rapid property.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): decide — the model's transition function\n\ndecide is workflowfold.tla's NextOptions in Go: a pure function of the fold\n(plus the cancel annotation and clock, which the model treats as\nenvironment), so racing reconcilers compute the same action and CAS\narbitrates. Failure ROUTING lives in the fold (policy, attempt, and class\nare all log-derived): retryable-with-budget stays put for decide to arm the\nbackoff timer (W5: reschedule only after TimerFired), permanent classes and\ntyped function errors go straight to the first matching Catch route with\nthe error object as the flowing document, and unroutable failures go\nrun-level. Bare Fail states fail with the new Fission.Failed class.\n\nThe exponential full-jitter backoff is extracted to pkg/utils/backoff,\nshared with the async dispatcher so retry pacing can never drift.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): engine core — checkpointed CAS fold loop, invoker, timers\n\nThe reconcile loop is checkpoint -> read tail -> fold -> decide ->\nCAS-append; a lost CAS re-reads and replans, never errors (correctness\nnever depends on leader election). Invocations run on a bounded worker\npool, never inline in the reconciler; workers classify outcomes per the\nRFC error model, shape the next document (spilling >64KiB to the run's KV\nio keyspace), and CAS-append through a guard that drops the write when the\nattempt is already resolved (W2) or the run went terminal (W4 — a late\ncompletion loses to a racing cancel by design). Queue-backed timers turn\nbackoff delays into TimerFired events with the same guard; a timer lost to\nthe DLQ heals via the resync re-arm (dedup keys only span unsettled\nmessages). Fold checkpoints are opportunistic KV writes — losing one costs\na longer re-fold, never correctness.\n\nVerification (all under -race against the memory statestore + a scripted\nhttptest router): linear pipeline, retry-then-catch, permanent-error\nfast-fail, cancellation, 64KiB spill round-trip, crash-point enumeration\n(fresh engine resumes at 7 points of the step lifecycle; W1-W6 asserted\nover the final log every time), and two engine instances racing the same\nrun.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): fission-bundle workflow head, reconcilers, history API\n\n- --workflowPort dispatches workflow.Start: statestore open (env-driven,\n  read once in Start), HMAC-signed invoker to the router internal listener,\n  the engine, and a non-leader-elected manager (single replica in v1;\n  correctness never depends on it — CAS arbitrates).\n- WorkflowRun reconciler drives the engine and mirrors the fold into status\n  (phase, activeStates, bounded RecentEvents tail, Accepted condition);\n  Workflow reconciler writes the Validated graph condition. Cancel arrives\n  as an annotation, so the run controller composes\n  Or(GenerationChanged, AnnotationChanged) predicates.\n- Appends wake the reconciler through a source.Channel raw source — added\n  as a general pkg/controller capability\n  (RegisterTenantScopedWithRawSources) that composes the tenant watch.\n- Read-only run-history endpoint (GET /history/{ns}/{name}), verified with\n  a new ServiceWorkflow HMAC channel; /readyz gates on cache sync AND\n  statestore Ping.\n- WorkflowRun spec is immutable after creation via a new generic\n  UpdateValidator webhook facet (ValidateTransition — deliberately not\n  named ValidateUpdate, which would shadow the embedded implementation).\n- svcinfo: PortWorkflow=8892, SvcWorkflow.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(fission-cli): workflow run, runs, history, describe\n\n- run: reads the Workflow (recording its generation), creates a\n  WorkflowRun with inline-JSON or @file input, and warns when no workflow\n  controller is deployed — a run with no controller would otherwise sit\n  Pending with no signal (the client-side NoWorkflowController surface;\n  a status condition cannot exist without a running writer).\n- runs: lists runs; non-terminal runs older than 30s with no Accepted\n  condition render as \"(NoWorkflowController)\".\n- history: reads the head's signed history endpoint over the portless\n  port-forward plane (ProxyGet cannot carry HMAC headers), --io\n  dereferences spilled payloads; FISSION_WORKFLOW_URL overrides.\n- describe: phase, active state, last error with cause, per-state attempt\n  counts, duration — degrading to status-only when the head is\n  unreachable.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(charts): workflow head deployment, RBAC, networkpolicy allowlists\n\n- templates/workflow/: deployment (--workflowPort 8892, statestore env per\n  mode, internalAuth.envs, readyz gating), ClusterIP service, service\n  account, per-namespace role.\n- workflow-rules RBAC wired in all three tenancy places (namespaced\n  role-generator, cluster-role generator dispatch, dynamic-cluster-roles\n  component list) — the RFC-0027 Forbidden-crash-loop lesson.\n- svc: workflow added to the router-internal NetworkPolicy allowlist (the\n  statestore allowlist already anticipated it).\n- values: workflows.enabled=false; the existing statestore validate gate\n  fails render when workflows are enabled without a statestore.\n- TestWorkflowChart drift test pins ports against svcinfo, the statestore\n  env wiring, and membership in BOTH allowlists.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* test(workflow): engine integration coverage + kind profile enablement\n\nLive-cluster tests (gated on NODE_RUNTIME_IMAGE): the three-step linear\npipeline run via the CLI and asserted through status + history/describe,\nretry-then-catch with a deliberately failing function (attempt count and\nTimerFired asserted from history), spec-immutability at admission, and the\ndangling-workflowRef GitOps case. skaffold kind profiles enable\nworkflows.enabled so CI legs and local kind runs deploy the head. Also\npins the lenient success contract for plain-text functions (a non-JSON\nbody folds as a JSON string).\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* test(fission-bundle): dispatch table grows to 15 heads (workflow)\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* fix(workflow): review-battery findings — InputPath, history authz, timeout precedence\n\n- CRITICAL (code review): InputPath was validated at admission but never\n  applied at invoke time — functions always received the full flowing\n  document. The invoker now sends the InputPath-selected view while keeping\n  the raw document for the ResultPath merge (ASL semantics); an engine test\n  pins the request bodies end to end.\n- MEDIUM (security): the history endpoint read any stream by uid while\n  ignoring the claimed namespace/name. It now resolves the run at\n  {namespace}/{name} and requires the uid to match, with a single 404 for\n  both mismatch cases so run existence is not confirmable cross-namespace.\n- decide now settles a fold-complete run on its own outcome even when the\n  deadline passed in the same reconcile — the timeout stops runs that\n  cannot finish, not ones that did.\n- Defense-in-depth: the invoker re-validates the snapshot's function name\n  (a URL path segment) before building the request.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* fix(workflow): silent-failure audit — livelock, W4 timer hole, hot loops\n\nCriticals:\n- Stale-checkpoint livelock: checkpoints are name-keyed but streams are\n  UID-keyed, so a delete-and-recreate under the same name restored the OLD\n  run's fold and spun the single reconcile worker forever (append at a seq\n  past the empty stream's head -> conflict -> re-read zero events ->\n  repeat). Checkpoints now carry the run UID and are discarded on mismatch,\n  and the engine fails loud (EngineError condition) when the stream head is\n  behind the folded seq — a stale checkpoint or trimmed stream can never\n  converge by re-reading.\n- TimerFired-after-terminal: the timer path's terminal guard only ran on\n  CAS conflict, so a backoff firing after a cancel appended cleanly and\n  poisoned the stream against its own fold (strict W4). The fold now\n  tolerates exactly this one benign post-terminal event — TimersFired is\n  only consulted for live runs — and stays strict for every event the TLA\n  invariants cover.\n\nHot-loop class:\n- A failed result append no longer wakes the reconciler (wake -> re-invoke\n  -> fail -> wake would re-execute the function's side effects as fast as\n  it responds); the 60s resync is the retry cadence.\n- Dispatch never blocks the reconcile worker: in-flight (run, state,\n  attempt) dedup stops the resync re-dispatching long steps, and a full\n  pool defers to resync instead of freezing the run control plane.\n- expr.Parse failures in Result/OutputPath now wrap errInvalidPath: they\n  are as permanent as an unwritable path, and anything else re-invoked a\n  SUCCEEDED function forever on webhook-bypassed specs.\n- Process shutdown mid-invocation appends nothing (a pod restart must\n  never consume an attempt; previously classified Fission.Timeout).\n\nSurfacing:\n- A lost TERMINAL status write now requeues (no future wake exists to heal\n  it; the run displayed Running forever) and logs at Error, not V(1).\n- A run whose Workflow never appears is terminally failed after a 10m\n  GitOps grace instead of reconciling every minute forever.\n- Timer-loop Head/append/Nack/Kill failures are logged; readyz 503s say\n  why; history keeps the OutputRef when a spilled payload is unreadable;\n  the CLI warns when the auth-secret read fails instead of silently\n  sending unsigned requests.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* docs(rfc): TLA+ model for phase-3 parallel regions (spec-first)\n\nworkflowbranch.tla verifies what phase 3 genuinely adds over the linear\nfold: concurrent branch execution over one CAS-append log, the join\ndiscipline (W7: joined is unique and only follows every branch succeeding;\nW8: after joined only the terminal event lands — a late branch append\nalways loses the CAS), and fail-fast (an exhausted branch fails the run;\nsibling completions then lose to the terminal, W4 unchanged). W1-W6 lift\nper (branch, attempt). Each branch is one task step with attempts —\nbranch-internal sequences compose per workflowfold.tla, and MaxConcurrency\nis deliberately unmodeled (it throttles dispatch, never which appends are\nlegal). TLC green at Branches=2 x MaxAttempts=2 x 2 reconcilers; wired\ninto hack/run-tlc.sh's green list (the tlc.yaml job already watches\ndocs/rfc/specs/**).\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* refactor(workflow): declarative state-field exclusivity table\n\nPhase-3 precondition: three hand-enumerated per-type \"must not set\" lists\nwere a growth trap — every new WorkflowState field had to be added to two\nor three of them, and a missed entry silently admitted junk fields into\ndurable spec snapshots. One declarative table (field, isSet, allowedOn)\nreplaces them; adding a field is now exactly one row.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): Parallel and Map state types with bounded branch graphs\n\nBranches carry a bounded WorkflowBranchState (WorkflowState minus the\nfan-out fields): nested Parallel/Map is impossible BY TYPE, which is what\nkeeps the CRD schema non-recursive for controller-gen. Map takes exactly\none branch (the iterator template) plus ItemsPath; MaxConcurrency defaults\nto 10 in the ENGINE, not the schema (a schema default would stamp the\nfield onto every state type and trip the exclusivity table). Catch is\nallowed on fan-out states and routes the new Fission.BranchFailed class;\nRetry is rejected (region-retry would re-execute every branch's side\neffects) — the RFC's branch-failure paragraph is amended accordingly, and\nits \"zero orphaned streams\" wording now states precisely what Trim gives.\n\nBranch graphs run the same per-state rules and reachability walk as the\ntop level (widened via ToState). Branch states cap at 20 (vs 100\ntop-level): the doubly-nested FunctionReference CEL rule was 8% over the\napiserver's cost budget at 50 — caught by the envtest gate again.\n\nAlso refreshes test/benchmark's go.sum (nested module; controller-gen\nwalks it and needed the ojg entry).\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): parallel execution engine with join and fail-fast\n\nA branch is a run in miniature: RunState gains BranchRuns (per-branch\nmini-states over the SAME fold machinery — attempts, results, in-branch\ncatch routing all reuse), and branch-tagged step events route into their\nmini-run. Region entry (advance reaching Parallel/Map) shapes the region\ninput, seeds one mini per branch (or per ItemsPath item, capped at 100)\nwith the workflow's DefaultRetry carried over, and resolves ItemsPath\ndeterministically from the flowing document.\n\ndecide returns an action LIST (a region dispatches several branches\nconcurrently); decideRegion is workflowbranch.tla's NextOptions — all\nbranches done -> join, per-branch step actions otherwise, NEW branches\nopened only under MaxConcurrency (engine default 10), sorted iteration so\nracing reconcilers compute identical lists, log-changing actions first so\nthe engine's process-first-append loop converges. decideStep never emits\ntimeouts or terminal actions for mini-runs (a branch has no RunStarted; a\nraw decide would misfire the deadline check — the plan-review blocker).\n\nJoin: the engine assembles the ordered branch-output array, merges it per\nResult/OutputPath, spills when large, and CAS-appends EvBranchesJoined;\nthe fold enforces W7 (unique, all-branches-ok) and W8 (nothing after the\njoin but the region's continuation), with the phase-2 TimerFired\nredelivery carve-out extended to closed regions. Fail-fast: a terminally\nfailed branch dissolves the region with Fission.BranchFailed, routable by\na Catch on the fan-out state.\n\nInvoker/timers thread the branch through dedup keys, spill keys, the\nX-Fission-Workflow-Branch header, and the CAS guards (a late branch result\nor timer after the join is dropped).\n\nTests (-race): parallel join with ordered output, fail-fast with branch\nattribution, 5-item Map under MaxConcurrency=2 (observed in-flight max\nasserted), crash-point resume through the region, W7/W8 asserted over\nevery final log, fold corruption cases, Map non-array/oversize errors.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(fission-cli): workflow cancel\n\nMergePatches the fission.io/cancel-requested annotation (the spec is\nimmutable; annotations are the cancellation channel the engine watches via\nthe annotation predicate). In-flight invocations drain — no kill signal\nexists — and their late completions lose the CAS against the terminal.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): run GC — deletion finalizer and retention sweeper\n\nThe fission.io/workflow-gc finalizer guards every run deletion (kubectl,\nretention, namespace teardown): Trim reclaims every event payload, the io\nand checkpoint KV keyspaces are deleted (paginated), then the finalizer\nlifts. The deletion branch sits ABOVE the terminal fast-exit — terminal\nruns are exactly what the sweeper deletes. The retention sweeper (10min\nrunnable) enforces per-Workflow HistoryRetention (count + age) via a new\nspec.workflowRef field index (a cached List by field errors without one);\nruns that predate the finalizer get best-effort direct cleanup before\ndeletion (upgrade edge). RBAC gains the delete verb on workflowruns.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* fix(workflow): phase-3 audit — checkpoint panic, seed failures, region identity\n\nThree empirically confirmed criticals from the silent-failure audit:\n\n- Checkpoint reload of a live region panicked on nil maps (omitempty\n  serializes empty maps away; folding into the restored nil map killed the\n  whole head, deterministically, on every restart — a crash-loop only run\n  deletion could end). loadCheckpoint now normalizes the state tree\n  (BranchRuns included); a roundtrip-mid-region test pins it.\n\n- A branch failing terminally AT SEED (StartAt resolving to a Fail state,\n  or an unmatched Choice for a Map item — data-dependent and\n  admission-valid) produced no event, so fail-fast never routed: decide\n  joined the failed region and durably appended an event the fold itself\n  rejects (W7), wedging the log forever. enterRegion now routes seed-time\n  failures through the same failRegion path as event-driven fail-fast, and\n  decide joins only when every branch SUCCEEDED.\n\n- Branch events now carry a REGION IDENTITY (state@entrySeq): a sibling\n  draining out of a fail-fasted region could previously be routed into a\n  successor region reusing the same branch keys — poisoning it, or worse,\n  being silently accepted as the new region's result. The drain carve-out\n  and all CAS guards match on region, not region liveness.\n\nAlso from the audit: deterministic region-entry errors (unshapeable input,\nunparseable ItemsPath, missing branches) become terminal PendingError\ninstead of livelocking fold errors with no timeout path; appendGuarded\ndrops when the conflict walk finds a TRIMMED stream (a drained appender was\nrecreating rows in a reclaimed stream after run deletion); the retention\nsweeper deletes with a UID precondition (a stale cache listing could\ndelete a recreated, running run); cancel on a terminal run says \"already\nfinished\" instead of claiming a cancellation the engine ignores; and\nWorkflowRunStatus gains ErrorType/Cause so kubectl answers \"why did it\nfail\" without the history endpoint.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): Wait states with durable timers, OTel metrics\n\nWait pauses a run for a duration DURABLY: decide arms the same wf-timers\nQueue message backoff retries use (DedupKey-collapsed re-arms; a DLQ-lost\ntimer heals via the resync re-arm), and the fold advances through Next/End\nwhen TimerFired lands — the document passes through unchanged. Allowed in\nbranches too. The RFC's flagship synctest verification is in: a 6-hour\nwait completes in ~0ms wall-clock inside a testing/synctest bubble, engine\non the standard time package, no clock seam, no sleeps.\n\nOTel metrics per RFC-0019 house pattern, labeled by workflow/state/phase\nONLY — never a run UID (unbounded label values mint unbounded series):\nfission_workflow_runs_total, fission_workflow_step_duration_seconds,\nfission_workflow_active_runs. Step spans already come from the invoker's\notelhttp transport; full run-as-trace topology is a documented follow-up.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* test(workflow): serial resume-across-restart test + generic rollout helpers\n\nRestartDeployment/WaitForDeploymentRollout generalize the executor-only\nlifecycle helpers. The serial test parks a run in a durable 45s Wait,\nrestarts the workflow controller under it, and asserts the run completes\nWITHOUT re-executing the pre-restart step — W1 checked from the history\n(each step scheduled exactly once, the wait fired exactly once).\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* test(workflow): Wait-state validation cases; new unknown-type fixture\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* fix(workflow): accept any JSON value for run input/output; default CLI namespace\n\nLive-cluster e2e surfaced two bugs no unit test could:\n\n- WorkflowRun spec.input/status.output were runtime.RawExtension, whose\n  generated schema is type=object — the apiserver rejected a bare JSON\n  string/array (e.g. a text function's terminal output, or a Parallel\n  join's array), leaving the run stuck Running while the reconciler\n  retried the doomed terminal write forever. Both fields are now\n  apiextensionsv1.JSON (x-kubernetes-preserve-unknown-fields, no type),\n  which is the correct shape for \"any JSON value\".\n\n- `workflow run`/`history` passed GetFissionNamespace() straight through,\n  which returns \"\" when FISSION_NAMESPACE is unset — silently querying\n  nothing. fissionNamespace() now defaults to \"fission\".\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* test(workflow): e2e hardening from live kind runs\n\n- New wf-step.js fixture returning a JSON object with a hop counter:\n  the node env's strict body-parser 400s a bare-string step output, so\n  chained Task states cannot use the plain-text hello fixture — and the\n  counter proves each state saw its predecessor's output (hops:3).\n- Warm functions via the router before starting runs, so the single\n  default attempt (no retry policy) can't be spent on a router-cache\n  404 or cold-start hiccup; paths built with utils.UrlForFunction\n  (default-namespace folding).\n- ns.ID-suffixed resource names + cleanup so suites can share the\n  default namespace; run names parsed from the CLI \"started\" line\n  (warnings share stdout).\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* refactor(workflow): whole-branch simplify/deslop pass\n\nFindings from the end-of-RFC review battery (4 simplify lenses + deslop),\nverified by unit suite and a full kind e2e re-run:\n\n- v1.WorkflowBuiltinErrorTypes is now the canonical error-class list; the\n  webhook derives its typo-warning set from it. This also fixes a real\n  drift: Fission.Failed and Fission.BranchFailed were missing, so a Catch\n  on either spuriously warned \"not a built-in error class\".\n- streamNameForUID + isTerminalEvent extracted; three inline \"wfrun/\"+uid\n  sites and three copies of the terminal-event switch now share one\n  definition each.\n- RegisterTenantScopedWithPredicates delegates to the RawSources variant\n  (one builder pipeline instead of two diverging copies).\n- gc.go uses controllerutil finalizer helpers; describe.go reuses the\n  activeStates renderer; WaitForExecutorRollout delegates to the generic\n  WaitForDeploymentRollout; workflow e2e tests share waitForTerminalRun;\n  reattached the TestAsyncInvocationChart doc comment split by an\n  insert-in-place edit.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* test(workflow): serial resume test cleans up its runs too\n\nDeleting only the Workflow left succeeded runs behind on shared clusters\n(no cascade; the retention sweeper can't reach runs whose workflow is\ngone).\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* chore: mark generated paths linguist-generated\n\nCollapses pkg/generated/, zz_generated*, and crds/v1 YAML in GitHub diff\nviews so reviews focus on the sources of truth.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* fix(workflow): actually expose the head's metrics; capture its pprof in CI\n\nAnalysis of the PR's green CI run (prom-dump artifact) showed zero\nfission_workflow_* series in the TSDB: the head never served /metrics\n(the OTel meters recorded into a registry nobody scraped), the pod\ndeclared no metrics port, and no ServiceMonitor existed. Fixed all\nthree; the ServeMetrics call is wrapped in the errgroup because\nhttpserver.Serve blocks until ctx ends to own the drain — inline it\nstalls Start before crMgr.Start and the pod never goes ready (verified\nboth ways on kind: hang first, then runs_total/step_duration served\nafter the fix).\n\nAlso adds the workflow head to the CI pprof capture loop (was\nrouter+executor only) and pins ServiceMonitor + metrics containerPort\ntogether in TestWorkflowChart so they cannot drift apart.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* feat(workflow): catch resultPath + branch-state function defaulting\n\nBoth found by building real business examples against a live cluster:\n\n- ApplyDefaults never recursed into Branches, so every Parallel/Map\n  manifest that omits function.type (the documented, defaulted form)\n  failed validation at create.\n- A matched catch replaced the flowing document with the error object\n  (the SF-parity default) with no way to keep the business data — a\n  dunning flow that retries a charge after a Wait grace period lost the\n  subscription (and the past-due card \"succeeded\" on retry because the\n  charge function no longer saw it). Catch routes now take an optional\n  resultPath that merges the error into the document instead, exactly\n  like Step Functions' catcher ResultPath. RFC amended.\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* fix(statestore/client): pooled transport + response draining\n\nRunning the workflow examples on kind stalled every parallel join for\nexactly 60s: one branch's result append failed with EADDRNOTAVAIL and\nfell back to the engine's resync cadence. The client never reused\nconnections — json.Decoder stops before the trailing newline (and two\npaths closed the body unread), so every statestore call opened a fresh\nconnection (186 TIME_WAIT to :8891 at idle), and http.DefaultTransport's\nMaxIdleConnsPerHost=2 capped reuse under join concurrency anyway.\n\ndrainClose reads to EOF (bounded) before closing, and signedClient now\nuses httpx.PooledTransport(64) — the same treatment the router transports\ngot in the perf waves. Benefits every statestore consumer (engine,\nasync dispatcher, eventing).\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>\n\n* fix(statestore/httpapi): propagate CAS conflict head over the wire\n\nParallel workflow joins hung forever. Two branch StepSucceeded appends race\nthe CAS on the shared run stream; the loser gets ErrVersionConflict and must\nre-read the head to retry at the new sequence. But the HTTP statestore client\ndropped the head: `Client.Append` returned `0, err` on any error, and the wire\n`httpapi.Error` had no field to carry it. So `appendGuarded` saw head=0, walked\n0..0 (empty), never dropped, set expectedSeq=head=0, and retried Append(0)\nforever — a ~20ms tight loop that held the invoker inflight key, defeating the\nresync backstop, so the run stalled in Running until the process restarted.\n\nLinear workflows append sequentially and never conflict, so they were unaffected\nand hid the bug; commit 728e39d0's pooled transport was chasing a symptom (the\nconflict loop's connection churn manifested as EADDRNOTAVAIL), which is why it\nturned a slow-but-healing stall into a permanent one.\n\nAdd `Head` to the wire Error, have the server's eventAppend send it on a\nconflict, and have the client return it (via a conflictError that still\nIs-matches ErrVersionConflict). The interface already documents the head as\nmeaningful on a conflict; only the HTTP driver broke it.\n\nRegression guards:\n- conformance AppendCASReadTrim now asserts the conflict reports the current\n  head, covering every driver including the HTTP client (the gap that shipped).\n- new integration TestWorkflowEngineParallel — the only Parallel-on-a-real-\n  cluster test in the suite, so a join regression can't ship silently again.\n\nVerified on kind: order-pipeline completes in 5s with both branches joining in\nms (was a permanent stall), and the full example baseline (6 order routes,\nbatch-enrichment Map, dunning renew + cancel) is green.\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\n\n* test(workflow): give the Parallel integration test a run input\n\nTestWorkflowEngineParallel ran with no input, so the run document was null.\nA Parallel region seeds each branch with the marshaled document — the literal\n\"null\" — which the node env body-parser rejects (strict mode), failing both\nbranches with Fission.PermanentError → the run failed. The main flow sends an\nempty body instead, so the linear test was unaffected. Pass an object input,\nwhich is what any real workflow carries.\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\n\n* refactor(cli): group workflow-run operations under `workflow runs`\n\nThe `fission workflow` group mixed commands that act on a Workflow\n(create/update/delete/list/validate/graph/run) with commands that act on a\nWorkflowRun (runs/history/describe/cancel), and they all shared one `--name`\nflag whose help said \"Workflow name\". So `fission workflow describe --name\n<run>` advertised a workflow name but needed a run name — passing a workflow\nname failed with `workflowruns ... not found`.\n\nSplit the run operations into a `runs` subgroup so the resource is explicit:\n\n  fission workflow run  --name <workflow>        # start an execution\n  fission workflow runs list [--workflow <wf>]   # list/filter runs\n  fission workflow runs describe --name <run>\n  fission workflow runs history  --name <run> [--io]\n  fission workflow runs cancel   --name <run>\n\n- `run` (start) stays top-level: it acts on a Workflow.\n- Run subcommands take `--name` via a run-scoped descriptor whose help reads\n  \"Name of the workflow run\"; their handler code is unchanged.\n- `runs list` gains `--workflow` to scope by `spec.workflowRef`.\n- The workflow `--name` help drops the create-specific \"overrides the\n  manifest's metadata.name\" (moved to create/update's own help).\n\nNo backward-compat shim — the CLI is new in this PR. Integration/serial tests\nand the RFC doc updated to the new paths.\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\n\n* feat(cli): draw the workflow graph, and color it by what a run did\n\n`workflow graph` emitted a diagram with three gaps: it never read\nst.Branches, so a Parallel/Map state rendered as one opaque node and the\nfan-out was invisible; every state type looked identical; and it only ever\nshowed the definition, never where a given run actually got to — which is the\nquestion workflows exist to answer.\n\nDiagram content:\n- Parallel/Map branches render as a mermaid composite state, one \"--\"-separated\n  concurrent region per branch. A Map's branch is a per-item template, so it is\n  drawn once and its data-driven width rides in a note (itemsPath,\n  maxConcurrency). A Wait's delay gets a note too — the shape cannot show it.\n- Branch state ids are scoped <state>__<idx>__<name>: a branch state may reuse a\n  top-level name and mermaid ids are global.\n- Ids are sanitized and the real name kept as the label. State names may contain\n  '-' (^[A-Za-z0-9_-]{1,64}$), which mermaid will not parse as an id — so\n  payment-dunning's `grace-period` never rendered correctly.\n- classDef per state type, so shape-of-graph is readable at a glance.\n\nRendering:\n- `graph --open` serves an ephemeral local page that renders client-side.\n  Only the mermaid library is fetched; the workflow itself is inlined and drawn\n  in the user's own browser, so state names are never sent to a third-party\n  renderer (which a mermaid.ink-style image URL would do).\n\nRun overlay:\n- `workflow runs graph --name <run>` colors every state by what that run did:\n  succeeded / active / failed / unreached. Drawn against the spec snapshot in\n  the run's RunStarted event, not the live Workflow, so it stays right after the\n  definition is edited — or deleted while the run kept going.\n- A region takes its branches' worst status; the composite emits no events of\n  its own, so otherwise a region that ran would draw as never-reached.\n- Choice/Succeed/Fail keep their type color instead of going grey: they resolve\n  inside the fold and emit no events, so the log cannot say whether the run\n  passed through them, and claiming \"unreached\" would be a lie.\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\n\n* feat(cli): semantic palette, day/night viewer, Tufte pass on the graph\n\nColors and the browser view, following an author's captured mermaid style\n(mid-tone Tailwind 400/500 fills, white text) and Tufte's information-design\nprinciples.\n\nPalette:\n- States are colored by semantic role from one small palette whose fills stay\n  legible on both a white and a black canvas — so the viewer's theme toggle\n  only flips the page, never the diagram. Run-status colors are chosen not to\n  collide with the type colors that survive into a run view (Choice stays sky,\n  distinct from active's amber).\n\nViewer:\n- A day/night toggle (button + prefers-color-scheme default, remembered in\n  localStorage). Only mermaid's base theme flips; node fills are fixed.\n- The diagram is centered in a bounded column; chrome is muted and borderless\n  so the diagram is the loudest ink on the page.\n- The legend is built from exactly the classes a diagram used and sits with the\n  exhibit — a color never reaches the viewer without the word that decodes it,\n  and no swatch shows for a role not on screen.\n- A run view carries the run's claim beside the title (phase, error, elapsed),\n  so the exhibit is never orphaned from what it is evidence for.\n\nTufte pass on the diagram itself:\n- Fan-out containers are no longer colored. A region is structure — its\n  composite shape already says \"this fans out\", and its status is whatever its\n  branches show — so filling it was redundant ink competing with the branch\n  nodes inside it. It also fixes an unreadable white title on mermaid's light\n  cluster header. The branch-status roll-up that fed it is gone with it.\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\n\n* refactor(cli): collapse the graph renderer's two-pass class model\n\nA thermo-nuclear review found the renderer built a class map keyed by state\ntype during the body loop, then — in a run view — threw it away and rebuilt it\nkeyed by run status. Two competing population strategies for one map, and every\ntype-keyed append (including the ones threaded down into renderRegions) was dead\nwork in a run view.\n\nA node's class is a pure function of its type and the overlay, so assign it once\nafter the body is written:\n\n  classFor(id, type, overlay) → \"wftask\" | \"ok\" | \"unreached\" | ...\n\nThis deletes the in-loop bookkeeping, the discard-and-rebuild block, the\nallNodes slice (writeClasses sorts, so insertion order never mattered), and\ndrops renderRegions from six parameters to three.\n\nTwo more from the same review:\n- renderRegions re-implemented the top-level transition vocabulary (next, choice\n  rules, default, typed catches, terminal) and hand-inlined the terminal\n  predicate — a verbatim copy of WorkflowState.IsTerminal, whose own doc claims\n  to be the single source of truth. Extract one writeTransitions helper used at\n  both levels, and give WorkflowBranchState its own IsTerminal so \"terminal\"\n  keeps one definition and the two levels cannot drift.\n- The \"a Map is drawn as one template region\" invariant lived as bare literals\n  (`[:1]`, `\"0\"`) in the renderer and the overlay. Name it: renderedBranches()\n  and the mapTemplateBranch const. Same for the \"wf\" type-class prefix\n  (typeClassPrefix / isTypeClass), previously a scattered string literal.\n\nBehavior is unchanged — definition and run views render byte-identically\n(verified on kind); race + lint clean.\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\n\n* refactor(cli): apply /simplify — dedup helpers, reuse canonical run utils\n\nCleanup pass (reuse/simplification/efficiency), no behavior change:\n\n- One branch-node-id constructor. branchNodeID now takes the branch key as a\n  string, so the renderer (region index) and the overlay (wire Branch field)\n  build the id through the same function instead of eventNodeID re-deriving it\n  with a raw mermaidID — removing a silent coupling where coloring only landed\n  if the two id shapes happened to match.\n- WorkflowBranchState.IsTerminal delegates to ToState().IsTerminal instead of\n  copying the predicate, so \"terminal\" really is one definition (the comment\n  had promised that while the body was a verbatim copy).\n- Shared runDuration(run) in runs.go, reused by describe.go and runs_graph's\n  runElapsed — the \"finished-or-now minus started\" branch lived in two places.\n- runClaim builds its phase via the canonical runPhase (which also surfaces a\n  missing controller), dropping its own empty-phase default.\n- Delete mermaidFromSpec: a test-only shim once graph.go started calling\n  renderMermaid directly; the test calls renderMermaid too.\n- Inline the single-use topLevelID adapter; parse the constant viewer template\n  once at package level (template.Must) instead of per --open; drop a comment\n  duplicated between legendFor's doc and body.\n\nVerified on kind: definition graph, run overlay, and describe render identically;\nrace + lint clean.\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\n\n* chore(ci): bump tla2tools pin for the upstream v1.8.0 rebuild\n\nThe tlc job failed on `tla2tools.jar: FAILED` — a checksum mismatch, not a\nflake. tlaplus periodically rebuilds and re-uploads the v1.8.0 release asset\n(its manifest carries a build date), so the pinned SHA drifts; this is the\nexact recurrence hack/run-tlc.sh documents.\n\nVerified the current asset is genuine before re-pinning: downloaded from the\nofficial tlaplus/tlaplus v1.8.0 release, manifest Main-class is tlc2.TLC,\ntlc2/TLC.class is present, and Implementation-Version is \"2.0 2026-07-18\" — the\nrebuild that moved the bytes. New SHA256\ncc4803dce2a8ffaf0f5920a9dc39df4b5ee34ab4cb53fb58ac557277a7e516b3.\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>\n\n---------\n\nCo-authored-by: Claude Fable 5 <noreply@anthropic.com>",
          "timestamp": "2026-07-19T14:55:17Z",
          "url": "https://github.com/fission/fission/commit/12dbcbc686ba7813c2cfc7a3a3866dca6f4ad51f"
        },
        "date": 1784536385979,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "cold-start-poolmgr/cold_p50",
            "value": 63.141,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_p95",
            "value": 156.644,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_max",
            "value": 203.062,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr/apiserver_calls",
            "value": 225,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/cold_p50",
            "value": 2842.985,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_p95",
            "value": 3838.452,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_max",
            "value": 7100.921,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/apiserver_calls",
            "value": 718,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p50",
            "value": 132.867,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p95",
            "value": 569.991,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_max",
            "value": 813.006,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/apiserver_calls",
            "value": 539,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/burst_p50",
            "value": 2254.151,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_p95",
            "value": 4280.959,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_max",
            "value": 6319.005,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/apiserver_calls",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p50",
            "value": 3533.563,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p95",
            "value": 6429.631,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_max",
            "value": 6539.66,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/apiserver_calls",
            "value": 60,
            "unit": "count"
          },
          {
            "name": "warm-path/p50",
            "value": 19.663,
            "unit": "ms"
          },
          {
            "name": "warm-path/p95",
            "value": 56.095,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99",
            "value": 95.807,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99.9",
            "value": 204.927,
            "unit": "ms"
          },
          {
            "name": "warm-path/max",
            "value": 31653.887,
            "unit": "ms"
          },
          {
            "name": "warm-path/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path/apiserver_calls",
            "value": 453,
            "unit": "count"
          },
          {
            "name": "warm-path-newdeploy/p50",
            "value": 17.487,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p95",
            "value": 39.423,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99",
            "value": 55.103,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99.9",
            "value": 76.287,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/max",
            "value": 99.007,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/apiserver_calls",
            "value": 107,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/c10_p50",
            "value": 4.863,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p95",
            "value": 10.327,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99",
            "value": 14.895,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99.9",
            "value": 24.383,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_max",
            "value": 77.951,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c50_p50",
            "value": 21.343,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p95",
            "value": 53.439,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99",
            "value": 72.191,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99.9",
            "value": 99.007,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_max",
            "value": 139.647,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c100_p50",
            "value": 41.183,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p95",
            "value": 101.311,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99",
            "value": 139.391,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99.9",
            "value": 188.543,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_max",
            "value": 329.983,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c250_p50",
            "value": 99.711,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p95",
            "value": 261.887,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99",
            "value": 346.879,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99.9",
            "value": 463.359,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_max",
            "value": 627.199,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c500_p50",
            "value": 166.143,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p95",
            "value": 452.095,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99",
            "value": 626.687,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99.9",
            "value": 960.511,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_max",
            "value": 59801.599,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_error_rate",
            "value": 0.005107994189507775,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/specializations",
            "value": 54,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/apiserver_calls",
            "value": 395,
            "unit": "count"
          },
          {
            "name": "rps-sweep/rps100_p50",
            "value": 2.006,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p95",
            "value": 2.841,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99",
            "value": 3.851,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99.9",
            "value": 5.811,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_max",
            "value": 7.755,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps250_p50",
            "value": 1.679,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p95",
            "value": 2.339,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99",
            "value": 3.511,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99.9",
            "value": 21.551,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_max",
            "value": 55.199,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps500_p50",
            "value": 1.598,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p95",
            "value": 2.373,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99",
            "value": 3.817,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99.9",
            "value": 21.807,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_max",
            "value": 83.839,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps1000_p50",
            "value": 1.554,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p95",
            "value": 3.803,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99",
            "value": 9.519,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99.9",
            "value": 172.927,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_max",
            "value": 280.831,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/apiserver_calls",
            "value": 360,
            "unit": "count"
          },
          {
            "name": "payload-sweep/1KiB_p50",
            "value": 18.175,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p95",
            "value": 39.871,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99",
            "value": 60.447,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99.9",
            "value": 99.775,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_max",
            "value": 180.863,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/10KiB_p50",
            "value": 20.287,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p95",
            "value": 44.895,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99",
            "value": 63.807,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99.9",
            "value": 104.255,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_max",
            "value": 201.087,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/100KiB_p50",
            "value": 53.471,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p95",
            "value": 91.135,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99",
            "value": 119.231,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99.9",
            "value": 163.583,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_max",
            "value": 209.663,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/1MiB_p50",
            "value": 341.247,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p95",
            "value": 747.519,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99",
            "value": 1475.583,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99.9",
            "value": 54001.663,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_max",
            "value": 54034.431,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_error_rate",
            "value": 0.0006350550381033022,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/apiserver_calls",
            "value": 318,
            "unit": "count"
          },
          {
            "name": "autoscale-newdeploy/scale_up_seconds",
            "value": 35.15710185,
            "unit": "s"
          },
          {
            "name": "autoscale-newdeploy/apiserver_calls",
            "value": 216,
            "unit": "count"
          },
          {
            "name": "build-time-python/build_seconds",
            "value": 12.104447665,
            "unit": "s"
          },
          {
            "name": "build-time-python/apiserver_calls",
            "value": 44,
            "unit": "count"
          },
          {
            "name": "router-index-scale/create_seconds",
            "value": 7.936271247,
            "unit": "s"
          },
          {
            "name": "router-index-scale/router_rss_mb",
            "value": 84.890625,
            "unit": "MiB"
          },
          {
            "name": "router-index-scale/apiserver_calls",
            "value": 46,
            "unit": "count"
          },
          {
            "name": "route-churn/create_seconds",
            "value": 3.230014798,
            "unit": "s"
          },
          {
            "name": "route-churn/route_table_applies_total",
            "value": 257,
            "unit": "count"
          },
          {
            "name": "route-churn/apiserver_calls",
            "value": 2,
            "unit": "count"
          }
        ]
      },
      {
        "commit": {
          "author": {
            "name": "Sanket Sudake",
            "username": "sanketsudake",
            "email": "sanketsudake@gmail.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "a106aa583733511db51aad79c4d9ff0ab6af1243",
          "message": "fix(executor): key remaining executor caches by (UID, Generation) (#3609)\n\n#3596 moved the poolmgr pool cache to (UID, Generation) keys because\nResourceVersion also bumps on status-only writes (not just spec\nchanges), and informer lag across components can make an RV-keyed\nlookup miss the entry a concurrent writer just set. This extends the\nsame fix to every other executor-side cache still keyed on RV:\n\n- pkg/executor/fscache/functionServiceCache.go: the byFunction cache\n  (GetByFunction, GetByFunctionUID, Add, _touchByAddress, DeleteEntry,\n  ListOld) migrated CacheKeyUR -> CacheKeyUG.\n- pkg/executor/executortype/poolmgr/gpm.go: the functionEnv memoization\n  cache migrated the same way.\n- pkg/executor/executor.go: dispatchCreateFuncService's newdeploy/\n  container dispatcher dedup key migrated to CacheKeyUGFromMeta, so two\n  concurrent specialization requests that observed different RVs of the\n  same spec still coalesce onto one specialization.\n- pkg/executor/executortype/{container,newdeploy}mgr.go: updated stale\n  comments referencing the old RV-keyed GetByFunction.\n\nAlso included is the adopt-path fix that keying migration exposed:\nadoptSpecializedPods (the post-restart AdoptExistingResources path)\nbuilt its synthetic ObjectMeta from pod labels/annotations without\nGeneration. Under CacheKeyUG that synthetic entry keys as (UID, 0),\nwhich no live Function (Generation >= 1) can ever match, so\nRefreshFuncPods' GetByFunction -> DeleteEntry would deterministically\nmiss every adopted pod and leak stale byFunction/byAddress entries\nwith dead addresses until TTL reap. adoptSpecializedPods now reads\npod.Labels[fv1.FUNCTION_GENERATION] (stamped at specialization time)\nalongside the existing required-field reads and populates Generation;\na missing or unparsable label is treated like any other missing\nrequired field (log and skip adoption) rather than adopting at\nGeneration 0.\n\npkg/crd/key.go is untouched: CacheKeyUR and its constructors stay in\nplace for the router, which still uses them until its own (UID,\nGeneration) migration lands separately.\n\nExtracted from #3599.\n\nCo-authored-by: Claude Fable 5 <noreply@anthropic.com>",
          "timestamp": "2026-07-24T13:46:27Z",
          "url": "https://github.com/fission/fission/commit/a106aa583733511db51aad79c4d9ff0ab6af1243"
        },
        "date": 1785141405436,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "cold-start-poolmgr/cold_p50",
            "value": 84.569,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_p95",
            "value": 192.613,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_max",
            "value": 270.705,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr/apiserver_calls",
            "value": 205,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/cold_p50",
            "value": 2850.855,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_p95",
            "value": 3540.362,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_max",
            "value": 7169.067,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/apiserver_calls",
            "value": 711,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p50",
            "value": 161.279,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p95",
            "value": 224.274,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_max",
            "value": 351.12,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/apiserver_calls",
            "value": 553,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/burst_p50",
            "value": 745.676,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_p95",
            "value": 4438.578,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_max",
            "value": 5465.879,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/apiserver_calls",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p50",
            "value": 2835.828,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p95",
            "value": 5110.14,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_max",
            "value": 5914.645,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/apiserver_calls",
            "value": 80,
            "unit": "count"
          },
          {
            "name": "warm-path/p50",
            "value": 23.231,
            "unit": "ms"
          },
          {
            "name": "warm-path/p95",
            "value": 45.375,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99",
            "value": 59.839,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99.9",
            "value": 81.791,
            "unit": "ms"
          },
          {
            "name": "warm-path/max",
            "value": 169.855,
            "unit": "ms"
          },
          {
            "name": "warm-path/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path/apiserver_calls",
            "value": 373,
            "unit": "count"
          },
          {
            "name": "warm-path-newdeploy/p50",
            "value": 24.319,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p95",
            "value": 46.655,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99",
            "value": 60.223,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99.9",
            "value": 82.815,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/max",
            "value": 134.143,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/apiserver_calls",
            "value": 80,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/c10_p50",
            "value": 5.931,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p95",
            "value": 10.551,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99",
            "value": 14.199,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99.9",
            "value": 21.343,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_max",
            "value": 68.287,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c50_p50",
            "value": 23.183,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p95",
            "value": 46.975,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99",
            "value": 71.039,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99.9",
            "value": 131.199,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_max",
            "value": 218.495,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c100_p50",
            "value": 35.199,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p95",
            "value": 130.367,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99",
            "value": 383.743,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99.9",
            "value": 636.927,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_max",
            "value": 900.095,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c250_p50",
            "value": 38.815,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p95",
            "value": 994.815,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99",
            "value": 1731.583,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99.9",
            "value": 2034.687,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_max",
            "value": 2387.967,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c500_p50",
            "value": 179.071,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p95",
            "value": 1077.247,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99",
            "value": 3287.039,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99.9",
            "value": 41254.911,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_max",
            "value": 59113.471,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_error_rate",
            "value": 0.04308996417579091,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/specializations",
            "value": 88,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/apiserver_calls",
            "value": 489,
            "unit": "count"
          },
          {
            "name": "rps-sweep/rps100_p50",
            "value": 2.237,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p95",
            "value": 3.215,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99",
            "value": 4.407,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99.9",
            "value": 13.375,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_max",
            "value": 38.527,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps250_p50",
            "value": 2.069,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p95",
            "value": 2.707,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99",
            "value": 3.673,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99.9",
            "value": 65.407,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_max",
            "value": 114.303,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps500_p50",
            "value": 1.963,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p95",
            "value": 2.931,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99",
            "value": 4.363,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99.9",
            "value": 10.479,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_max",
            "value": 25.871,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps1000_p50",
            "value": 2.621,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p95",
            "value": 6.243,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99",
            "value": 14.111,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99.9",
            "value": 44.607,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_max",
            "value": 102.463,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/apiserver_calls",
            "value": 317,
            "unit": "count"
          },
          {
            "name": "payload-sweep/1KiB_p50",
            "value": 25.055,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p95",
            "value": 50.047,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99",
            "value": 80.703,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99.9",
            "value": 157.567,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_max",
            "value": 294.143,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/10KiB_p50",
            "value": 28.431,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p95",
            "value": 60.447,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99",
            "value": 92.543,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99.9",
            "value": 162.943,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_max",
            "value": 279.039,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/100KiB_p50",
            "value": 74.687,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p95",
            "value": 120.383,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99",
            "value": 158.847,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99.9",
            "value": 287.999,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_max",
            "value": 312.831,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/1MiB_p50",
            "value": 476.671,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p95",
            "value": 1985.535,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99",
            "value": 27770.879,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99.9",
            "value": 56721.407,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_max",
            "value": 59080.703,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_error_rate",
            "value": 0.004597701149425287,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/apiserver_calls",
            "value": 341,
            "unit": "count"
          },
          {
            "name": "autoscale-newdeploy/scale_up_seconds",
            "value": 40.046374996,
            "unit": "s"
          },
          {
            "name": "autoscale-newdeploy/apiserver_calls",
            "value": 249,
            "unit": "count"
          },
          {
            "name": "build-time-python/build_seconds",
            "value": 12.02501265,
            "unit": "s"
          },
          {
            "name": "build-time-python/apiserver_calls",
            "value": 45,
            "unit": "count"
          },
          {
            "name": "router-index-scale/create_seconds",
            "value": 5.9970042679999995,
            "unit": "s"
          },
          {
            "name": "router-index-scale/router_rss_mb",
            "value": 84.203125,
            "unit": "MiB"
          },
          {
            "name": "router-index-scale/apiserver_calls",
            "value": 20,
            "unit": "count"
          },
          {
            "name": "route-churn/create_seconds",
            "value": 3.332374844,
            "unit": "s"
          },
          {
            "name": "route-churn/route_table_applies_total",
            "value": 757,
            "unit": "count"
          },
          {
            "name": "route-churn/apiserver_calls",
            "value": 1501,
            "unit": "count"
          }
        ]
      },
      {
        "commit": {
          "author": {
            "name": "Sanket Sudake",
            "username": "sanketsudake",
            "email": "sanketsudake@gmail.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "f367a6992257b5eae2ea88c7b7772c772c149d3d",
          "message": "RFC-0028/0029 wave: auth-material provisioning, name inventory, spec-apply idempotency, CI flake fixes (#3649)\n\n* test(chart): pin NetworkPolicy svc selectors to real workloads\n\nThe fixed-name inventory RFC-0029 phase 2 asks for, made executable rather than\nwritten down.\n\nNetworkPolicies address workloads by a bare `svc:` pod label — a string that no\ntemplate or Go constant forces to agree with the labels the Deployments carry.\nA rename, which is exactly what phase 2 does, can orphan an allowlist entry,\nand the failure is silent: traffic is dropped rather than rejected and surfaces\nfar away as `dial tcp <ip>:8889: i/o timeout` in an unrelated integration test.\nThis repo has paid for that debugging more than once.\n\nEvery `svc:` value referenced by a policy — its own podSelector and every\ningress from-selector — must be carried by some rendered pod template. Renaming\nstatesvc's pod label fails it with the two policies that would silently drop\nthat traffic.\n\nThe render enables every optional component (canaryDeployment, workflows and\nits statestore prerequisite, functionState, mcp) on purpose: the allowlist\nlegitimately names workloads that only exist behind a flag, so a default render\nreports them as orphaned and the guard would be checking nothing.\n\n* test(rfc-0029): assert a real spec reapply is a no-op, not just its dry-run\n\nRFC-0029 §4's first `fission spec` guarantee is idempotency. The existing\ncoverage proves the PREVIEW reports no creates; this asserts the stronger\nproperty, that a real second apply writes nothing observable.\n\nThe distinction matters because the failure mode is silent rather than an\nerror. A spec apply that rewrites unchanged objects bumps the function's\nPackageRef, which recycles pods and mints an RFC-0025 version for content\nnobody edited — and a GitOps controller reapplies on every sync, so it would be\ncontinuous churn rather than a one-off. Exactly the class of bug this wave has\nalready hit twice from the CLI side.\n\nAsserts across all four surfaces a reapply could disturb: the function's\nPackageRef stamp and generation, the package's generation and recorded content\nhash, and the FunctionVersion count. It waits for the content hash to be\nrecorded first, so buildermgr's own first-sight status write cannot be\nmistaken for churn caused by the reapply, and applies twice because one repeat\ncan coincide with a settling write while a pair cannot.\n\n* test(rfc-0029): skip the spec-idempotency assertion, documenting the bug it found\n\nThe test does not pass, and it should not: it found a real defect.\n\nMeasured on CI — reapplying a byte-identical spec directory moved the\nfunction's PackageRef.ResourceVersion 4785 -> 4789 and its generation 1 -> 2.\nEvery GitOps sync of an unchanged spec therefore recycles pods and mints an\nRFC-0025 version.\n\nThe cause is in the apply engine. applyResourceType records a NO-OP package's\nCURRENT live metadata, and the cross-reference pass copies that\nResourceVersion into every referencing function — but the package's\nResourceVersion drifts on controller status writes (buildermgr records a\ncontent hash), so the copy carries drift unrelated to the spec. Same defect\nclass as the `fn update` stamp fixed in #3635, in a second code path.\n\nSkipped rather than deleted, with the measurement and the root cause in the\ntest, because the fix is not a one-liner: the spec file's function carries no\nResourceVersion, so simply not stamping leaves it empty, which also differs\nfrom live and still forces an update. The engine has to preserve the live\nfunction's stamp when the referenced package was a no-op this apply. That is\nRFC-0029 phase-4 work (#3296, #1491) and deserves its own change.\n\nKeeping a green-but-meaningless test, or a red gate on every unrelated PR,\nwould both be worse than a skip that says exactly what is broken.\n\n* refactor(auth): resolve the internal-auth Secret name in one place\n\nThe fetcher pod-spec builder and the storagesvc client each hardcoded\n\"fission-internal-auth\". RFC-0029 §10 requires both to honour\ninternalAuth.existingSecret, which points an install at a pre-created Secret —\nthe supported path for GitOps renderers, where the chart cannot preserve a\ngenerated value across a `helm template` sync.\n\nBoth now resolve through fv1.InternalAuthSecretName(), overridable by\nFISSION_INTERNAL_AUTH_SECRET_NAME. Two independent resolutions would not fail\nloudly if they disagreed: the fetcher's secretKeyRef is marked optional, so the\npod starts with the env var simply absent and every archive fetch and builder\nupload 401s, with nothing naming the Secret as the cause.\n\nNo behaviour change on its own — the default is the same name the chart\ngenerates today. This is the prerequisite that lets the chart wiring follow.\n\n* feat(chart): support internalAuth.existingSecret for the auth master\n\nRFC-0029 G3 asserts the internal-auth master can be supplied rather than\ngenerated; nothing delivered it. This does, for the half that carries no prune\nrisk: the chart stops rendering the master when the operator names their own,\nand every consumer reads theirs instead.\n\nThis is the GitOps path. Argo CD and Flux run `helm template`, where the\nchart's `lookup`-based preservation returns empty — so a generated master is\nre-minted on every sync and breaks every pod signed with the previous one.\n\nSwitching an existing install into this mode is only safe because #3636's\npre-upgrade hook stamps helm.sh/resource-policy=keep on the live Secret before\nHelm computes its deletion set. Without it Helm would prune the master it has\nstopped rendering and wedge every control-plane pod in\nCreateContainerConfigError — which is exactly how the earlier attempt at this\nfailed, before the migration existed.\n\nThe chart also now exports FISSION_INTERNAL_AUTH_SECRET_NAME, which the Go side\nreads (fv1.InternalAuthSecretName). Both halves must resolve one name and the\ndisagreement is silent: the secretKeyRef is mounted optional so rotation can\ndrop a key, which also means a reference to a non-existent Secret starts the\npod happily with the env var absent, then 401s on every archive fetch and\nbuilder upload with nothing naming the Secret. The chart test asserts the env\nvar and the secretKeyRefs agree in both modes; hardcoding either one fails it.\n\nValues docs state the operational requirement that is easy to miss: the Secret\nmust exist in EVERY namespace Fission runs pods in, because kubelet cannot\nresolve a cross-namespace secretKeyRef.\n\nGeneration stays in the template for now. Moving it into a hook Job is the\nremaining half of RFC-0029 §10 and is a separate, riskier change.\n\n* fix: address review on #3639\n\npodSelectorSvcLabels did an unguarded type assertion on each ingress `from`\nentry, so malformed chart output would panic instead of producing a readable\nassertion failure — in a test whose whole job is to report a readable failure.\n\nThe cleanup used `_ = ns.CLI(...)`, which reads as best-effort but is not:\nns.CLI fails the test on error regardless of the discarded return. Since the\ntest can fail before anything is applied, a destroy that finds nothing must not\nmask the real failure — switched to the actual best-effort helper.\n\n* fix: address review on #3640\n\nThe default-mode subtest looped over internalAuthEnvNames without asserting\nthe slice was non-empty, so it would have passed vacuously if the chart ever\nstopped wiring FISSION_INTERNAL_AUTH_SECRET anywhere — the exact\nguard-is-inert failure this test exists to prevent.\n\nRenamed chartSecret to masterSecret: with existingSecret the Secret is no\nlonger chart-owned, and the old name implied otherwise.\n\nResolve the name once per call in the storagesvc client rather than twice\n(the Get and the error message could otherwise disagree if the env changed\nbetween them).\n\n* fix(spec): make `spec apply` idempotent, and unskip the test that proved it wasn't\n\nMeasured on CI earlier in this branch: reapplying a byte-identical spec\ndirectory moved the function's PackageRef.ResourceVersion 4785 -> 4789 and its\ngeneration 1 -> 2. Every GitOps sync of an unchanged spec therefore recycled\npods and minted an RFC-0025 version.\n\nThe cross-reference pass copied each package's CURRENT ResourceVersion into\nevery referencing function. That is right only for packages this apply actually\nwrote: a package's ResourceVersion also drifts on controller STATUS writes\n(buildermgr records a content hash), so stamping unconditionally re-stamped\nfunctions whose package nobody had touched.\n\nNow keyed on what the apply really did, which the engine already tracked in\nResourceApplyStatus:\n\n  package created or updated  -> stamp the new ResourceVersion (unchanged)\n  package untouched, fn live  -> keep the stamp the function already has\n  package untouched, fn new   -> stamp the current ResourceVersion\n\nThe middle case needs the live stamp rather than simply skipping the write: a\nfunction in a spec FILE carries no ResourceVersion — it is a runtime stamp, not\nsomething a user writes — so leaving it empty would itself differ from live and\nforce the very update this avoids.\n\nSame defect class as the `fn update` stamp fixed in #3635, in the second of the\ntwo code paths that stamp.\n\nTestSpecApplyIsIdempotent is unskipped and now asserts the contract across all\nfour surfaces a reapply could disturb: the function's stamp and generation, the\npackage's generation and recorded content hash, and the FunctionVersion count.\n\n* feat(rfc-0029): generate the auth master in-cluster, not in the template\n\nCompletes RFC-0029 §10 alongside existingSecret. `internalAuth.autoGenerate`\nmoves master generation out of the chart template into the pre-install /\npre-upgrade hook, which creates it only if absent.\n\nThe template preserves a generated value across upgrades with `lookup`, and\n`lookup` returns EMPTY under `helm template` — so a GitOps renderer re-mints the\nmaster on every sync and breaks every pod signed with the previous one. An\nin-cluster create-if-absent survives any renderer. Default false, so existing\ninstalls are byte-identical.\n\nSafe only because #3636 landed: with autoGenerate on, the template stops\nrendering a Secret Helm owns, and the retention hook's resource-policy=keep is\nwhat stops Helm pruning the live value. That is precisely how the earlier\nattempt at this failed.\n\n**Tenancy decides where the master may go**, and the hook does not derive that\nitself — the namespace set comes from the chart, which already computes it in\none place. Under STATIC tenancy the master is replicated into the release\nnamespace plus defaultNamespace plus each additionalFissionNamespaces, because\nkubelet cannot resolve a cross-namespace secretKeyRef. Under DYNAMIC or CLUSTER\ntenancy it goes to the release namespace ONLY: tenant namespaces receive\ncontroller-owned derived keys and the master must never land there, which is\nthe entire isolation end state. Duplicating that rule in Go would be a second\nsource of truth for a security decision. Asserted per mode in the chart test.\n\nRBAC is a SEPARATE Role from the adoption grant, on purpose. Adoption is\npatch-only and deliberately cannot read — a grant that cannot read the HMAC\nmaster, the JWT signing key or the webhook TLS key is worth more than one that\ncan. Generation genuinely needs `get`, since it must tell \"already provisioned\"\nfrom \"absent\"; without it a re-run would mint a new master and rotate every\nderived key with no dual-accept window. Widening the adoption Role would have\nhanded that read over all three Secrets; a separate Role fenced by\nresourceNames to the master alone confines it to the one object that needs it.\nNo list, no delete.\n\nIdempotence is the property that matters, since this runs on every install AND\nevery upgrade: an existing master anywhere in the set is reused, a namespace\nthat already has it is untouched, and a concurrent starter's AlreadyExists is\nsuccess rather than a failed install. Mutation-verified — regenerating on\nre-run, and minting a per-namespace master, each fail the tests that exist for\nthem.\n\n* feat(rfc-0028): exercise skip-level (N-2 -> N) upgrades in the upgrade leg\n\nRFC-0028 phase 5. The leg built in phase 1 installs the latest published\nrelease and upgrades to HEAD; this adds a second matrix path that starts from\nN-2 instead.\n\nWorth testing precisely because it is NOT promised. RELEASES.md supports the\nlatest two minor lines, so N-2 is outside the window — but clusters defer\nupgrading until something forces it, which makes the jump from an out-of-window\nline the common real-world case. Running it on every change is how we learn\nwhether it works, rather than hearing it from an issue after someone tries.\n\nThe source version is resolved from DISTINCT appVersions, newest first: the\nchart publishes many revisions per appVersion (61 revisions across a handful of\nversions today), so counting back without deduping lands on another patch of\nthe same minor and tests nothing. Verified against the live repo — latest\nv1.27.0, N-2 resolves to v1.25.0, chart 1.25.0.\n\nA repo with fewer than three published appVersions skips the leg cleanly rather\nthan failing: there is nothing to skip from, and a red leg would say something\nuntrue about the code.\n\nArtifact names and the benchmark's fission-version label carry the matrix path,\nso the two legs' results stay distinguishable.\n\nRELEASES.md states the position plainly: exercised, not promised.\n\n* test(workflow): assert retry policy on the log, not on delivery counts\n\nTestEngineLinearPipeline fails intermittently in CI with\ncalls[fn-b] == 2 while every log invariant passes: the run is\nSucceeded, the recorded output is the first call's response, and\nassertInvariants confirms one EvStepScheduled and one result per\nattempt.\n\nThat combination is the engine behaving as designed, not a defect.\nexecute() sets X-Fission-Workflow-Run and -Attempt precisely because\ndelivery is at-least-once and functions are expected to dedup on that\npair. A redelivered attempt re-runs the side effect and its append then\nloses the result guard, which leaves the log exactly right and the\nharness call counter one high. Asserting an exact call count tests a\nproperty the design deliberately does not offer, so it can only fail\nunder the contention that makes redelivery likely -- CI, not a laptop.\nengine_test.go:364 already asserted the correct shape for the\ncrash-and-resume case; the other four sites did not.\n\nMove the exact assertions onto the log via attempts(), which counts\nrecorded EvStepScheduled per state -- W1/W2 do guarantee that -- and\nkeep the harness counter as a lower bound. The retry-budget and\n\"4xx never retries\" cases keep their exact expectations, now stated\nagainst attempts, so both still fail if the retry policy regresses\n(verified by mutation: 2->3 and 1->9 both compile and fail).\n\nAlso read the counter through callCount() under the harness mutex. It\nis written from the httptest handler goroutine, and the very\nredelivery above can still be in flight when the run reaches terminal,\nso the unsynchronised map read was a genuine race.\n\n* test(cli): close the watch-registration race in run --watch tests\n\nTestRunLocalWatchDirectoryReloadsOnChange fails in CI with \"Condition\nnever satisfied\" after the full 3s, on the second wait only: the\ninitial specialize is observed, the edit that should re-specialize\nnever lands.\n\nThe tests wait for the first specialize, then immediately write the\nsource file. But runLocal specializes inside launch() and only then\ncalls serveAndWatch, which is where addWatchTree/watcher.Add run. So\nobserving the first specialize does not mean the watch exists yet, and\na write that lands in that gap is lost for good -- inotify has no\nreplay, which is why the symptom is a full-timeout miss rather than a\nslow pass. On a laptop the gap is microseconds; on a loaded runner the\ngoroutine can be descheduled right across it.\n\nRewrite the file on every tick instead of once, via requireReSpecialize.\nThe tick stays above serveAndWatch's 250ms debounce -- a faster loop\nwould keep restarting drainBurst's quiet period so no reload would ever\nfire, trading one flake for a worse one.\n\nBoth watch tests had the identical pattern; the single-file one had\njust not lost the race yet, so both are converted. Verified by mutation\nthat they still catch a broken watcher (watchTriggers stubbed to false:\nboth fail, and pass again on revert).\n\n* fix(chart): unfence create on the authgen Role, or generation cannot run\n\nThe generation Role granted [\"get\",\"create\"] under a single\nresourceNames fence. RBAC matches resourceNames against the name in the\nrequest PATH, and a POST carries the object name in the BODY instead --\nso the authorizer sees an empty name, the rule never matches, and\ncreateIfAbsent fails Forbidden. The fence did not tighten the grant, it\nvoided it: internalAuth.autoGenerate was dead on arrival for anyone who\nturned it on.\n\nCI never caught this because CI supplies internalAuth.secret directly\nand so never exercises the generation path.\n\nSplit the rule. `get` keeps its fence and still reaches the master and\nnothing else -- that read is what distinguishes \"already provisioned\"\nfrom \"absent\", without which a re-run would mint a new master and\nrotate every derived key with no dual-accept window. `create` drops the\nfence because RBAC offers no way to keep it.\n\nThis is the exact inverse of the CRD ClusterRole next door, which DOES\nkeep resourceNames on create: server-side apply PATCHes a named path,\nso the create authorization is evaluated against that name. Switching\ngeneration to apply would recover the fence and is deliberately not\ndone -- apply takes ownership of .data and would rotate the master,\nthe one thing this hook must never do.\n\nThe residual grant is \"create A Secret in this one namespace\": it\ncannot read, patch or delete any existing Secret, create fails\nAlreadyExists rather than overwriting the master, and the hook\nServiceAccount is deleted with the hook.\n\nThe chart guard asserted every rule carried exactly one resourceName --\nit encoded the bug, so it could never have caught it. It now pins both\nhalves separately, and fails on the old fenced form (verified).\n\n* ci(upgrade): tolerate the missing results file on a skipped leg\n\nSKIP_LEG=1 (fewer than three published appVersions, so there is no N-2\nto skip-level from) exits before writing upgrade-results.json, but the\nupload step runs under always(). upload-artifact defaults to\nif-no-files-found: warn, so a leg that is designed to skip cleanly\nwould annotate every such run with a red herring. Ignore instead.\n\n* fix: address review battery findings on the internal-auth wave\n\nFive review lenses (code review, security, simplify, quality audit,\ndeslop) ran over the consolidated branch. Fixing what they found.\n\nCorrectness:\n\n- spec apply preserved a live stamp using only the function's identity,\n  so a spec that repoints a function from pkgA to an UNTOUCHED pkgB\n  wrote pkgB's name with pkgA's ResourceVersion. It never converged:\n  the next apply re-preserved the same wrong value, and package\n  restamping only fires when pkgB itself is written. liveFunctionStamps\n  now returns the whole PackageRef and the stamp is kept only when the\n  live reference names the same package. This is the same re-point vs\n  refresh distinction the `fn update --pkg` path already makes.\n\n- The generation hook swallowed AlreadyExists and kept writing its OWN\n  master into the remaining namespaces. Two hook Jobs racing therefore\n  diverged permanently -- one namespace on each master, no error -- and\n  the symptom is pods signing with a key the control plane will not\n  verify. It now re-reads on a lost create and adopts the winner,\n  retrying the whole pass; a genuinely irreconcilable split fails loudly\n  rather than half-provisioning.\n\n- A Secret that exists but carries no usable key (aborted rotation, or\n  an ExternalSecret placeholder) was skipped by existingMaster and then\n  left untouched by the create, so that namespace stayed keyless while\n  every other one got a new master and the hook exited 0. Now a hard\n  error naming the key.\n\n- autoGenerate with preUpgradeChecks.enabled=false rendered NO master at\n  all: the template stops rendering and the Job that would provision it\n  does not exist. Every control-plane pod wedges on a required\n  secretKeyRef. The chart now refuses to render, mirroring the existing\n  crds.mode=hook guard one condition above it.\n\nHonesty:\n\n- The managed-by label never made the hook-created Secret Helm-adoptable\n  -- Helm also requires the meta.helm.sh/release-{name,namespace}\n  annotations, which the hook cannot know. The comment claimed it did.\n  Corrected to state the real consequence instead of implying a rotation\n  path that aborts the upgrade.\n\n- authMasterBytes claimed to exceed \"the minimum the signer enforces\".\n  There is no minimum; keys.go only rejects a zero-length master.\n\nDuplication (the reason this branch was consolidated):\n\n- The 4-term provisioning predicate was open-coded in three templates,\n  one as a hand-written De Morgan negation. Both drift directions fail\n  silently: two masters racing, or none at all. Now one named helper,\n  verified render-identical across 10 value combinations.\n- countMasterSecrets re-implemented the existing countByKindName;\n  jobNamesForDefault was a pure pass-through; a subtest asserted\n  NotEmpty on a value an earlier subtest already pinned to a non-empty\n  constant; one rationale paragraph was copied into six files.\n\nNew guards are mutation-verified: each fails on the defect it targets\nand passes on the fix.\n\n* fix(chart): keep the master in the retention set when existingSecret adopts it\n\nfission.adoptSecretNames skipped the internal-auth Secret whenever\ninternalAuth.existingSecret was set. That guard reads as \"the operator\nowns it, so retention is not ours to do\" -- but retention has to cover\nthe object Helm PREVIOUSLY managed, and setting existingSecret is\nexactly what removes that object from the rendered manifest.\n\nSo the most natural adoption there is destroys the master: an operator\nwith a chart-generated master sets\n\n    internalAuth.existingSecret: fission-internal-auth\n\nto take ownership of the Secret that already exists. The template stops\nrendering it, nothing stamps helm.sh/resource-policy=keep, and Helm\nprunes a Secret still in use -- every control-plane pod wedges on a\nrequired secretKeyRef at next restart, and function pods run unsigned.\nvalues.yaml promised the opposite (\"switching an existing install to\nthis is safe: the retention hook stamps keep\").\n\nName the chart-generated Secret unconditionally instead. Stamping keep\non a Secret Helm no longer manages is harmless, and so is naming one\nthat does not exist -- the hook already logs \"no live secret to adopt\"\nand moves on -- so the unconditional form is strictly safer. An\noperator-supplied Secret under a different name is still never added:\nHelm never managed it, so it was never at risk.\n\nGuarded by a test over all three shapes, verified to fail on the old\ncondition.\n\n* test(helm): factor the chart-render walkers, and pin why two of them differ\n\nSecond simplification pass over test/helm/portdrift_test.go.\n\nThe load-bearing change is NOT the line count. Two earlier review passes\nboth recommended collapsing npAllowsFromSvc into podSelectorSvcLabels\non the grounds that the latter \"strictly generalises\" it. It does not:\npodSelectorSvcLabels also includes spec.podSelector, the workload a\npolicy PROTECTS rather than one it admits. The router policy targets\n`svc: router`, so the collapsed form reports every policy as admitting\nits own target, and TestAsyncInvocationChart's default-off assertion\n(the router is NOT in the async allowlist) silently goes true. The\ntempting follow-up -- relax that assertion -- would make the whole\nallowlist guard inert.\n\nSo the shared part is now named for what it actually is:\ningressSvcLabels covers only what the ingress rules ADMIT,\nnpAllowsFromSvc is a Contains over it, and podSelectorSvcLabels is\ningressSvcLabels plus the target. The comment states the trap, and a\nmutation that folds the target back in fails TestAsyncInvocationChart,\nso the next reader who tries the collapse is told why it is wrong.\n\nThat also removes a latent panic: the old npAllowsFromSvc used a\nsingle-value type assertion on a `from` entry, which would panic on\nmalformed chart output -- the exact case podSelectorSvcLabels had added\na comment to guard against.\n\nMechanical alongside it: podTemplates extracted as the doc -> spec ->\ntemplate primitive both new walkers were repeating; verbsOf generalised\nto stringsOf and reused in verbsFor (which hand-inlined the same\nconversion three times); the authgen-grant assertions convert verbs\nonce instead of twice; and the existingSecret table now takes render\nargs like the retention table added one commit earlier, dropping an\nempty-string sentinel and its if/else.\n\nDeliberately NOT unified: containerArgs uses fmt.Sprint rather than a\nstring assertion because sigs.k8s.io/yaml decodes an unquoted numeric\narg as float64 -- which is why containerPorts asserts float64 two lines\naway. Folding it into a string-asserting helper would turn every\nnumeric arg into \"\" and make argAfter comparisons pass vacuously.\n\n* fix(preupgrade): reject a keyless master before writing anything, not after\n\nRe-review of the previous fix commit found the keyless-Secret guard was\nin the wrong place. createIfAbsent raised it, which is the create that\ntrips over the bad Secret -- by which point a freshly minted master has\nalready been created in every namespace scanned ahead of it.\n\nThat left the cluster changed by a run that then exited 1, and this\nidentity holds neither patch nor delete on the master, so it could never\ntake it back. Worse, the error told the operator to populate the keyless\ncopy: doing that produces two different masters, and every later run\nreports an irreconcilable split forever. The advice walked them into a\nharder hole than they started in.\n\nMove the check into existingMaster. That scan holds `get` on every\nnamespace and completes before the first create, so refusing there\nleaves the cluster untouched -- the failure is atomic and the operator\nstill has exactly the one Secret they need to fix. The error now also\nnames internalAuth.existingSecret, which is the actual answer when the\nkeyless Secret is an external-secret-manager placeholder.\n\nThe createIfAbsent check stays for the narrow case it can still catch: a\nkeyless Secret appearing BETWEEN the scan and the create. That one\ncannot be atomic, and the message says so rather than implying the\ncluster is clean.\n\nPinned by a test asserting no namespace was written, verified to fail\nunder the old ordering.\n\nAlso corrects three comments the re-review found stating things that are\nno longer true -- these are load-bearing, because each one invites a\nmaintainer to \"fix\" the contradiction by reverting a real fix:\n\n- fission.adoptSecretNames said \"never one supplied via an existingSecret\n  value\", which is exactly what the internalAuth entry now does. The\n  test is PRUNABILITY, not ownership: internal-auth-secret.yaml and the\n  webhook cert are ordinary manifest resources and prune; router/secret\n  .yaml carries helm.sh/hook, so it sits outside Helm's deletion set and\n  its guard is correct. The asymmetry looks like an oversight and is not.\n- The adopt Role said \"fenced to exactly the Secrets the chart\n  generates\". It is fenced to the chart's NAMES, and with\n  existingSecret: fission-internal-auth that means patch on an\n  operator-owned object -- intended, since it is the only way to stop\n  Helm pruning it, but previously an unstated side effect.\n- The unfenced create said it \"cannot read any existing Secret\". It\n  composes with the fenced `get` in the same Role into a\n  ServiceAccount-token read, and under static tenancy renders into the\n  function namespaces as well. Documented honestly, with the VAP that\n  actually closes it named as follow-up.",
          "timestamp": "2026-08-03T07:31:12Z",
          "url": "https://github.com/fission/fission/commit/f367a6992257b5eae2ea88c7b7772c772c149d3d"
        },
        "date": 1785747527964,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "cold-start-poolmgr/cold_p50",
            "value": 58.241,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_p95",
            "value": 137.758,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_max",
            "value": 148.486,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr/apiserver_calls",
            "value": 84,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/cold_p50",
            "value": 2839.343,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_p95",
            "value": 2864.827,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_max",
            "value": 6311.213,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/apiserver_calls",
            "value": 893,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p50",
            "value": 110.297,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p95",
            "value": 233.861,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_max",
            "value": 1221.864,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/apiserver_calls",
            "value": 65,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/burst_p50",
            "value": 2448.097,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_p95",
            "value": 4587.509,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_max",
            "value": 5486.776,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/apiserver_calls",
            "value": 539,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p50",
            "value": 2967.57,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p95",
            "value": 5005.927,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_max",
            "value": 6008.107,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/apiserver_calls",
            "value": 74,
            "unit": "count"
          },
          {
            "name": "warm-path/p50",
            "value": 14.983,
            "unit": "ms"
          },
          {
            "name": "warm-path/p95",
            "value": 29.775,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99",
            "value": 38.431,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99.9",
            "value": 56.799,
            "unit": "ms"
          },
          {
            "name": "warm-path/max",
            "value": 116.735,
            "unit": "ms"
          },
          {
            "name": "warm-path/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path/apiserver_calls",
            "value": 235,
            "unit": "count"
          },
          {
            "name": "warm-path-newdeploy/p50",
            "value": 16.463,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p95",
            "value": 34.527,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99",
            "value": 47.007,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99.9",
            "value": 66.559,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/max",
            "value": 104.191,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/apiserver_calls",
            "value": 91,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/c10_p50",
            "value": 4.005,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p95",
            "value": 7.063,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99",
            "value": 9.391,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99.9",
            "value": 13.887,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_max",
            "value": 75.199,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c50_p50",
            "value": 16.479,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p95",
            "value": 32.479,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99",
            "value": 48.543,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99.9",
            "value": 102.719,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_max",
            "value": 215.423,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c100_p50",
            "value": 24.751,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p95",
            "value": 70.463,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99",
            "value": 356.351,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99.9",
            "value": 593.919,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_max",
            "value": 976.895,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c250_p50",
            "value": 25.407,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p95",
            "value": 675.839,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99",
            "value": 1495.039,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99.9",
            "value": 2050.047,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_max",
            "value": 2596.863,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c500_p50",
            "value": 100.223,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p95",
            "value": 643.071,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99",
            "value": 1508.351,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99.9",
            "value": 36896.767,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_max",
            "value": 57311.231,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_error_rate",
            "value": 0.03352692713833157,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/specializations",
            "value": 88,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/apiserver_calls",
            "value": 467,
            "unit": "count"
          },
          {
            "name": "rps-sweep/rps100_p50",
            "value": 1.874,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p95",
            "value": 2.511,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99",
            "value": 3.725,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99.9",
            "value": 22.047,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_max",
            "value": 66.175,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps250_p50",
            "value": 1.817,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p95",
            "value": 2.333,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99",
            "value": 3.189,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99.9",
            "value": 18.719,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_max",
            "value": 70.527,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps500_p50",
            "value": 1.711,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p95",
            "value": 2.369,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99",
            "value": 3.473,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99.9",
            "value": 6.811,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_max",
            "value": 12.279,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps1000_p50",
            "value": 1.676,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p95",
            "value": 3.135,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99",
            "value": 7.087,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99.9",
            "value": 152.703,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_max",
            "value": 322.559,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/apiserver_calls",
            "value": 313,
            "unit": "count"
          },
          {
            "name": "payload-sweep/1KiB_p50",
            "value": 17.967,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p95",
            "value": 35.039,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99",
            "value": 51.807,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99.9",
            "value": 114.559,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_max",
            "value": 214.143,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/10KiB_p50",
            "value": 19.615,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p95",
            "value": 39.391,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99",
            "value": 63.839,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99.9",
            "value": 132.223,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_max",
            "value": 205.951,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/100KiB_p50",
            "value": 44.255,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p95",
            "value": 85.375,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99",
            "value": 118.143,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99.9",
            "value": 164.991,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_max",
            "value": 221.055,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/1MiB_p50",
            "value": 302.591,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p95",
            "value": 886.783,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99",
            "value": 5017.599,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99.9",
            "value": 53641.215,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_max",
            "value": 53936.127,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_error_rate",
            "value": 0.00020362451639177357,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/apiserver_calls",
            "value": 332,
            "unit": "count"
          },
          {
            "name": "autoscale-newdeploy/scale_up_seconds",
            "value": 35.040372936,
            "unit": "s"
          },
          {
            "name": "autoscale-newdeploy/apiserver_calls",
            "value": 241,
            "unit": "count"
          },
          {
            "name": "build-time-python/build_seconds",
            "value": 12.025978769,
            "unit": "s"
          },
          {
            "name": "build-time-python/apiserver_calls",
            "value": 52,
            "unit": "count"
          },
          {
            "name": "router-index-scale/create_seconds",
            "value": 4.341042816,
            "unit": "s"
          },
          {
            "name": "router-index-scale/router_rss_mb",
            "value": 80.5625,
            "unit": "MiB"
          },
          {
            "name": "router-index-scale/apiserver_calls",
            "value": 1,
            "unit": "count"
          },
          {
            "name": "route-churn/create_seconds",
            "value": 2.469359157,
            "unit": "s"
          },
          {
            "name": "route-churn/route_table_applies_total",
            "value": 1267,
            "unit": "count"
          },
          {
            "name": "route-churn/apiserver_calls",
            "value": 1500,
            "unit": "count"
          }
        ]
      },
      {
        "commit": {
          "author": {
            "name": "Sanket Sudake",
            "username": "sanketsudake",
            "email": "sanketsudake@gmail.com"
          },
          "committer": {
            "name": "GitHub",
            "username": "web-flow",
            "email": "noreply@github.com"
          },
          "id": "87a49300c294976e9275bc288d10c33b372ffdbf",
          "message": "timer: phase-anchor @every schedules to the trigger, not to process start (#2660) (#3698)\n\nrobfig/cron's ConstantDelaySchedule — what \"@every 6h\" parses to — computes\nNext(t) as t + delay, and newCron starts the cron when the trigger is registered.\nSo every @every trigger's phase is set by whenever the timer process last started,\nwhich has two consequences.\n\nThe reported one: every @every trigger registered at process start fires at\nstart+d, +2d, …, so a restart collapses independently-authored schedules into a\nsingle thundering herd. The issue states the expectation well — \"the schedule is\nrelative to when the function was deployed\" — and that is exactly what was not\ntrue.\n\nThe unreported one is worse. The phase resets on EVERY restart, so a timer pod\nthat restarts more often than the interval starves the trigger completely: an\n\"@every 24h\" trigger under a daily rollout never fires at all, and node churn can\ndo the same to \"@every 6h\". Nothing carried phase across a restart.\n\nAnchoring to the trigger's own CreationTimestamp fixes both. Firing times become a\nproperty of the trigger — the smallest creation + k*interval after now — so they\nsurvive restarts and, because creation times differ, spread out on their own.\nTestAnchoredScheduleSurvivesRestart asserts the STOCK schedule fires zero times\nacross a restart cycle that outpaces the interval, so the bug is pinned against\nthe real library rather than only the fix being asserted.\n\nThis is the issue's own option 2 and needs no API change. Its option 1, a\nconfigurable offset field, would need a CRD change and would still leave the\nstarvation, since the phase would keep resetting.\n\nOnly fixed-interval schedules are wrapped. Standard cron expressions and the\n@daily/@hourly descriptors are already wall-clock anchored, so wrapping them would\nchange what they mean. A trigger with no CreationTimestamp — a hand-built object\nthat never round-tripped through the API server — keeps the stock schedule, as\nthere is nothing stable to anchor to.\n\nTwo edge cases the arithmetic has to exclude. k is at least 1 even when the timer\nasks about a time before the anchor: CreationTimestamp is server-set, so a pod\nwhose clock trails the API server sees a trigger created in its own future, and\nfiring at the anchor itself would fire it seconds after creation rather than an\ninterval later. And time.Sub saturates at ~292 years rather than wrapping, so a\nfar-past anchor would otherwise overflow the multiply into a time in the PAST,\nwhich cron fires immediately and then recomputes forever.\n\nRegistration now parses the spec itself rather than discarding AddFunc's error,\nand that error propagates through addUpdate. This matters more than it looks:\nthe reconciler stamps Scheduled/Ready=True on whatever addUpdate leaves behind, so\nan error absorbed here would report a trigger as firing on schedule while it has\nno cron entry at all. It now reports Scheduled=False/InvalidCron, the same way a\nspec rejected up front does. There is no TimeTrigger admission webhook — the\nreconciler's IsValidCronSpec is the gate, and this branch is belt and braces\nagainst that parser and this one drifting apart.",
          "timestamp": "2026-08-24T07:40:03Z",
          "url": "https://github.com/fission/fission/commit/87a49300c294976e9275bc288d10c33b372ffdbf"
        },
        "date": 1787559786922,
        "tool": "customSmallerIsBetter",
        "benches": [
          {
            "name": "cold-start-poolmgr/cold_p50",
            "value": 57.19,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_p95",
            "value": 458.874,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/cold_max",
            "value": 613.876,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr/apiserver_calls",
            "value": 179,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/cold_p50",
            "value": 2838.517,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_p95",
            "value": 2855.701,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/cold_max",
            "value": 4605.071,
            "unit": "ms"
          },
          {
            "name": "cold-start-newdeploy/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-newdeploy/apiserver_calls",
            "value": 733,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p50",
            "value": 99.11,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_p95",
            "value": 170.142,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/cold_max",
            "value": 213.262,
            "unit": "ms"
          },
          {
            "name": "cold-start-poolmgr-configdeps/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-start-poolmgr-configdeps/apiserver_calls",
            "value": 378,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/burst_p50",
            "value": 2569.087,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_p95",
            "value": 3524.322,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/burst_max",
            "value": 5576.576,
            "unit": "ms"
          },
          {
            "name": "cold-burst-same-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-same-fn/apiserver_calls",
            "value": 166,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p50",
            "value": 2031.943,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_p95",
            "value": 4096.049,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/burst_max",
            "value": 6118.402,
            "unit": "ms"
          },
          {
            "name": "cold-burst-distinct-fn/failures",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "cold-burst-distinct-fn/apiserver_calls",
            "value": 0,
            "unit": "count"
          },
          {
            "name": "warm-path/p50",
            "value": 11.663,
            "unit": "ms"
          },
          {
            "name": "warm-path/p95",
            "value": 24.159,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99",
            "value": 32.015,
            "unit": "ms"
          },
          {
            "name": "warm-path/p99.9",
            "value": 48.991,
            "unit": "ms"
          },
          {
            "name": "warm-path/max",
            "value": 87.039,
            "unit": "ms"
          },
          {
            "name": "warm-path/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path/apiserver_calls",
            "value": 483,
            "unit": "count"
          },
          {
            "name": "warm-path-newdeploy/p50",
            "value": 13.047,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p95",
            "value": 29.663,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99",
            "value": 41.279,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/p99.9",
            "value": 58.207,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/max",
            "value": 102.591,
            "unit": "ms"
          },
          {
            "name": "warm-path-newdeploy/error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "warm-path-newdeploy/apiserver_calls",
            "value": 73,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/c10_p50",
            "value": 3.227,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p95",
            "value": 5.875,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99",
            "value": 8.039,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_p99.9",
            "value": 12.879,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_max",
            "value": 73.919,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c10_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c50_p50",
            "value": 12.367,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p95",
            "value": 26.079,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99",
            "value": 38.783,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_p99.9",
            "value": 79.423,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_max",
            "value": 129.599,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c50_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c100_p50",
            "value": 18.943,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p95",
            "value": 75.263,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99",
            "value": 199.167,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_p99.9",
            "value": 321.023,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_max",
            "value": 523.775,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c250_p50",
            "value": 42.559,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p95",
            "value": 215.167,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99",
            "value": 521.727,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_p99.9",
            "value": 14573.567,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_max",
            "value": 59506.687,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c250_error_rate",
            "value": 0.002720234358652438,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/c500_p50",
            "value": 180.095,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p95",
            "value": 791.551,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99",
            "value": 1474.559,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_p99.9",
            "value": 14131.199,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_max",
            "value": 58982.399,
            "unit": "ms"
          },
          {
            "name": "concurrency-sweep/c500_error_rate",
            "value": 0.009967075055434855,
            "unit": "ratio"
          },
          {
            "name": "concurrency-sweep/specializations",
            "value": 126,
            "unit": "count"
          },
          {
            "name": "concurrency-sweep/apiserver_calls",
            "value": 528,
            "unit": "count"
          },
          {
            "name": "rps-sweep/rps100_p50",
            "value": 2.046,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p95",
            "value": 3.087,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99",
            "value": 4.939,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_p99.9",
            "value": 17.215,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_max",
            "value": 47.807,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps100_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps250_p50",
            "value": 9.919,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p95",
            "value": 7868.415,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99",
            "value": 11419.647,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_p99.9",
            "value": 54132.735,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_max",
            "value": 60030.975,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps250_error_rate",
            "value": 0.2610363391655451,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps500_p50",
            "value": 1.821,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p95",
            "value": 2.753,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99",
            "value": 5.183,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_p99.9",
            "value": 103.103,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_max",
            "value": 150.655,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps500_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/rps1000_p50",
            "value": 1.726,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p95",
            "value": 4.127,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99",
            "value": 14.447,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_p99.9",
            "value": 168.447,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_max",
            "value": 372.991,
            "unit": "ms"
          },
          {
            "name": "rps-sweep/rps1000_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "rps-sweep/apiserver_calls",
            "value": 522,
            "unit": "count"
          },
          {
            "name": "payload-sweep/1KiB_p50",
            "value": 14.503,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p95",
            "value": 30.383,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99",
            "value": 57.119,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_p99.9",
            "value": 106.559,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_max",
            "value": 182.143,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/10KiB_p50",
            "value": 21.967,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p95",
            "value": 82.495,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99",
            "value": 183.679,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_p99.9",
            "value": 1615.871,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_max",
            "value": 36306.943,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/10KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/100KiB_p50",
            "value": 44.127,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p95",
            "value": 92.799,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99",
            "value": 124.735,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_p99.9",
            "value": 223.231,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_max",
            "value": 342.527,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/100KiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/1MiB_p50",
            "value": 210.047,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p95",
            "value": 291.071,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99",
            "value": 357.375,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_p99.9",
            "value": 446.463,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_max",
            "value": 604.159,
            "unit": "ms"
          },
          {
            "name": "payload-sweep/1MiB_error_rate",
            "value": 0,
            "unit": "ratio"
          },
          {
            "name": "payload-sweep/apiserver_calls",
            "value": 390,
            "unit": "count"
          },
          {
            "name": "autoscale-newdeploy/scale_up_seconds",
            "value": 35.046206898,
            "unit": "s"
          },
          {
            "name": "autoscale-newdeploy/apiserver_calls",
            "value": 199,
            "unit": "count"
          },
          {
            "name": "build-time-python/build_seconds",
            "value": 12.035335166,
            "unit": "s"
          },
          {
            "name": "build-time-python/apiserver_calls",
            "value": 12,
            "unit": "count"
          },
          {
            "name": "router-index-scale/create_seconds",
            "value": 4.300806191,
            "unit": "s"
          },
          {
            "name": "router-index-scale/router_rss_mb",
            "value": 104.75,
            "unit": "MiB"
          },
          {
            "name": "router-index-scale/apiserver_calls",
            "value": 57,
            "unit": "count"
          },
          {
            "name": "route-churn/create_seconds",
            "value": 2.30915414,
            "unit": "s"
          },
          {
            "name": "route-churn/route_table_applies_total",
            "value": 255,
            "unit": "count"
          },
          {
            "name": "route-churn/apiserver_calls",
            "value": 28,
            "unit": "count"
          }
        ]
      }
    ]
  }
}
window.BENCHMARK_DATA = {
  "lastUpdate": 1784536386873,
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
      }
    ]
  }
}
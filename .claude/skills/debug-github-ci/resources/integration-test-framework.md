# Go integration-test framework — quirks the bash→Go migration uncovered

The `test/integration/framework/` package was built during the 2026-04/05 bash→Go migration. Across that work the framework was iterated dozens of times to close races and capture gotchas the bash suite had been silently ignoring or working around. This file is the distilled "things that made tests flake" list — read it before assuming a flake is random.

Authoritative reference: `docs/test-migration/02-framework-api.md`. This file is the "what burned us" companion.

## The builder ↔ runtime ↔ Service readiness race (top source of flakes)

Symptom: `dial tcp <svc>:8000: i/o timeout` from buildermgr → fetcher when a test calls `CreatePackage(Src=...)` shortly after `CreateEnv(Builder=...)`. Layered fix shipped over six iterations:

1. **Wait for builder pod Ready** (`WaitForBuilderReady`, by `envName=<env>` label).
2. **Also wait for runtime pool pod Ready** — buildermgr's POST goes through a Service that round-robins, so even one not-Ready pool pod causes timeouts.
3. **Wait for ALL pool pods** (`waitForRuntimePoolReady`) — the Service round-robins; one Ready pod isn't enough when the Service points at three.
4. **Wait for the env Service to publish endpoints** — `Pod.Status.Conditions[Ready]=True` fires before kube-proxy programs iptables. The build POST can land on a stale endpoint.
5. **Use EndpointSlices via service-name prefix, not Endpoints by name** — the env Service has a port-hash suffix (`<env>-<rv>-<hash>`); listing Endpoints by exact name returns 404. EndpointSlices carry a `kubernetes.io/service-name` label that supports prefix matching.
6. **Target the *builder* Service, not the runtime pool** — buildermgr POSTs to the Service named `<env>-<env.RV>` (selecting `envName=<env>`), not the runtime pool (which selects `environmentName=<env>`). The earlier "wait for runtime pool" was load-bearing on a misunderstanding; the real race is on the builder Service. Reference: `pkg/buildermgr/common.go:57`.
7. **5-second settle after EndpointSlice readiness** — even after pod Ready + endpoint published, the fetcher process can take a beat to bind port 8000. K8s 1.28 needed 15s in CI (commit `9bc0c573`).
8. **Retry on transient build failure** — final safety net. `WaitForPackageBuildSucceeded` retries up to 3× on the known transient signature `dial tcp …:8000: i/o timeout`, resetting `Status.BuildStatus → pending` to re-trigger the buildermgr's UpdateFunc.

Folded into `CreateEnv` automatically when `EnvOptions.Builder` is set, so test authors don't have to think about this — but if a test file *bypasses* `CreateEnv` and directly creates Environments via `f.FissionClient()`, this race is back.

## Test-cadence vs production-cadence (why we didn't fix Fission proper)

In production, builds happen well after env creation: `env create` → user push to repo → CI build kicks off. Kube-proxy has long since propagated. The race is purely a test-cadence artefact (env create → immediate build). Fixing in the framework is correct; "fixing" Fission would just add complexity to a path that doesn't bite real users.

## Pod label conventions — why selectors silently miss

| Pod | Labels | Where set |
|---|---|---|
| **Per-env builder pod** (env builder + fetcher sidecar) | `envName=<env>, envNamespace=<ns>, envResourceVersion=<rv>, owner=buildermgr` | `pkg/buildermgr/envwatcher.go` `getLabels()` |
| **Runtime pool pod** (poolmgr) | `environmentName=<env>, functionName=<fn>, executorType=poolmgr, managed=<bool>, executorInstanceId=<id>, environmentNamespace=<ns>, environmentUid=<uid>` | `pkg/apis/core/v1/const.go` (label keys) |
| **Newdeploy function pod** | Same as above with `executorType=newdeploy` | same |
| **Container-executor function pod** | Same with `executorType=container` | same |

Pitfalls:
- A selector `envName=<env>` matches **builder pods**, NOT runtime pods. Runtime pods carry `environmentName=` (longer key).
- `WaitForBuilderReady` uses `envName=`. `WaitForRuntimePodReady` uses `environmentName=`. Mixing them up was a recurring bug during migration.
- For function-specific log reading, use `functionName=<fn>` directly — narrower than env label and exactly the specialised pod. See `ns.FunctionLogs()` in `framework/function.go`.

## `ns.CLI` captures cobra writers, not `os.Stdout`

The in-process Fission CLI sets cobra's `Out`/`Err` to a `bytes.Buffer` that `ns.CLI` returns. But these subcommands print to `os.Stdout` directly:

- `fission function logs` (kubectl-style log streaming)
- `fission archive list / download / get-url / delete`

For those, use `ns.CLICaptureStdout` (or the cleanup-friendly `CLICaptureStdoutBestEffort`). It takes the process-global `cliMu` write lock to serialize against any concurrent `ns.CLI` call — cross-test `os.Stdout` capture under `t.Parallel()` would race. Don't reach for `os.Stdout` redirection in your test code; it'll race with sibling tests.

For env-var-driven CLI flags (e.g. `FISSION_DEFAULT_NAMESPACE`), use `ns.CLIWithEnv(t, ctx, env, args...)` — same `cliMu` write-lock idea, restoring env on return.

## `embed.FS` skips nested Go modules

Symptom: a file under `test/integration/testdata/<lang>/<feature>/` exists on disk but `WriteTestData(t, "<lang>/<feature>/<file>")` fails with "file does not exist".

Cause: `//go:embed` silently excludes any directory that contains its own `go.mod` — Go treats it as a separate module. `testdata/go/module_example/` had this problem.

Workaround: store as `.txt` on disk, strip the `.txt` suffix when materializing. `TestGoEnv`'s `zipModuleExample` is the reference implementation.

Also: `embed.FS` skips files starting with `_` or `.`. Don't store fixtures under `_internal` or `.hidden` paths.

## Spec tests need `WithCWD`

`fission spec init/apply/destroy` and `env create --spec`, `fn create --spec` write specs to `./specs` relative to `os.Getwd()` — there's no `--specdir` override on `env create` / `fn create` (only on `spec apply`).

The framework helper `ns.WithCWD(t, workdir, func(){ … })` chdirs under a process-global `cwdMu`, runs the closure, restores. Other concurrent tests are unaffected as long as they pass absolute paths to the CLI (every framework helper does — verified during migration).

Also relevant for `fn create --deploy "<glob>"` — globs expand against cwd.

## Vendored spec YAMLs need rewriting per-test

Bash spec tests had hardcoded names like `spec-merge-9f3a` baked into the YAMLs. Multiple parallel Go tests using the same fixture trip over each other. `framework.MaterializeSpecs(t, embedDir, replacements, workdir)` walks the embedded tree, applies a longest-old-string-first replacer, writes to `workdir`.

Pair with `framework.NewSpecUID(t)` for the `DeploymentConfig.uid` field — `spec destroy` selects by uid, so per-test UIDs ensure cleanup only removes that test's resources.

## Package CR has no `/status` subresource yet

`pkg/buildermgr/pkgwatcher.go:259-262` only re-builds when `Package.Status.BuildStatus == "pending"`. Bash side-stepped this by using `kubectl replace`, which round-trips Status. Go clientset `Update()` overwrites Status — so a test that updates `Spec.Source.URL` and expects a rebuild **must explicitly set** `Status.BuildStatus = fv1.BuildStatusPending` along with the spec change.

Once the `/status` subresource lands (long-standing TODO in pkgwatcher.go), generation-based diffing will work and this kludge can drop.

## Cross-namespace lookups

| Resource | Where it gets created | Test must list with |
|---|---|---|
| `Ingress` (created by router on HTTPTrigger with `--ingressrule`) | `POD_NAMESPACE` (defaults to `fission`) | `f.KubeClient().NetworkingV1().Ingresses(metav1.NamespaceAll).List(...)` — the label-selector (`functionName + triggerName`) is unique enough that cross-ns matching is safe. |

If a test creates a CR in `default` and expects to see a side-effect resource also in `default`, double-check whether the controller writes it elsewhere. Router's Ingress creation is the only one we hit during migration; others may be similar.

## Test debug checklist (when one fails)

1. **Re-run alone** — `go test -tags=integration -run TestFooBar -v -count=1 ./test/integration/suites/common/...`. If it passes solo and fails in the parallel suite, it's a parallel-load issue (more pods, more contention, more kube-proxy lag).
2. **Read the diagnostic dump** — `$LOG_DIR/<sanitized-test-name>/` has `events.yaml`, `pods.yaml`, container logs, CR yamls. The pod's `caller":"…"` field in structured logs tells you which Fission binary version is actually running.
3. **Check `Status.BuildLog`** if a build failed — it's captured in the diag dump's `packages.yaml` AND in the `WaitForPackageBuildSucceeded` failure message.
4. **Compare to bash counterpart** — `git log -- test/tests/<old-bash-test>.sh` may surface why a particular dance was needed. Many migration commits cite the bash precedent.
5. **Hop the layered race fixes** — if it's a builder-pod-related test and you see `dial tcp …:8000: i/o timeout`, walk back through the 8 fixes above. Most likely you need a longer settle, not a new mechanism.
6. **`fission#653` style hangs** — if specialize fails (invalid function code, runtime crash) the router's request never returns headers, so `GetEventually` polls until ctx timeout. Reduce the test to a happy-path assertion until upstream returns a proper error response.
7. **Skipped under FIXME** — `TestIdleObjectsReaper` is currently skipped. Don't re-enable without addressing the parallel-CI fsvc-TTL refresh issue called out in commit `0fc53807`.

## When the framework needs a new helper

Adding a one-off API call to a test is fine. If the same idiom shows up in 2+ tests, hoist it to `framework/`:

- Add to `framework.go` if cluster-scoped or namespace-creating.
- Add to `namespace.go` if per-namespace state.
- Add to a topic-specific file (`builder.go`, `canary.go`, `package.go`, etc.) if it's domain-specific.
- Update `docs/test-migration/02-framework-api.md` in the same PR — the framework-api doc is the discoverability surface for future tests.

The framework grew **49 helpers** during migration. The pattern that scaled was: introduce when a second test needs it, document in `02-framework-api.md` with the symptom that motivated it, register cleanup automatically. Tests that don't need to think about cleanup are tests that don't leak resources.

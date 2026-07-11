# Fission integration-test quirks

The "things that made tests flake" list the 2026-04/05 bash→Go test migration uncovered.
Read it before assuming a flake is random.
Authoritative framework reference: `docs/test-migration/02-framework-api.md`; this is the "what burned us" companion for the test rows in [`fission-ci-failure-patterns.md`](fission-ci-failure-patterns.md).

## The builder ↔ runtime ↔ Service readiness race (top source of flakes)

Symptom: `dial tcp <svc>:8000: i/o timeout` from buildermgr → fetcher when a test calls `CreatePackage(Src=...)` shortly after `CreateEnv(Builder=...)`.
Layered fix shipped over several iterations, all folded into `CreateEnv` automatically when `EnvOptions.Builder` is set:

1. Wait for builder pod Ready (`WaitForBuilderReady`, by `envName=<env>`).
2. Wait for runtime pool pods Ready — buildermgr's POST goes through a round-robining Service.
3. Wait for **all** pool pods (`waitForRuntimePoolReady`) — one Ready pod isn't enough when the Service points at three.
4. Wait for the env Service to publish endpoints — `Pod Ready` fires before kube-proxy programs iptables; the POST can land on a stale endpoint.
5. Use EndpointSlices via service-name **prefix**, not Endpoints by name — the env Service has a port-hash suffix (`<env>-<rv>-<hash>`); listing Endpoints by exact name 404s.
   EndpointSlices carry a `kubernetes.io/service-name` label.
6. Target the **builder** Service (`<env>-<env.RV>`, selecting `envName=<env>`), not the runtime pool (selecting `environmentName=<env>`).
   Reference: `pkg/buildermgr/common.go:57`.
7. 5-second settle after EndpointSlice readiness — the fetcher process can take a beat to bind port 8000 (k8s 1.28 needed 15s; commit `9bc0c573`).
8. `WaitForPackageBuildSucceeded` retries up to 3× on the transient `dial tcp …:8000: i/o timeout`, resetting `Status.BuildStatus → pending` to re-trigger buildermgr's UpdateFunc.

If a test **bypasses** `CreateEnv` and makes Environments directly via `f.FissionClient()`, the race is back.
This is a test-cadence artefact (env create → immediate build); in production, builds happen long after env creation, so fixing the framework — not Fission proper — is correct.

## Pod label conventions — why selectors silently miss

| Pod | Labels | Set in |
|---|---|---|
| Per-env builder pod (builder + fetcher sidecar) | `envName=<env>, envNamespace=<ns>, envResourceVersion=<rv>, owner=buildermgr` | `pkg/buildermgr/envwatcher.go` `getLabels()` |
| Runtime pool pod (poolmgr) | `environmentName=<env>, functionName=<fn>, executorType=poolmgr, managed=<bool>, executorInstanceId=<id>, environmentNamespace=<ns>, environmentUid=<uid>` | `pkg/apis/core/v1/const.go` |
| Newdeploy / container function pod | as above with `executorType=newdeploy\|container` | same |

- A selector `envName=<env>` matches **builder pods**, not runtime pods (which carry `environmentName=`).
  `WaitForBuilderReady` uses `envName=`; `WaitForRuntimePodReady` uses `environmentName=`.
  Mixing them up was a recurring migration bug.
- For function-specific log reading use `functionName=<fn>` (narrower; exactly the specialised pod) — see `ns.FunctionLogs()`.

## `ns.CLI` captures cobra writers, not `os.Stdout`

The in-process CLI sets cobra's `Out`/`Err` to a `bytes.Buffer` that `ns.CLI` returns.
But these subcommands print to `os.Stdout` directly: `fission function logs`, `fission archive list/download/get-url/delete`.
For those use `ns.CLICaptureStdout` (or `CLICaptureStdoutBestEffort`) — it takes the process-global `cliMu` write lock to serialize against concurrent `ns.CLI` calls; raw `os.Stdout` redirection in test code races sibling `t.Parallel()` tests.
For env-var-driven flags (e.g. `FISSION_DEFAULT_NAMESPACE`) use `ns.CLIWithEnv(t, ctx, env, args...)` — same lock idea, restores env on return.

## `embed.FS` skips nested Go modules

A file under `test/integration/testdata/<lang>/<feature>/` that exists on disk but fails `WriteTestData` with "file does not exist": `//go:embed` silently excludes any directory containing its own `go.mod` (`testdata/go/module_example/` hit this).
Workaround: store as `.txt`, strip the suffix when materializing (`TestGoEnv`'s `zipModuleExample` is the reference).
`embed.FS` also skips files starting with `_` or `.` — don't store fixtures under `_internal`/`.hidden`.

## Spec tests need `WithCWD`

`fission spec init/apply/destroy` and `env create --spec`, `fn create --spec` write specs to `./specs` relative to `os.Getwd()` — there's no `--specdir` on `env create`/`fn create` (only on `spec apply`).
Use `ns.WithCWD(t, workdir, func(){ … })` (chdir under process-global `cwdMu`, runs the closure, restores).
Concurrent tests are safe as long as they pass absolute paths (every framework helper does).
Also relevant for `fn create --deploy "<glob>"` — globs expand against cwd.
Vendored spec YAMLs with hardcoded names trip parallel tests over each other: use `framework.MaterializeSpecs(t, embedDir, replacements, workdir)` (longest-old-string-first replacer) + `framework.NewSpecUID(t)` for the `DeploymentConfig.uid` (so `spec destroy` only removes that test's resources).

## Package CR has no `/status` subresource yet

`pkg/buildermgr/pkgwatcher.go` only re-builds when `Package.Status.BuildStatus == "pending"`.
The Go clientset `Update()` overwrites Status, so a test that updates `Spec.Source.URL` and expects a rebuild **must explicitly set** `Status.BuildStatus = fv1.BuildStatusPending` along with the spec change.
(The bash suite side-stepped this with `kubectl replace`, which round-trips Status.)
Once the `/status` subresource lands, generation-based diffing will work and this kludge can drop.

## Cross-namespace side-effect resources

A router-created `Ingress` (HTTPTrigger with `--ingressrule`) lands in `POD_NAMESPACE` (defaults to `fission`), not the trigger's namespace.
List with `metav1.NamespaceAll` — the `functionName+triggerName` label selector is unique enough that cross-ns matching is safe.
If a controller writes a side-effect resource, double-check which namespace it lands in.

## WebSocket dial succeeds but `ReadMessage` returns "Error"

`TestWebsocket` fails on slower k8s (1.32+/1.34 in CI; 1.28 passes) with `actual: "Error"` instead of the echo.
Dial succeeded (101 Switching Protocols) so the router upgraded, but the function pod's ws-handler isn't attached yet and the first frame is the router's "Error" placeholder.
Fix: retry the **entire** dial+SetReadDeadline+WriteMessage+ReadMessage cycle (not just the dial) inside `require.EventuallyWithT`, re-establishing the connection each attempt to drive it through pod warmup.
Re-sign the HMAC headers on every attempt (timestamp must stay inside the verifier's skew window).
The framework's `RouterClient` does this for HTTP automatically; raw `gorilla/websocket` dials must build headers with `hmacauth.DeriveServiceKey(master, hmacauth.ServiceRouterInternal)` + `hmacauth.Sign(...)` (canonical = method, path, nil body, ts).

## Router/MCP reachability: in-process port-forwards, not kubectl

The framework registers `router.fission` / `router-internal.fission` / `mcp.fission` in a go-portless registry that port-forwards in-process from `FISSION_NAMESPACE` (default `fission`) — no `kubectl port-forward` is needed, and pod restarts self-heal (each dial re-resolves a ready pod).
Base-URL hosts (`f.Router(t).BaseURL()`, `f.RouterInternalBaseURL()`, `f.MCPBaseURL()`) are portless route names: only clients built from the framework (`f.HTTPClient()`, `Router(t)`'s client) resolve them; a plain `http.Client` cannot.
Env overrides `FISSION_ROUTER` / `FISSION_ROUTER_INTERNAL` / `FISSION_MCP_BASE_URL` (host:port or URL) point a route at a fixed address instead — for hand-managed forwards or non-default installs.
`/fission-function/...` lives only on the internal listener since the listener split; `f.Router(t)` auto-routes those paths there.
To have the framework sign requests, export the master HMAC secret; empty secret = unsigned, which works only in pass-through mode (`internalAuth.enabled=false`):
```bash
export FISSION_INTERNAL_AUTH_SECRET=$(kubectl get secret fission-internal-auth -n fission -o jsonpath='{.data.master}' | base64 -d)
```
The MCP test (`TestMCPToolsListAndCall`) `t.Skip`s when `svc/mcp` is unreachable; the service exists only when `mcp.enabled` (on in the kind/kind-ci profiles).
The MCP server runs in `fission`, so its pod log is in the CI `kind-logs` artifact, not the `default`-scoped diagnostics dump.

## In-process e2e harness bypasses fission-bundle/main.go

`test/e2e/framework/services/services.go` calls `timer.Start`/`kubewatcher.Start`/`mqtrigger.StartScalerManager` directly — NOT through `cmd/fission-bundle/main.go`.
Any env-resolution logic in main.go (e.g. the `ROUTER_INTERNAL_URL → publishURL` override) must be **mirrored explicitly** there, or the in-process harness points trigger publishers at the wrong URL.
Symptom on miss: e2e tests pass functionally but `/fission-function/...` from timer/kubewatcher hits the public listener and 404s — the integration suite (real fission-bundle) won't reproduce it.
Rule: when adding env-driven config to a `Start()` entrypoint, grep `services.go` for that env name and replicate the resolution.

## Test debug checklist

1. **Re-run alone** — `go test -tags=integration -run TestFooBar -v -count=1 ./test/integration/suites/common/...`.
   Passes solo but fails in the suite ⇒ parallel-load issue (more pods, more kube-proxy lag).
2. **Read the diagnostic dump** — `$LOG_DIR/<sanitized-test-name>/` has `events.yaml`, `pods.yaml`, container logs, CR yamls.
   The `caller":"…"` field tells you which binary version actually ran.
3. **Check `Status.BuildLog`** if a build failed — captured in `packages.yaml` and in the `WaitForPackageBuildSucceeded` failure message.
4. **Hop the layered race fixes** — a builder-pod test with `dial tcp …:8000: i/o timeout` most likely needs a longer settle, not a new mechanism.
5. **Skipped under FIXME** — `TestIdleObjectsReaper` is currently skipped; don't re-enable without addressing the parallel-CI fsvc-TTL refresh issue (commit `0fc53807`).

## When the framework needs a new helper

A one-off API call in a test is fine; if the idiom appears in 2+ tests, hoist it to `framework/` (`framework.go` for cluster/namespace-creating, `namespace.go` for per-namespace state, a topic file like `builder.go`/`canary.go`/`package.go` for domain-specific) and document it in `docs/test-migration/02-framework-api.md` in the same PR.
Introduce when a second test needs it; register cleanup automatically.

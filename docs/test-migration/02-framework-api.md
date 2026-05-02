# Framework API

Helper-by-helper reference for `test/integration/framework`.
This file grows incrementally as helpers are added in each phase.
See `00-design.md` for design rationale and `01-migration-status.md` for migration progress.

All framework code lives behind the `//go:build integration` build tag, so it is invisible to `make test-run` and only compiles when the test runner sets the `integration` tag.

## Conventions

- All helpers take `t *testing.T` as the first argument and call `t.Helper()` so failures point at the test, not the helper.
- All resource-creating helpers register cleanup automatically (via `ns.addCleanup` → single namespace-level `t.Cleanup`). Tests do not write defers.
- Mutations go through `ns.CLI(t, ctx, args...)`. Reads and waits go through the typed clientset (`f.FissionClient()`, `f.KubeClient()`).
- Helpers do not return errors. They use `testify/require` for prerequisites that must hold for the test to continue (`require.NoError`, `require.NotEmpty`), and tests use `testify/assert` for sibling checks where seeing all failures aids debugging (e.g. multiple fields of one rendered yaml document).
- Polling loops use `require.EventuallyWithT(t, func(c *assert.CollectT){...}, waitFor, tick)`. The condition runs assertions on a `*CollectT`; failed assertions cause the iteration to retry; on final timeout testify reports the most recent assertion errors — so failure messages reflect actual final state, no manual lazy-format dance required.
- The one exception is `package.go`'s build-status poll, which needs to *early-exit* on terminal `BuildStatusFailed` (so the test fails fast with the captured BuildLog rather than waiting the full timeout). That helper uses `wait.PollUntilContextTimeout` directly.
- `t.Parallel()` is the default in suite tests. The framework is concurrency-safe — no global mutable state, per-test namespaces.

## Environment variables read by the framework

| Variable | Purpose | Default |
|----------|---------|---------|
| `KUBECONFIG` | Path to kubeconfig pointing at the test cluster. | `$HOME/.kube/config` (controller-runtime default) |
| `FISSION_ROUTER` | `host:port` (or full URL) of the port-forwarded Fission router. | `127.0.0.1:8888` |
| `FISSION_NAMESPACE` | Namespace where Fission control plane is deployed. (Reserved for future use; not consumed yet.) | `fission` |
| `NODE_RUNTIME_IMAGE`, `NODE_BUILDER_IMAGE`, `PYTHON_RUNTIME_IMAGE`, `PYTHON_BUILDER_IMAGE`, `GO_RUNTIME_IMAGE`, `GO_BUILDER_IMAGE`, `JVM_RUNTIME_IMAGE`, `JVM_BUILDER_IMAGE`, `JVM_JERSEY_RUNTIME_IMAGE`, `JVM_JERSEY_BUILDER_IMAGE`, `TS_RUNTIME_IMAGE` | Runtime/builder images for env tests. | Tests `t.Skip` when a required image is unset. |
| `LOG_DIR` | Directory for diagnostic dumps on test failure. | `test/integration/logs/` |
| `TEST_NOCLEANUP` | If set to `1`, leave the test namespace and resources behind on test exit. | unset |

## Top-level types

### `Framework` — process-wide singleton

`framework.Connect(t)` returns a `*Framework` built from `KUBECONFIG` on first call and cached for subsequent calls.
Cached fields: typed Fission clientset, Kubernetes clientset, runtime image registry, router base URL, logger.

```go
f := framework.Connect(t)
img := f.Images().RequireNode(t) // skips if NODE_RUNTIME_IMAGE unset
```

### `TestNamespace` — per-test resource scope

`f.NewTestNamespace(t)` returns a per-test scope rooted in the **`default`** Kubernetes namespace, with a fresh 6-hex-character `ID`. Tests embed `ns.ID` into resource names so concurrent tests (`t.Parallel()`) don't collide.

It does **not** create a Kubernetes namespace, because the deployed Fission router only watches namespaces listed in `FISSION_RESOURCE_NAMESPACES` (default: `default`) per `pkg/utils/namespace.go`. Resources in arbitrary namespaces would be invisible to the router.

What it does:

1. Generates a unique `ns.ID`.
2. Sets `ns.Name = "default"`.
3. Registers a `t.Cleanup` that dumps diagnostics (events, pods, Fission CRs in the namespace) to `$LOG_DIR/<sanitized-test-name>/` if `t.Failed()`.

Per-resource cleanup (delete the env, function, route, package, etc.) is registered by the `Create*` helpers themselves via their own `t.Cleanup`, so failures during creation still clean up what was successfully created. All cleanup hooks honor `TEST_NOCLEANUP=1`.

```go
ns := f.NewTestNamespace(t)
envName := "nodejs-" + ns.ID    // unique per test
fnName  := "hello-"  + ns.ID
```

Once Fission supports a wildcard or dynamically-extended namespace list, this scope can revert to one Kubernetes namespace per test for cleaner isolation.

## Helpers (Phase 1)

### `ns.CLI(t, ctx, args...)` → `string`

Invokes the Fission CLI in-process via `cmd.fission-cli/app.App`. No fork/exec.
The default namespace passed to the CLI is `ns.Name`, so commands like `fission fn create --name foo` create the function in the test namespace.
Returns combined stdout+stderr; `t.Fatal` on non-zero exit.

```go
ns.CLI(t, ctx, "env", "create", "--name", "myenv", "--image", img)
out := ns.CLI(t, ctx, "fn", "list")
```

### `ns.CreateEnv(t, ctx, EnvOptions{Name, Image})`

Creates a Fission Environment via `fission env create`.

```go
ns.CreateEnv(t, ctx, framework.EnvOptions{
    Name:  "nodejs-" + ns.ID,
    Image: f.Images().Node,
})
```

### `ns.CreateFunction(t, ctx, FunctionOptions{Name, Env, Code})`

Creates a Function from a local source file via `fission fn create --code ...`.

```go
codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
ns.CreateFunction(t, ctx, framework.FunctionOptions{
    Name: "hello",
    Env:  envName,
    Code: codePath,
})
```

### `ns.CreateRoute(t, ctx, RouteOptions{Function, URL, Method, Name})`

Creates an HTTPTrigger via `fission route create`. `Name` is optional (CLI auto-generates).

```go
ns.CreateRoute(t, ctx, framework.RouteOptions{
    Function: "hello",
    URL:      "/hello",
    Method:   "GET",
})
```

### `ns.WaitForFunction(t, ctx, name)`

Polls until the Function CR is visible in the test namespace.
Use this when the CLI returns before the controller has processed the request and the test wants to assert on cluster state.

### Polling primitive: `require.EventuallyWithT`

Use testify directly — there is no framework wrapper. Capture the parent `ctx` by closure so test cancellation propagates through clientset calls.

```go
require.EventuallyWithT(t, func(c *assert.CollectT) {
    _, err := f.FissionClient().CoreV1().Functions(ns.Name).Get(ctx, name, metav1.GetOptions{})
    assert.NoErrorf(c, err, "function %q not visible in namespace %q", name, ns.Name)
}, 30*time.Second, 500*time.Millisecond)
```

When the timeout fires, testify reports the last iteration's assertion errors — which include the actual `err` value, the last observed weight, etc. — without needing a separate "lazy fail message" mechanism.

### `f.Router(t)` → `*RouterClient`

HTTP client targeting the Fission router (default `http://127.0.0.1:8888`, override via `FISSION_ROUTER`).

```go
r := f.Router(t)
status, body, err := r.Get(ctx, "/hello")              // single-shot
body := r.GetEventually(t, ctx, "/hello", framework.BodyContains("hello")) // poll until satisfied
```

`ResponseCheck` is a `func(status int, body string) bool`. `framework.BodyContains(substr)` is the only built-in today; more checks (`StatusEquals`, `JSONFieldEquals`) get added as tests need them.

### `framework.WriteTestData(t, embedPath)` → `string`

Reads a file from the embedded `testdata.FS` and writes it to `t.TempDir()`, returning the on-disk path.
This is the bridge between vendored fixtures (which the CLI cannot read directly because they live in `embed.FS`) and CLI flags like `--code` that take a filesystem path.

```go
codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
```

The embed root is `test/integration/testdata/`. Add new fixtures by creating files there and updating the `//go:embed` directive in `testdata/embed.go`.

### Diagnostics on failure

Registered automatically by `NewTestNamespace`; runs only when `t.Failed()`.
Dumps to `$LOG_DIR/<sanitized-test-name>/`:

- `events.yaml` — Kubernetes events in the test namespace.
- `pods.yaml` — pod descriptions.
- `logs-<pod>-<container>.log` — container logs for every pod (init + main).
- `environments.yaml`, `functions.yaml`, `packages.yaml`, `httptriggers.yaml` — Fission CRs in the namespace.

The CI workflow uploads `test/integration/logs/` as the `go-integration-logs-<run>-<k8s-version>` artifact (5-day retention).

## Helpers (Phase 2)

### `EnvOptions.Builder`

Optional builder image (e.g. `PYTHON_BUILDER_IMAGE`). When set, `CreateEnv` invokes `fission env create --image ... --builder ...`, which lets functions use source-package builds against this environment.

### `FunctionOptions.Src` / `Entrypoint` / `BuildCmd`

When `Src` is set instead of `Code`, `CreateFunction` invokes `fission fn create --src <archive> --entrypoint <module:func> --buildcmd <cmd>`. `Code` and `Src` are mutually exclusive (the helper `t.Fatal`s if both are set).

```go
srcZip := framework.ZipTestDataDir(t, "python/sourcepkg", "demo-src-pkg.zip")
ns.CreateFunction(t, ctx, framework.FunctionOptions{
    Name: "srcbuild-" + ns.ID,
    Env:  envName,
    Src:  srcZip,
    Entrypoint: "user.main",
    BuildCmd:   "./build.sh",
})
```

### `ns.WaitForBuilderReady(t, ctx, envName)`

Polls for a Pod labelled `envName=<env>` to reach `Ready=True`. Mirrors the bash `wait_for_builder` helper. Default timeout is 3 minutes (covers cold image pulls on Kind).

### `ns.WaitForPackageBuildSucceeded(t, ctx, pkgName)`

Polls `Package.Status.BuildStatus` until it reaches `succeeded`. If the build reaches the terminal `failed` state, the helper `t.Fatal`s with the build log captured from `Status.BuildLog` — much better signal than a generic timeout.

### `ns.WaitForPackageBuildStatus(t, ctx, pkgName, status, timeout)`

Lower-level variant for negative tests that want to assert on a specific terminal state (e.g. `fv1.BuildStatusFailed`).

### `ns.FunctionPackageName(t, ctx, fnName)` → `string`

Returns `Function.Spec.Package.PackageRef.Name`. Mirrors:

```sh
kubectl get functions <fn> -o jsonpath='{.spec.package.packageref.name}'
```

`fission fn update --src <new>` writes a new package and re-points the function at it; tests can poll until `FunctionPackageName` returns a fresh value to detect that the rebuild started.

### `framework.ZipTestDataDir(t, embedDir, archiveName)` → `string`

Packs an embedded testdata directory into a flat zip (no nested entries — mirrors the bash `zip -j` idiom used by the existing source-package tests) under `t.TempDir()`. Returns the on-disk archive path for `--src`.

### `f.Images().RequirePython(t)` / `RequirePythonBuilder(t)`

Companion to `RequireNode`. The full set fans out as more environments come online during migration.

## Helpers (Phase 3)

### `EnvOptions.GracePeriod`

Optional `--graceperiod <n>` (seconds). Lower values speed up pod recycling between function versions. Canary tests use `1` so traffic flips cleanly when weights change.

### `RouteOptions.FunctionWeights` / `framework.FunctionWeight`

Multi-version `HTTPTrigger`. The CLI accepts paired `--function <fn> --weight <w>` repeated; the framework emits the args in the slice order.

```go
ns.CreateRoute(t, ctx, framework.RouteOptions{
    URL:    "/" + routeName,
    Method: "GET",
    FunctionWeights: []framework.FunctionWeight{
        {Name: fnV1, Weight: 100},
        {Name: fnV2, Weight: 0},
    },
})
```

`Function` and `FunctionWeights` are mutually exclusive (the helper `t.Fatal`s if both are set or neither).

### `ns.FunctionWeight(t, ctx, routeName, fnName)` → `int`

One-shot read of the current weight assigned to a function on the named HTTPTrigger. Returns 0 if the function isn't in `Spec.FunctionReference.FunctionWeights`.

### `ns.WaitForFunctionWeight(t, ctx, routeName, fnName, want, timeout)` / `WaitForFunctionWeightAtLeast`

Polls the HTTPTrigger spec until the weight assigned to `fnName` matches the predicate. The canary controller settles asynchronously (every `IncrementInterval`); these helpers observe the final state.

`WaitForFunctionWeightAtLeast` is for negative tests where the *initial* state already matches the final target — e.g. rollback tests start at v3=0 and end at v3=0; the test must first wait for v3 to rise above 0 (canary alive) before asserting it returned to 0 (rollback fired).

Failure messages are built lazily via `EventuallyLazy`, so `(last=N)` reports the actual last observed value (not the zero captured at call time).

### `ns.CreateCanaryConfig(t, ctx, CanaryConfigOptions{...})`

Creates a `CanaryConfig` CR via `fission canary-config create`. Required: `Name`, `NewFunction`, `OldFunction`, `HTTPTrigger`. Optional: `IncrementStep`, `IncrementInterval` (Go duration string like `"30s"`), `FailureThreshold` (percent).

### `r.Post(ctx, path, contentType, body)` / `r.PostEventually(t, ctx, path, contentType, body, check)`

POST companion to `Get` / `GetEventually`. Use `PostEventually` for tests that retry until a `ResponseCheck` is satisfied — e.g. `TestHugeResponse` POSTs a 240KB body and retries until the echo length matches (a transient truncation would be a real bug worth catching).

### `ns.FunctionLogs(t, ctx, fnName)` → `string`

Returns the combined log output of every pod backing the function's environment, read directly via the Kubernetes API. Mirrors `fission function logs --name <fn> --detail` for assertion purposes — the CLI subcommand streams pod logs to `os.Stdout` directly, which the in-process `ns.CLI` helper does not capture (it only routes cobra's `SetOut`/`SetErr`). `os.Stdout` redirection would be unsafe under `t.Parallel()`.

### `ns.CreateEnvObject(t, ctx, env *fv1.Environment)`

Companion to `CreateEnv` for tests that need fields the CLI doesn't expose — most notably `metadata.annotations` (TestEnvironmentAnnotations) or `runtime.podspec` for pod-level customization. Forces `env.Namespace = ns.Name`. Registers cleanup the same way as `CreateEnv`.

### `ns.Framework()` → `*Framework`

Escape hatch when a test needs framework-level state (typed clientsets, router URL) that isn't surfaced via a per-namespace shortcut. Used sparingly — typical tests don't need this.

### `f.Images().RequireGo(t)` / `RequireGoBuilder(t)`

Same shape as `RequireNode` / `RequirePython`. CI's "Go integration tests (common phase)" step pre-pulls and kind-loads `GO_RUNTIME_IMAGE` and `GO_BUILDER_IMAGE`.

### `r.LoadLoop(ctx, path)`

Fires GETs at ~10 rps until `ctx` is cancelled. The canary controller measures failure rate per tick; without sustained traffic over multiple ticks it can't justify successive weight increments. Tests typically launch this in a goroutine with a `t.Cleanup`-bound cancel:

```go
loadCtx, stop := context.WithCancel(ctx)
t.Cleanup(stop)
go f.Router(t).LoadLoop(loadCtx, "/myroute")
ns.WaitForFunctionWeight(t, ctx, routeName, fnName, 100, 5*time.Minute)
```


## Helpers (Phase 4 and beyond)

Timer/kubewatcher/MQ trigger helpers and a `Specs`/yaml-apply helper will be added as the bulk migration touches the relevant tests. This file gets a new section per phase.

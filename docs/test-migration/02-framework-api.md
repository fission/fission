# Framework API

Helper-by-helper reference for `test/integration/framework`.
This file grows incrementally as helpers are added in each phase.
See `00-design.md` for design rationale and `01-migration-status.md` for migration progress.

All framework code lives behind the `//go:build integration` build tag, so it is invisible to `make test-run` and only compiles when the test runner sets the `integration` tag.

## Conventions

- All helpers take `t *testing.T` as the first argument and call `t.Helper()` so failures point at the test, not the helper.
- All resource-creating helpers register `t.Cleanup` automatically. Tests do not write defers.
- Mutations go through `ns.CLI(t, ctx, args...)`. Reads and waits go through the typed clientset (`f.FissionClient()`, `f.KubeClient()`).
- Helpers do not return errors. They `t.Fatal` on unrecoverable conditions and `t.Error` on recoverable ones.
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

### `TestNamespace` — per-test isolation unit

`f.NewTestNamespace(t)` creates `fission-it-<sanitized-test-name>-<6-hex-id>` in the cluster, labels it `fission.io/test-id=<id>`, and registers a `t.Cleanup` that:

1. Dumps diagnostics (events, pod logs, Fission CR YAML) to `$LOG_DIR/<sanitized-test-name>/` if `t.Failed()`.
2. Deletes the namespace, **unless** `TEST_NOCLEANUP=1`.

All Fission CRs are namespace-scoped, so the namespace deletion cleans up everything the test created.

```go
ns := f.NewTestNamespace(t)
// ns.Name is the namespace name (max 63 chars, DNS-1123 compliant).
// ns.ID is the random 6-hex-character ID.
```

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

### `framework.Eventually(t, ctx, timeout, interval, cond, failMsg, ...)`

Polling primitive over `wait.PollUntilContextTimeout`.
The condition runs immediately and is retried until it returns `(true, nil)` or the timeout elapses.
Timeout or condition error becomes `t.Fatal` with the formatted message.

```go
framework.Eventually(t, ctx, 30*time.Second, 500*time.Millisecond,
    func(c context.Context) (bool, error) {
        _, err := f.FissionClient().CoreV1().Functions(ns.Name).Get(c, name, metav1.GetOptions{})
        return err == nil, nil
    },
    "function %q never became visible", name)
```

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

## Helpers (Phase 2 and beyond)

Builder/package helpers, canary helpers, timer/kubewatcher/MQ trigger helpers, and a `Specs`/yaml-apply helper will be added in subsequent phases.
This file gets a new section per phase.

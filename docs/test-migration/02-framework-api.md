# Framework API

Helper-by-helper reference for `test/integration/framework`.
This file grows incrementally as helpers are added in each phase.
See `00-design.md` for design rationale and `01-migration-status.md` for migration progress.

> **Status**: stub. The framework will be populated starting in Phase 1.
> Update this file in the same PR as each helper is added — tests that come later in the migration should be reading from this doc.

## Conventions

- All helpers take `t *testing.T` as the first argument and call `t.Helper()` so failures point at the test, not the helper.
- All resource-creating helpers register `t.Cleanup` automatically. Tests do not write defers.
- Mutations go through `f.CLI(t, ctx, args...)`. Reads and waits go through the typed clientset (`f.FissionClient()`, `f.KubeClient()`).
- Helpers do not return errors. They `t.Fatal` on unrecoverable conditions and `t.Error` on recoverable ones.
- `t.Parallel()` is the default in suite tests. The framework is concurrency-safe — no global state, per-test namespaces.

## Environment variables read by the framework

| Variable | Purpose | Default |
|----------|---------|---------|
| `KUBECONFIG` | Path to kubeconfig pointing at the test cluster. | `$HOME/.kube/config` (controller-runtime default) |
| `FISSION_ROUTER` | `host:port` of the port-forwarded Fission router. | `127.0.0.1:8888` |
| `FISSION_NAMESPACE` | Namespace where Fission control plane is deployed. | `fission` |
| `NODE_RUNTIME_IMAGE`, `NODE_BUILDER_IMAGE`, `PYTHON_RUNTIME_IMAGE`, `PYTHON_BUILDER_IMAGE`, `GO_RUNTIME_IMAGE`, `GO_BUILDER_IMAGE`, `JVM_RUNTIME_IMAGE`, `JVM_BUILDER_IMAGE`, `JVM_JERSEY_RUNTIME_IMAGE`, `JVM_JERSEY_BUILDER_IMAGE`, `TS_RUNTIME_IMAGE` | Runtime/builder images for env tests. | Set by `kind_CI.sh` and the new CI steps. Tests `t.Skip` if a required image is unset. |
| `LOG_DIR` | Directory for diagnostic dumps on test failure. | `test/integration/logs/` |
| `TEST_NOCLEANUP` | If set to `1`, leave the test namespace and resources behind on test exit. | unset |

## Helpers

### `framework.Connect(t)`

> **Phase 1 — to be implemented.**
> Builds a `*Framework` from `KUBECONFIG`. Caches the typed Fission clientset (`pkg/generated/clientset`) and the Kubernetes clientset. Verifies the Fission control plane is reachable (e.g., a quick GET on the router). Cached on `sync.Once` per process — safe to call from every test.

### `f.NewTestNamespace(t)` → `string`

> **Phase 1 — to be implemented.**
> Creates `fission-it-<sanitized-test-name>-<5char-rand>`, labels it `fission.io/test-id=<id>`, registers `t.Cleanup` (skip when `TEST_NOCLEANUP=1`), and registers `diag.OnFailure(t, ns)`. Returns the namespace name.

### `f.CLI(t, ctx, args...)` → `(stdout, stderr string)`

> **Phase 1 — to be implemented.**
> Invokes the Fission CLI in-process via `cmd.fission-cli/app.App`. No fork/exec. Captures stdout/stderr. `t.Fatal` on non-zero exit. The returned strings are for optional substring assertions in the test.

### Helper sets to come (added incrementally per the migration plan)

The plan in `00-design.md` lists the file layout:

- `cluster.go`, `cli.go`, `namespace.go`, `env.go`, `function.go`, `package.go`, `httptrigger.go`, `canary.go`, `timer.go`, `kubewatcher.go`, `mqtrigger.go`, `router.go`, `poll.go`, `diag.go`, `images.go`, `cleanup.go`.

Each helper is documented here in the PR that introduces it.

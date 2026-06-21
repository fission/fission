# RFC-0018: Local-Development Inner Loop for Function Developers

- Status: Implemented (phases 0–5 except `--remote`; branch `feat/rfc-0018-function-run-local`)
- Tracking issue: —
- Supersedes: —
- Targets: Fission v1.N+ (phased; an interpreted-language MVP ships first)
- Requires: no Kubernetes floor change; a Docker/containerd engine on the developer's machine for the local path (the `--remote` path in a later phase needs only a cluster).
  The Docker Engine client (`github.com/moby/moby/client`) is already a transitive dependency; `ory/dockertest/v3` is already a test dependency.
- Related: [RFC-0001](0001-oci-native-package-delivery.md) / [RFC-0012](0012-oci-default-package-delivery.md) (the image-volume specialize path this reuses), [RFC-0017](0017-function-developer-debugging-toolkit.md) (shares the invoke path and header builder), [RFC-0015](0015-invocation-correlation-and-failure-attribution.md) (the `X-Fission-*` headers injected for fidelity).

## Summary

Fission's edit→test loop forces a cluster round-trip for every change: upload source to storagesvc, wait for a builder pod (10–60 s), then trigger a cold fetch + specialize on first invocation — a measured 15–120 s per iteration, with no way to run a function against its real environment image without a cluster.
AWS Lambda's single biggest developer-experience advantage is the local inner loop (`sam local`), and Fission has no equivalent.
This RFC adds `fission function run` — a local-Docker emulation that starts the function's real environment runtime image, replays the exact in-cluster specialize contract to load the user's code, and invokes it with the same headers the router injects — collapsing the interpreted-language inner loop to sub-second.
It is feasible precisely because the loader contract is small and already reproduced in-tree by the OCI image-volume path (`loadOnlySpecialize`), so we get high fidelity with zero per-language work.
The command is entirely new and additive, supports both the v1 and v2 environment contracts, and can run cluster-less with `--image`.

## Motivation

The loop is slow because every step is remote, yet the part that actually loads user code is local-reproducible:

- In-cluster, a v2 environment pod is specialized by: mounting the deploy package at `<sharedMountPath>/deployarchive`, then POSTing a `fetcher.FunctionLoadRequest` (`FilePath`, `FunctionName` from `fn.Spec.Package.FunctionName`, `FunctionMetadata`, `EnvVersion`) to the runtime's `/v2/specialize` on port 8888.
  That request is built by `(*config.Config).NewSpecializeRequest` (`pkg/fetcher/config/config.go:192`); `TargetFilename` returns `deployarchive` for v2 and `user` for v1.
- `loadOnlySpecialize` (`pkg/executor/executortype/poolmgr/oci_specialize.go:30`) is the in-tree proof: when the code is *already on the volume* (the OCI image-volume case), it specializes a v2 env with **no fetcher and no storagesvc**, using a connect-refused retry/backoff loop because the runtime server is not ready the instant the container starts.
- The function is then invoked over plain HTTP on the same port with the router's `X-Fission-*` headers.

So "code on a volume + POST `/v2/specialize`" fully specializes a real env image.
Locally, a bind-mount replaces the volume and a direct POST replaces the fetcher — the same handshake against a container on the developer's machine, with the published env image as the source of truth.

## Goals

- Sub-second re-run after a source edit for interpreted environments (Python/Node/Ruby/PHP); single-digit seconds for compiled environments where a build is unavoidable.
- High fidelity: the function runs inside its real `Runtime.Image`, specialized via the same `/v2/specialize` contract, invoked with the same `X-Fission-*` headers.
- Reuse existing code — the CLI invoke path, env/package resolution, the specialize-request builder, and the builder image — adding as little new code as possible.
- Optional hot reload on file change.
- Work cluster-less (`--image`) or against an existing cluster, and against older published env images.

## Non-goals (v1)

- Reproducing in-cluster networking/DNS, secrets, ConfigMaps, ServiceAccounts, autoscaling, or non-HTTP triggers (MQ/timer/MCP).
- A debugger-attach protocol — left to a later phase (env images already expose ports we can map).
- Replacing the cluster for CI or integration testing.
- Windows containers; the target is Docker/containerd on Linux/macOS.

## Design

### Approach comparison

| | A. Local Docker emulation | B. Functions-framework harness | C. Remote dev-loop (sync to cluster) |
|---|---|---|---|
| Fidelity to in-cluster runtime | High — real `Runtime.Image`, real `/v2/specialize`, real headers | Low — bypasses the env image; a per-language harness drifts from the published runtime | Highest — actually in-cluster |
| Loop latency | Sub-second after first container start (re-specialize only) | Sub-second | 1–10 s (sync + re-specialize); network-bound |
| Per-language work | **None** — env images already implement the contract | High — one harness per language, ongoing | None |
| New infra | Docker client (already transitive) + a small local specialize shim | New harness binaries/images | Dev-pod controller, source sync, port-forward management |
| Developer prereq | Docker/containerd | Language toolchain locally | A reachable cluster + kubeconfig |
| Risk | Docker dependency; bind-mount UID/permissions | Drift → "works locally, fails in cluster" | Slowest; needs a cluster; most moving parts |

**Recommendation: A (local Docker emulation), with C as a later complementary phase.**
A is the only option that is both high-fidelity and zero-per-language-cost, because the env images already implement the exact loader contract Fission uses in-cluster, and `loadOnlySpecialize` proves the handshake works without the fetcher/storagesvc.
B is rejected because it reintroduces the very fidelity gap we are closing — the published env image is the source of truth.
C is valuable but heavier and cluster-bound; it ships later as `fission function run --remote`.

### 1. CLI surface

A new `run` subcommand under `pkg/fission-cli/cmd/function/` (mirroring the shape of `run_container.go`), registered in `command.go`:

```
fission function run \
  --env python \         # env CRD name; image+version resolved from the cluster (or --image for cluster-less)
  --code hello.py \      # OR --src ./src for builder envs; OR --deploy ./build for a prebuilt archive
  --entrypoint main \    # FunctionName in the load request
  --port 8888 \          # local host port (default: auto-pick a free port)
  --watch \              # re-specialize on file change (hot reload)
  --build \              # run the builder image first (compiled envs; auto-detected with --src)
  --env-from .env \      # inject extra runtime env vars (config-fidelity bridge)
  --keep                 # keep the container running after a one-shot invoke
```

It reuses the `function test` invoke vocabulary so a developer can run and call in one step:

```
fission function run --env python --code hello.py \
  --method POST -b '{"x":1}' -H 'Content-Type: application/json' --query a=b
```

With invoke flags present, `run` starts the container, specializes, fires one request, prints the response, and (unless `--keep`/`--watch`) tears down.
Without them, it prints the local URL (`http://127.0.0.1:<port>`) and blocks until Ctrl-C.

Files to add: `run.go` (the cobra glue + `complete()`/`run()`), `run_local.go` (the Docker lifecycle, the local specialize shim, the watch loop — isolating the Docker dependency so the rest stays unit-testable).

Reuse, not reinvent:

- **Export `doHTTPRequest` from `test.go`** (`pkg/fission-cli/cmd/function/test.go:161`, rename to `DoHTTPRequest`) and share it between `test` and `run` so the invoke path — OTEL transport, header parsing, verbose dump — is byte-identical.
- Resolve the env via the generated clientset exactly as `function create` does, to read `env.Spec.Runtime.Image` and `env.Spec.Version`; with `--image`, skip the cluster entirely.
- Source globbing/archiving reuses `pkg/utils` zip helpers and the package helpers in `pkg/fission-cli/cmd/package`.
- Reuse existing flags (`FnEnvName`, `FnEntryPoint`, `FnPort` (already defaults 8888), `PkgCode`, `PkgSrcArchive`, the `test` invoke flags) and add only `--watch`/`--build`/`--keep`/`--env-from`/`--image`.

### 2. Local specialize shim (no fetcher, no storagesvc)

The point is to skip the in-cluster fetcher + storagesvc.

For interpreted / v1-style envs the runtime reads the function from `FilePath` directly:

1. Create a host temp dir `T`, lay the source out as the runtime expects: copy `--code` to `T/deployarchive` (v2) or `T/user` (v1), or extract `--deploy` into `T/`.
2. `docker run` the `Runtime.Image` with `-v T:/userfunc` and publish container `8888` → host `--port`, injecting the env vars from §3.
3. Build the load request via `(*config.Config).NewSpecializeRequest(fn, env)`, then **rewrite `LoadReq.FilePath`** to the local mount path — guaranteeing identical field semantics to production rather than re-deriving them — and POST it to `http://127.0.0.1:<port>/v2/specialize` (v2) or `/specialize` (v1).
4. **Reuse the retry/backoff loop** factored out of `loadOnlySpecialize` — the connect-refused-then-retry behavior is essential because the env server is not ready the instant the container starts.

No HTTP fetcher shim is needed: the load request carries everything and the "fetch" is replaced by the bind-mount.
Contract endpoints/expectations, named explicitly: `POST /v2/specialize` (v2, JSON `FunctionLoadRequest`) or `POST /specialize` (v1); the runtime listens on 8888 for both specialize and invocation; the shared mount path is `/userfunc`; the v2 target file is `deployarchive`, v1 is `user`.

### 3. Fidelity

On every local invoke, inject the same headers the router sets (`pkg/router/requesthHeader.go`) via a shared header builder used by both the engine and `function test`: `X-Fission-Function-{Uid,Name,Namespace,ResourceVersion}`, `X-Fission-Full-Url`, `X-Fission-Params-*`, plus user `-H` headers, plus RFC-0015's `X-Fission-Request-ID`.
The function process environment in-cluster is small (the env image's own vars plus the OTEL vars); set the same OTEL vars (or disable them) and pass `--env-from`/`-e` for anything else.

| Aspect | Matches in-cluster? | Mitigation |
|---|---|---|
| Runtime image / language version | Yes — same `Runtime.Image` | — |
| Specialize contract (`/v2/specialize`, `FilePath`, `FunctionName`, `EnvVersion`) | Yes — same `FunctionLoadRequest` | reuse `NewSpecializeRequest` |
| Invocation headers (`X-Fission-*`) | Yes | shared header builder |
| Function timeout semantics | Partial | honor `--timeout`; the in-cluster router enforces `FunctionTimeout` |
| Builder/compile output | Yes for compiled envs (run the real builder image) | §4 |
| Secrets / ConfigMaps (`/secrets`, `/configs`) | **No** | `--env-from`/`-e`; later `--secrets-from kubeconfig` materializing the real objects |
| In-cluster DNS / service discovery | **No** | `kubectl port-forward` + `--env-from` to `127.0.0.1`; later `--remote` |
| ServiceAccount / RBAC / IRSA | **No** | out of scope; documented |
| Triggers (HTTP path / MQ / timer / MCP) | HTTP path only | `--subpath`/`--query`; non-HTTP triggers out of scope v1 |

The matrix is the honest contract: runtime + specialize + invoke fidelity is promised; cluster-environment fidelity (secrets/DNS) is explicitly not, with named bridges.

### 4. Builder loop (compiled languages)

For envs with a `Builder.Image` and `--src` (or `--build`): run the builder image locally first, reproducing buildermgr's contract (bind-mount source at the builder shared path, set `SRC_PKG`/`DEPLOY_PKG`, invoke the builder server's build endpoint or the default build command), streaming the build logs to the terminal as the builder does.
The produced deploy directory becomes the `/userfunc/deployarchive` mount for the runtime container.
For interpreted envs (no builder, `--code`/`--deploy`), skip the builder entirely — v1 of the feature can ship interpreted-only, which covers the largest user base (Python/Node) and most of the latency pain.

### 5. Hot reload / watch

With `--watch`, use `github.com/fsnotify/fsnotify` (already a direct dependency) on the source directory.
On change: interpreted envs re-copy the source into the existing `/userfunc` mount and re-POST `/v2/specialize` to the already-running container (no restart); where an env caches the code at process start, fall back to a container restart (still ~1–2 s vs the cluster's 15–120 s).
Compiled envs re-run the builder, then re-specialize.
Debounce events (200–300 ms) and serialize rebuilds, matching the ergonomics of `fission spec apply --watch`.

### 6. Docker client

Use `github.com/moby/moby/client` (typed errors, stream handling) rather than shelling out to `docker`, confined to `run_local.go` behind a small `localRuntime` interface so the cobra command and the watch/invoke logic stay testable with a fake.

## Implementation context (verified as of RFC-0015/0016/0017 merged, 2026-06-20)

Knowledge transfer for the implementing session — the surfaces this RFC reuses, confirmed against `main` after #3515–#3520:

- **`doHTTPRequest` is unchanged and still the invoke path to export** (`pkg/fission-cli/cmd/function/test.go`).
  RFC-0017 added `renderInvocationFailure(out, fnName, statusCode, component, body)` next to it (renders the structured RFC-0015 `{component,reason}` body when the `X-Fission-Component` header is present, else the raw body) and made `test` echo `X-Fission-Request-ID` to stderr.
  Phase 0's "export `DoHTTPRequest`" is still valid; `run` should **also reuse `renderInvocationFailure`** so a failed local invoke reads the same as `test`.
- **Correlation headers now live in `pkg/utils/correlation`** (`HeaderRequestID = "X-Fission-Request-ID"`, `HeaderComponent`, `HeaderDebug`, plus `NewContext`/`FromContext`/`ID`).
  The §3 header builder should use these constants, not string literals.
  The router honors an *inbound* `X-Fission-Request-ID`, so `run` can mint a known id for deterministic local correlation (this is how the integration tests do it via the framework's `GetWithRequestID`).
- **CLI subcommand pattern to mirror** (established by `describe.go` this round): a `*SubCommand` struct embedding `cmd.CommandActioner`; entry `func Run(input cli.Input) error { return (&RunSubCommand{}).do(input) }`; namespace via `opts.GetResourceNamespace(input, flagkey.NamespaceFunction)` (use the **second** return — `currentNS`); register in `command.go` `Commands()` + the `AddCommand` list; flags via `wrapper.SubCommand(..., flag.FlagSet{Required, Optional})`.
  Human output via `util.NewTabWriter` / `util.PrintConditionsTo` / `util.AgeOf` / `util.NoneValue`; `console.Info/Warn` go to **stdout**, `console.Error` to **stderr** — keep stdout clean for machine-readable output, send diagnostics to stderr (the `test` request-id echo does this).
- **`loadOnlySpecialize` is unchanged** (`pkg/executor/executortype/poolmgr/oci_specialize.go`) — still the retry/backoff source to factor out in phase 0.
- **Gated integration-test pattern** (use for the Docker leg, phase 1+): gate on an env var (`FISSION_TEST_*`) so the test `t.Skip`s when its dependency is absent, and stand up the CI-only infra in `push_pr.yaml` on one matrix leg — mirror `test/integration/otel/` (Collector+Loki) + the `FISSION_TEST_LOKI`/`TEST_GATEWAY_PARENTREF` gates.
  `ory/dockertest/v3` is already a test dep.
- **golangci-lint gotcha** (will hit `run_local.go`): `errcheck` flags unchecked error returns on `Close`/`CloseNow`/Docker client calls — `defer func() { _ = c.Close() }()`.
  Caught a real lint failure on the streaming PR.
- **Go 1.26 idioms enforced** (a `go fix` sweep ran repo-wide): `for i := range n` over `for i:=0;i<n;i++`; no redundant `x := x` loop-var copies; prefer `new(T)` over `ptr.To` (per the repo's [[feedback_use_new_not_ptr_to]]).
- **No `LogStreamer`-style coupling needed** here, but note RFC-0016 added streaming `--follow`; unrelated to `run`.

## As implemented (phases 0–1)

Phases 0 and 1 shipped together on `feat/rfc-0018-function-run-local`.
The result is a working interpreted one-shot inner loop: `fission function run-local --image <env-image> --code f.py [--env <name>] [invoke flags]` starts the real env runtime in Docker, replays `/v2/specialize`, invokes over the shared `DoHTTPRequest` path, prints the response, and tears down (unless `--keep`).

The command is named **`run-local`** (alias `runl`), not a bare `run` — mirroring the existing `run-container` and avoiding ambiguity for users.

All three executor types are supported, dispatched by `--executor` (default `poolmgr`):

- **poolmgr** and **newdeploy** collapse to the same local shape — both run an environment runtime image and load code via the specialize contract — so they share one code path (`resolveEnvRun` → `specialize`).
- **container** runs the user's own `--image` server directly: no `--env`, no `--code`, no bind mount, and **no specialize** call (the image *is* the function server).
  It publishes the function's own port (`--port`, default 8888) and gates readiness with an HTTP probe (`waitForServer`) since there is no specialize call to do so.

What matched the design, and the two deviations worth recording:

- **The CLI does not import `pkg/fetcher`.**
  The design said "build the load request via `NewSpecializeRequest`, then rewrite `LoadReq.FilePath`."
  Importing `pkg/fetcher` (and its `config` subpackage) into the CLI pulls a heavy server-side dependency tree into the `fission` binary for one four-field struct.
  Instead `run_local.go` carries a local `loadRequest` wire struct with the identical JSON tags, and `TestLoadRequestWireContract` asserts byte-for-byte JSON parity against `fetcher.FunctionLoadRequest` (the test file may import `fetcher`; the production code may not).
  This is the contract-regression guard the design called for, realized as a JSON-parity test rather than a direct call.
- **The shared retry helper had to be broadened for the Docker port-proxy race.**
  Phase 0 factored `loadOnlySpecialize`'s connect-refused retry into `httpx.PostWithConnRetry`.
  Against a real container this was insufficient: Docker's userland proxy binds the published host port the instant the container starts, so the specialize dial *connects* (no connection-refused) but the proxy severs it with an `EOF`/reset until the in-container server is actually listening.
  The retry predicate now also covers connection-reset/premature-EOF (`network.Error.IsConnResetError`), which is a genuine robustness improvement for the in-cluster path too (a freshly-Ready pod can reset early connections the same way); specialize is idempotent, so retrying on a reset is safe.

Files: `pkg/fission-cli/cmd/function/{run.go,run_local.go,run_test.go,run_local_docker_test.go}`, the `httpx.PostWithConnRetry` helper + `network.Error.IsConnResetError`, two new flags (`--env-version`, `--keep`) plus reused `--executor`/`--port`/`--code`/`--env`/`--image`, and the `run-local` command registration.
The Docker e2e (`run_local_docker_test.go`) is gated behind `FISSION_RUN_DOCKER_TESTS=1` so it never runs in default CI; the unit flow test (`TestRunLocalFlow`) exercises specialize + invoke end-to-end through the real `httpx`/`DoHTTPRequest` code with only the container engine faked.

### Phases 2–5 (as implemented)

Phases 2, 3, 4 (the local bridges), and 5 followed, all behind the generalized `containerSpec` (multiple bind mounts + published ports):

- **Phase 2 — env bridges + hot reload.**
  `-e KEY=VALUE` (repeatable) and `--env-from <file>` feed the container's env; `--watch` (fsnotify on the source's parent dir, debounced) re-runs `materialize` + `specialize` on each save without restarting the container, serving until Ctrl-C.
  `--watch` is env-executors-only (container functions carry their own prebuilt image).
- **Phase 3 — builder leg.**
  `--build` runs the env's builder image (`run_builder.go`), reproducing buildermgr's contract — stage source under `/packages`, POST `{srcPkgFilename, command}` to `:8001`, collect the artifact — and lays the result at the single deploy target so the runtime specializes it.
  The builder image/command come from `--builder-image`/`--buildcmd` (cluster-less) or the resolved environment.
  The deploy target is `targetFilename(envVersion)`, shared with the interpreted path so a v1 env and a builder env never disagree on where the code lands.
- **Phase 4 — fidelity bridges (local).**
  `--secret`/`--configmap` read the named cluster objects and materialize them (one file per key) at `/secrets/<ns>/<name>` and `/configs/<ns>/<name>`, the fetcher's on-disk layout.
  The temp dirs holding decrypted Secret data are owned by `do()` and reclaimed on every exit path.
  `--remote` (approach C) is **not** built — it is a separate cluster-side architecture (dev-pod + source sync) and warrants its own RFC/PR.
- **Phase 5 — debugger.**
  `--debug-port N` publishes an additional `127.0.0.1:N→N` port for delve/debugpy to attach to.

The shared connect-retry (`httpx.PostWithConnRetry` for specialize, `httpx.WaitReady` for the container/builder readiness probe) is factored onto one backoff core; `utils.FindFreePort` is reused for host ports.
Gated Docker e2e tests cover the env, container-executor, and builder legs end-to-end against a real daemon.

## Phased implementation

0. **Scaffold** — add `run.go`/`run_local.go` skeleton + flags; export `DoHTTPRequest` from `test.go`; factor the specialize retry loop out of `oci_specialize.go` into a shared helper without changing in-cluster behavior. ✅ **shipped** Ships as a hidden/alpha command.
1. **MVP: interpreted, one-shot** — `fission function run-local --env <interpreted> --code f.py [--image …] [invoke flags]`: resolve env image → `docker run` → bind-mount → `/v2/specialize` with retry → single invoke via the shared path with `X-Fission-*` headers → print → teardown. ✅ **shipped** (all executor types via `--executor`: poolmgr/newdeploy specialize, container runs the user image directly).
   Cluster-less with `--image`.
   This alone collapses the loop to container-start + sub-second.
2. **Watch + persistent server** — `--watch` (re-specialize without restart), `--keep`, `--env-from`/`-e`. ✅ **shipped**
3. **Builder leg** — run `Builder.Image` locally, stream build logs, feed the deploy artifact to the runtime. ✅ **shipped** (enables Go/Java/.NET via `--build`).
4. **Fidelity bridges + remote** — `--secret`/`--configmap` materializing real objects into `/secrets`/`/configs`. ✅ **shipped** (local bridges); `--remote` (approach C) **deferred** to its own RFC/PR — a port-forward helper and the cluster dev-pod path are a distinct architecture, not local Docker.
5. **Debug** — `--debug-port` publishes the env image's debug port for delve/debugpy attach. ✅ **shipped**

## Backward compatibility & migration

- Entirely new, additive command — no existing behavior, CRD, server, or Helm surface is touched.
- Works cluster-less (`--image`) and against existing clusters unchanged.
  `run-local` is marked cluster-optional (`cmd.ClusterOptionalAnnotation`), so the root `PersistentPreRunE` does not require a kubeconfig for it; `--env`/`--secret`/`--configmap` (which do need a cluster) return a clear error guiding the user to `--image` when none is configured, and every other command keeps its existing hard-fail behavior.
- Supports **both** the v1 `/specialize` and v2 `/v2/specialize` env contracts and honors `EnvVersion`, so it runs against older published env images.
- A contract-regression test guards against the local mount layout drifting from what the in-cluster fetcher writes, so local and in-cluster loaders cannot silently diverge.

## Test strategy

- **Unit (no Docker).**
  Assert the locally-built specialize request equals `NewSpecializeRequest`'s output after the `FilePath` rewrite; the header builder produces exactly the set in `requesthHeader.go`; flag wiring; watch debounce.
  Use the `localRuntime` fake.
  Matches the existing `pkg/fission-cli/cmd/function/*_test.go` style.
- **Integration (Docker, gated).**
  A tiny fake env image implementing `/v2/specialize` + an echo handler; assert run→specialize→invoke returns the body and round-trips the injected headers, backed by `ory/dockertest/v3`.
  Added to CI as an optional job (Docker-in-CI cost).
- **Contract regression.**
  A test that fails if `NewSpecializeRequest`'s field names/paths change.
- **E2E parity smoke.**
  Run the same source locally and in a kind cluster; diff the responses for a canonical Python/Node sample to prove fidelity empirically.

## Success metrics

- Interpreted re-run after edit (`--watch`): below 1 s P50 (re-specialize only), below 2 s P95.
- Cold local first run (image already pulled): below 5 s to first response.
- Compiled re-run: build time + below 2 s (build time dominated by the env's own toolchain, unchanged).
- Baseline today is 15–120 s — target at least a 15× improvement on the interpreted inner loop.

## Open questions / risks

- Do all published v2 env runtimes accept a **second** `/v2/specialize` on the same process (restart-free hot reload), or must some restart?
  Needs per-env probing; the design degrades gracefully to restart.
- Single-file vs directory `FilePath` expectations differ across env images — confirm the per-env layout against what the in-cluster fetcher writes.
- Bind-mount UID/permission mismatches on distroless/non-root images and macOS Docker Desktop file sharing — may need mode tuning or copying into a container volume for `--watch`.
- Docker prerequisite excludes some users — mitigated by `--remote` (phase 4) and `--image` cluster-less mode.
- macOS bind-mount latency could blunt the sub-second target — consider a container volume instead of a bind-mount for `--watch` if measured slow.
- Whether `run` should optionally synthesize a Function spec (like `run_container.go`'s spec-save) so a successful local run can be promoted to a committed spec — low-cost reuse, decide in phase 2.

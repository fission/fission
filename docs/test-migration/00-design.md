# Integration Test Migration: Bash → Go

Status: approved 2026-05-02. Tracks the live migration in `01-migration-status.md`.
Framework helpers documented in `02-framework-api.md`.

## Context

The Fission integration test suite is 48 bash scripts in `test/tests/` (42 enabled, 6 disabled), executed via GNU `parallel` from `test/run_test.sh` and orchestrated by `test/kind_CI.sh`.
CI runs them on a Kind cluster across three Kubernetes versions (1.28, 1.32, 1.34) after a Skaffold-deployed Fission install.

The bash suite is the only end-to-end coverage we have against a real Kubernetes cluster, but it has known problems: shell-tooling drift between macOS and Linux (`init_tools.sh` exists for this reason), brittle string-grep assertions, hard to debug locally, no shared types with the Go codebase, and an ad-hoc parallel runner that retries up to 8× to mask flakes.

We will replace it with a Go integration suite that uses the same Go conventions as the rest of the codebase, runs on the same Kind cluster, and integrates into the same CI matrix.
The migration is incremental: every PR migrates one or a small group of bash tests and disables the bash counterpart in the same change, so CI is always green at HEAD.

## Decisions

| # | Decision | Choice |
|---|----------|--------|
| 1 | Cluster model for migrated tests | Real Kind cluster |
| 2 | Test framework | `testing` + `testify` |
| 3 | How tests interact with Fission | Hybrid: CLI for mutations, clientset for reads/waits |
| 4 | Layout | New `test/integration/` tree, separate from existing `test/e2e/` (envtest, untouched) |
| 5 | Migration sequencing | Three diverse pilots (`node_hello_http`, `buildermgr`, `canary`), then bulk by category |
| 6 | Bash tests on migration | Marked `#test:disabled` with a comment pointing at the Go test; not deleted until teardown phase |
| 7 | Migration tracking docs | `docs/test-migration/` |

## Architecture

### Directory layout

```
test/integration/
  framework/                    # public package; build tag //go:build integration
    cluster.go                  # Connect() *Framework from KUBECONFIG; cached clientsets
    cli.go                      # f.CLI(t, ctx, args...) — runs in-process fission-cli app (no fork)
    namespace.go                # NewTestNamespace(t) — random ns + cleanup + label
    env.go                      # CreateEnv(t, ns, EnvOptions{Image, Builder, ...})
    function.go                 # CreateFunction, WaitForFunctionReady, Invoke
    package.go                  # CreatePackage, WaitForPackageBuild, WaitForPackageStatus
    httptrigger.go              # CreateRoute
    canary.go                   # CreateCanaryConfig, WaitForCanaryWeights
    timer.go, kubewatcher.go,
    mqtrigger.go                # other trigger helpers (added as tests need them)
    router.go                   # Router(t) HTTP client with retries; honors FISSION_ROUTER env
    poll.go                     # Eventually(t, fn, opts) over wait.PollUntilContextTimeout
    diag.go                     # OnFailure dump (events, pod logs, CRD YAML) → t.TempDir
    images.go                   # RuntimeImages from env vars; single source of truth
    cleanup.go                  # cluster-scoped label-selector sweep
  testdata/                     # //go:embed FS — only what tests need
    nodejs/hello/...
    nodejs/hello-callback/...
    python/sourcepkg/...
    python/hello/...
    go/hello/...
    misc/...                    # fixtures vendored from raw.githubusercontent.com/fission/examples
  suites/
    common/                     # phase-agnostic; runs in poolmgr CI step
      node_hello_http_test.go   # PILOT 1
      buildermgr_test.go        # PILOT 2
      canary_test.go            # PILOT 3
      ... bulk-migrated tests ...
    poolmgr/                    # CI phase 1: -parallel up to 6
      backend_poolmgr_test.go
      ...
    newdeploy/                  # CI phase 2: -parallel up to 3
      backend_newdeploy_test.go
      scale_change_test.go
      ...
docs/test-migration/            # in-flight engineering docs; deleted at teardown
  00-design.md                  # this design
  01-migration-status.md        # live checklist (bash → Go) with PR refs
  02-framework-api.md           # helper-by-helper docs as the framework grows
```

`test/e2e/` (envtest-based, in-process services) is **not touched** by this migration.
`test/tests/` (existing bash) shrinks PR by PR.

### Framework API conventions

- All framework helpers take `t *testing.T` first and call `t.Helper()`.
- All resource-creating helpers register `t.Cleanup` automatically; tests do not write defers.
- Mutations through `f.CLI(t, ctx, args...)`; reads/waits through clientset.
- `t.Parallel()` is the default in suite tests; framework state is per-test, no globals.
- `t *testing.T` is the only failure surface — helpers `t.Fatal` on unrecoverable, `t.Error` on recoverable, never return errors the test must check.

### CLI invocation

Reuse the in-process pattern from `test/e2e/framework/cli/cli.go`: build the `cmd.fission-cli/app.App` once per `*Framework`, invoke commands by setting `os.Args` and calling `app.Run`.
This avoids subprocess fork/exec overhead and gives us CLI-coverage in CI for free.
The helper captures stdout/stderr per call and returns them to the test for optional assertion.

### Test isolation

Each test calls:

```go
ns := f.NewTestNamespace(t)
```

Which:

1. Creates `fission-it-<sanitized-test-name>-<5char-rand>` in the cluster.
2. Labels it `fission.io/test-id=<id>` (parity with bash `TEST_ID`).
3. Registers `t.Cleanup` that deletes the namespace, **unless** `TEST_NOCLEANUP=1` (parity with bash).
4. Registers `diag.OnFailure(t, ns)` that runs only on `t.Failed()` and writes diagnostics to `$LOG_DIR/<test>/`.

Cluster-scoped Fission CRs (Functions/Envs/etc. in non-default namespaces) carry the same label; a `TestMain` post-pass does a label-selector sweep to catch anything orphaned.

### Diagnostics

`diag.OnFailure` dumps:

- `kubectl describe pod` for all pods in the test namespace.
- Recent events in the test namespace.
- Function/Package/Env CRs in the test namespace as YAML.
- Container logs for every pod (function pods, builder sidecar, fetcher sidecar).

Output goes to `$LOG_DIR/<test-name>/` (default `test/integration/logs/`).
CI uploads this directory as an artifact, mirroring the existing fission-dump/kind-logs upload.

### CI integration

`.github/workflows/push_pr.yaml` — after the existing port-forward step (line 127–129), insert two new steps **before** `./test/kind_CI.sh`:

```yaml
- name: Go integration tests (poolmgr / common phase)
  run: |
    go test -tags=integration -timeout=30m -p 1 -parallel 6 -v \
      ./test/integration/suites/common/... \
      ./test/integration/suites/poolmgr/...

- name: Go integration tests (newdeploy phase)
  run: |
    go test -tags=integration -timeout=30m -p 1 -parallel 3 -v \
      ./test/integration/suites/newdeploy/...
```

Then `./test/kind_CI.sh` runs unchanged — its embedded test list is what shrinks each PR.
Both Go and bash tests share the same deployed Fission, the same `KUBECONFIG`, the same port-forwarded router (`127.0.0.1:8888`), and the same runtime image env vars.

The build tag `//go:build integration` keeps the suite out of `make test-run` (the unit-test gate), so `make check` and the existing test workflow are unaffected.

### Examples handling

Migrated Go tests do **not** depend on `fission/examples` clone.
We vendor the ~10 files we actually use into `test/integration/testdata/` and embed via `//go:embed`.
Tests write the embedded content to `t.TempDir()` and pass that path to the CLI.

Two bash tests today fetch fixtures from `raw.githubusercontent.com/fission/examples/...`; their fixtures get vendored too — no network reach in tests.

The CI step that clones `fission/examples` stays until the bash teardown phase, since unmigrated bash tests still need it.

## Critical files

Files we **read** to design helpers (existing patterns we mirror):

- `test/e2e/framework/framework.go` — envtest framework; we copy the constructor pattern but swap envtest for KUBECONFIG.
- `test/e2e/framework/cli/cli.go` — in-process CLI invocation; we lift wholesale.
- `test/utils.sh` — bash helper functions (`test_fn`, `wait_for_builder`, `waitBuild`, `clean_resource_by_id`); each one maps to a Go helper.
- `test/test_utils.sh` — Helm/diagnostics helpers; mostly unneeded in Go since CI deploys via Skaffold, but the dump-on-failure logic informs `diag.go`.
- `test/run_test.sh` and `test/kind_CI.sh` — orchestration we replace with `go test` + the two CI steps above.
- `pkg/crd/client.go` — `ClientGenerator`, the canonical way to build clientsets in this codebase; framework wraps it.
- `pkg/apis/core/v1/*.go` — CRD types; framework's `Create*` helpers build these directly.

Files we **modify**:

- `.github/workflows/push_pr.yaml` — add two `go test` steps before `kind_CI.sh`.
- `test/kind_CI.sh` — remove migrated tests from the test list, PR by PR. Eventually deleted (or reduced to image preload).
- `test/tests/<each>.sh` — add `#test:disabled` and a comment pointing at the Go test (PR by PR).

Files we **create**:

- `test/integration/framework/*.go` — listed above.
- `test/integration/testdata/**` — vendored fixtures.
- `test/integration/suites/{common,poolmgr,newdeploy}/*_test.go` — the migrated tests.
- `docs/test-migration/00-design.md`, `01-migration-status.md`, `02-framework-api.md`.

Files we **do not touch**:

- `test/e2e/**` — existing envtest suite.
- `pkg/generated/**`, `zz_generated_*.go`, `crds/v1/*.yaml` — generated.
- `hack/runtests.sh`, `hack/update-codegen.sh` — unit-test/codegen scripts.

## Migration phases

### Phase 0 — Tracking docs (1 PR, before any code)

Create `docs/test-migration/00-design.md` (this file), `01-migration-status.md` (table: bash file | Go test | PR | status), `02-framework-api.md` (stub, grows with framework).
This PR has no code changes — establishes the artifact and review pattern.

### Phase 1 — Framework + Pilot 1 (1 PR)

Create the framework with **only** the helpers needed for `test_node_hello_http.sh`:
`Connect`, `NewTestNamespace`, `CLI`, `CreateEnv`, `CreateFunction`, `CreateRoute`, `Router`, `WaitForFunctionReady`, `Eventually`, `OnFailure`.
Migrate `test/tests/test_node_hello_http.sh` → `test/integration/suites/common/node_hello_http_test.go` as `TestNodeHelloHTTP`.
Add the two CI steps to `push_pr.yaml`.
Mark `test/tests/test_node_hello_http.sh` as `#test:disabled` with comment pointing at the Go test.

Validates: framework wiring, CLI helper, CI step, isolation, diagnostics.

### Phase 2 — Pilot 2: builder (1 PR)

Migrate `test_buildermgr.sh` → `suites/common/buildermgr_test.go`.
Adds: `EnvOptions{Builder}`, source-archive upload via CLI, `CreatePackage` with `--src`, `WaitForPackageBuild`, `WaitForPackageStatus`.
Disable bash counterpart.

Validates: builder/package paths, multi-step waits, archive handling.

### Phase 3 — Pilot 3: canary (1 PR)

Migrate `test_canary.sh` → `suites/common/canary_test.go` (success scenario + rollback scenario as two `t.Run` subtests).
Adds: `CreateCanaryConfig`, weight-shift polling helpers, two-version function setup.
Disable bash counterpart.

Validates: multi-resource orchestration, time-based waits, rollback scenarios.

### Phase 4 — Bulk migration (~10 PRs, 3–5 tests each)

Group by category for review-friendliness. See `01-migration-status.md` for the live working list and assignments.

### Phase 5 — Disabled-test triage (1 PR)

For each `#test:disabled` bash test, decide: migrate as `t.Skip("reason")`, migrate behind env-gate, or delete with rationale recorded in `01-migration-status.md`.

### Phase 6 — Bash teardown (1 PR)

When `kind_CI.sh`'s active list is empty: delete `test/tests/*.sh`, `test/run_test.sh`, `test/utils.sh`, `test/test_utils.sh`, `test/init_tools.sh`; reduce or delete `test/kind_CI.sh`; remove the `examples/` clone step from `push_pr.yaml`.

## Disabling bash tests

Each migration PR adds at the top of the bash file:

```sh
#!/bin/bash
#test:disabled
# Migrated to Go: test/integration/suites/<area>/<name>_test.go (Test<Name>)
# This script is retained for reference until the bash teardown phase.
```

The `#test:disabled` directive is honored by `test/run_test.sh:41` today, so no runner changes are needed.

## Verification

### Local

```bash
kind create cluster --config kind.yaml
kubectl create ns fission && make create-crds
SKAFFOLD_PROFILE=kind make skaffold-deploy
kubectl port-forward svc/router 8888:80 -nfission &

export NODE_RUNTIME_IMAGE=ghcr.io/fission/node-env-22
# ... other runtime image env vars matching kind_CI.sh lines 23-35 ...
export FISSION_ROUTER=127.0.0.1:8888

go test -tags=integration -timeout=30m -v ./test/integration/suites/common/...
```

A failed run leaves diagnostics in `test/integration/logs/<test>/` and resources in the cluster if `TEST_NOCLEANUP=1` is set.

### CI

Phase 1 PR is the verification: the new Go test passes on all three K8s versions (1.28/1.32/1.34), bash CI still green for the rest of the suite, fission-dump/kind-logs/integration-logs artifacts uploaded on failure.
Each subsequent migration PR is verified by the same matrix.

### Per-PR contract

A migration PR is acceptable iff:

1. The new Go test passes in CI on all three K8s versions.
2. The bash counterpart is marked `#test:disabled` with the migration comment.
3. `kind_CI.sh`'s active list no longer references the migrated bash test (if it was listed there).
4. `docs/test-migration/01-migration-status.md` reflects the migration.
5. The remaining bash suite is still green.

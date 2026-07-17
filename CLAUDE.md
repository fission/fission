## CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Fission is a Kubernetes-native serverless framework, written in Go (`github.com/fission/fission`, Go 1.26).
The control plane is shipped as a single multi-headed binary (`fission-bundle`) that runs one of several subsystems based on flags.
Functions execute inside per-environment pods that the executor manages; the user-facing CLI is `fission`.

## Common commands

Build / lint / test (run from repo root):
- `make build-fission-cli` — build the `fission` CLI via goreleaser (snapshot, single target).
  `make install-fission-cli` copies it to `/usr/local/bin/fission`.
- `make code-checks` — `golangci-lint run` (config in `.golangci.yaml`; goimports local prefix `github.com/fission/fission`), plus `make verify-gomod`.
- `make verify-gomod` — fails if `go.mod` does not keep direct and indirect requirements in separate blocks (`go mod tidy` does NOT enforce this); runs in CI's "Verify dependencies" step.
  See "Dependency management".
- `make license` — add the SPDX header (`SPDX-FileCopyrightText` + `SPDX-License-Identifier: Apache-2.0`) to any in-scope source file (`.go`/`.sh`/`.py`/`Dockerfile*`) missing one.
  `make license-check` is the CI gate (runs in `lint.yaml`); run it before pushing.
- `make test-run` — runs `hack/runtests.sh`: pulls `setup-envtest` Kubebuilder assets for k8s 1.32.x, then `go test -race -coverprofile=coverage.txt ./...`.
  Requires `KUBEBUILDER_ASSETS` (the script sets it).
- `make check` — full local gate: `test-run` + `build-fission-cli` + `clean`.
- Run a single Go test: `go test -race -run TestName ./pkg/router/...` (set `KUBEBUILDER_ASSETS=$(go tool setup-envtest -p path use 1.32.x)` first if the package needs envtest).

Code generation (run after editing `pkg/apis/core/v1/types.go`):
- `make codegen` — regenerates clientset/listers/informers under `pkg/generated/` (via `hack/update-codegen.sh`) and deepcopy via controller-gen.
  Never hand-edit `pkg/generated/` or `zz_generated_*.go`.
- `make generate-crds` — regenerates CRD YAMLs under `crds/v1/` from the Go types.
- `make generate-webhooks` — regenerates webhook configs into the Helm chart from `// +kubebuilder:webhook` markers in `pkg/webhook/`.
- `make all-generators` — runs all generators including swagger and CLI/CRD ref docs.

Local cluster development (skaffold + kind):
- `kind create cluster --config kind.yaml` then `kubectl create ns fission && make create-crds`.
- `SKAFFOLD_PROFILE=kind make skaffold-deploy` — builds linux/amd64 images via goreleaser, copies per-binary Dockerfiles into `dist/*_linux_amd64_v1/`, and Helm-installs `charts/fission-all`.
  Other profiles: `kind-debug` (pprof + debugEnv), `kind-ci` (full observability), `kind-opentelemetry`.

Integration tests (`test/integration/`, Go + testify, build tag `//go:build integration`, expect a running Fission cluster reachable via `KUBECONFIG`):
- No `kubectl port-forward` needed: the framework registers `router.fission` / `router-internal.fission` / `mcp.fission` in a go-portless registry that port-forwards in-process (SPDY, per-dial pod re-resolution) from the `FISSION_NAMESPACE` namespace (default `fission`).
  Setting `FISSION_ROUTER` / `FISSION_ROUTER_INTERNAL` / `FISSION_MCP_BASE_URL` overrides a route with a fixed address (for hand-managed forwards or non-default installs).
- `/fission-function/<ns>/<name>` moved off the public listener after GHSA-3g33-6vg6-27m8 (see Architecture).
  Tests that invoke functions go through the framework's `Router(t)` HTTP client which auto-routes those paths to the internal listener, or dial `f.RouterInternalBaseURL()` via `f.HTTPClient()` — base-URL hosts are portless route names, so plain `http.Client`s cannot resolve them.
- Export `FISSION_INTERNAL_AUTH_SECRET` (read from `kubectl get secret fission-internal-auth -n fission -o jsonpath='{.data.secret}' | base64 -d`) so the framework's transport signs requests on the internal listener — leave unset to test the verifier's pass-through mode.
- The MCP test (`TestMCPToolsListAndCall`) needs `svc/mcp` in the cluster (`mcp.enabled`/`mcp.allowInsecure` are on in the kind/kind-ci skaffold profiles); it `t.Skip`s when the endpoint is unreachable.
- Run the full suite: `go test -tags=integration -timeout=30m -parallel 6 -v ./test/integration/suites/common/...`.
  Set runtime/builder image env vars (`NODE_RUNTIME_IMAGE`, `PYTHON_RUNTIME_IMAGE`, etc.) — tests `t.Skip` when their required image is unset.
  `TEST_NOCLEANUP=1` leaves resources for debugging.
- Run a single test: `go test -tags=integration -run TestNodeHelloHTTP -v ./test/integration/suites/common/...`.
- `suites/serial/` holds tests that mutate cluster-wide control-plane state (e.g. restarting the executor to exercise `AdoptExistingResources`) and so cannot run alongside the parallel `common` suite.
  CI runs them after `common/` in the same step, single-package: `go test -tags=integration -p 1 ./test/integration/suites/serial/...`.
  Restart the executor via `framework.SetExecutorEnv` + `WaitForExecutorRollout` (a completed rollout means the new pod's adopt pass has run, since `/readyz` gates on `cachesSynced`, set after `runAdoptCleanup`).
- Framework reference + "Adding a new test" 12-step guide: `docs/test-migration/02-framework-api.md`.
- The previous bash test suite (`test/tests/`, `test/run_test.sh`, `test/kind_CI.sh`, `test/utils.sh`, etc.) was retired in 2026-05; the migration history lives in `docs/test-migration/`.

## Testing conventions

When writing or modifying tests, follow `.claude/resources/test-writing-guidelines.md`.
Key points: use `testify` (`require` for preconditions, `assert` for independent checks) over hand-written comparisons; use `t.Context()` instead of `context.Background()`; prefer fake clientsets over `envtest` for unit tests; table-driven subtests with `t.Parallel()`; `testing/synctest` bubbles for time-dependent code (no sleeps, no clock seams); `pgregory.net/rapid` for property-based tests (never the frozen `testing/quick`); fuzz parser/verifier boundaries; crash-point enumeration and `porcupine` for concurrency/consistency claims.

## Dependency management

When adding, upgrading, or removing Go dependencies, follow `.claude/resources/go-mod-conventions.md`.
Key point: `go.mod` keeps **direct** requirements in the first `require (...)` block and **indirect** (`// indirect`) ones in a second block — `go mod tidy` does NOT move entries between blocks, so a `go get` that lands a direct dep in the indirect block must be moved by hand.
`make verify-gomod` (CI-enforced in `lint.yaml`) guards the layout.

## Repo-specific playbooks

Deep operational knowledge lives under `.claude/resources/fission-*.md`, loaded on demand by the generic CI/dep/triage skills (`debug-ci`, `go-deps-security-sweep`, `bump-ci-tool-versions`, etc.).
Start there when CI is red or you're touching the build/dependency machinery:
- `fission-ci-failure-patterns.md` — symptom→cause tables, NetworkPolicy selectors/allowlist, CI-only Helm flags via the kind-ci profile.
- `fission-build-pipeline.md` — buildermgr→builder→fetcher→storagesvc flow, `/packages` permissions, why env-builder images aren't rebuilt per-PR.
- `fission-integration-test-quirks.md` — builder/runtime readiness races, pod-label conventions, framework gotchas.
- `fission-dep-groups.md` — k8s/controller-runtime/KEDA/code-generator lockstep, the `prometheus/common` exclude, codegen after a platform bump.
- `fission-workflow-tools.md` — pinned CI tool inventory and intentional stale pins.

## Architecture

`cmd/fission-bundle/main.go` is the dispatch point — the same binary becomes a different service depending on which `--<flag>` is passed (`--routerPort`, `--executorPort`, `--kubewatcher`, `--timer`, `--mqt`, `--mqt_keda`, `--builderMgr`, `--canaryConfig`, `--webhookPort`, `--storageServicePort`, `--mcpPort`).
Each flag dispatches to a `Start` function in the corresponding `pkg/` package.
The Helm chart deploys this binary multiple times with different flags.
Other binaries: `cmd/fission-cli` (user CLI), `cmd/builder` (per-env build sidecar), `cmd/fetcher` (per-env code-fetch sidecar), `cmd/preupgradechecks`, `cmd/reporter`.

Request path for an HTTP-triggered function:
1. `pkg/router` receives the HTTP request, resolves the trigger to a function via `functionReferenceResolver`, looks up a service URL in `functionServiceMap`, and proxies the request.
   The mux is a `mutablemux` that hot-swaps routes when triggers change.
2. On a cache miss the router asks `pkg/executor` (over HTTP, see `pkg/executor/client`) for a function service URL.
3. `pkg/executor/executortype/{poolmgr,newdeploy,container}` provide the three execution backends.
   `poolmgr` is the default warm-pool path: generic env pods are created up front (`gpm`/`gp`/`poolpodcontroller`) and specialized on demand by calling `fetcher` to load the user's package; `newdeploy` creates a Deployment+Service per function; `container` runs an arbitrary user container image.
4. `pkg/buildermgr` watches `Package` CRDs in `pending` state and runs the env's `builder` sidecar (which calls into `pkg/builder`) to produce a deployment archive, uploaded to `pkg/storagesvc` (local FS or S3).

Router listener split (post-GHSA-3g33-6vg6-27m8): the router runs **two** HTTP listeners — public (port 8888 on `svc/router`) for user HTTPTriggers + `/router-healthz` + `/_version`, and internal (port 8889 on the ClusterIP-only `svc/router-internal`) for `/fission-function/<ns>/<name>` invocations.
The internal listener is wrapped with `pkg/auth/hmac.ServiceVerifier` (key derived for `ServiceRouterInternal` from `FISSION_INTERNAL_AUTH_SECRET`); empty secret = pass-through.
Route updates are incremental (RFC-0013): reconcilers apply per-event diffs to `pkg/router/routetable` (handlers behind an atomic `HandlerRef`, so canary weight ticks and function updates are pointer swaps with no mux rebuild), only route-SHAPE changes signal the debounced materializer, and a 60s cache-fed resync is the drift guard (`fission_router_route_resync_drift_total`, CI bar zero) that also re-arms a failed materialize.
Change detection keys on **Generation** (matching the reconcilers' `GenerationChangedPredicate`), not ResourceVersion.
Anything affecting mux construction (e.g. `USE_ENCODED_PATH`) must be applied **inside `newListenerMuxes`** — applied only at startup it's silently dropped on the first atomic mux swap.
Route precedence is specified (hosted > host-less, exact > prefix, longest prefix, creationTimestamp tiebreak); duplicate shapes shadow the younger trigger and mark it `RouteAdmitted=False/RouteConflict`.

Other trigger paths invoke the router URL: `pkg/kubewatcher` (watches arbitrary k8s resources), `pkg/timer` (cron), `pkg/mqtrigger` (Kafka/NATS/etc., plus a KEDA-driven scaler manager via `--mqt_keda`), `pkg/canaryconfigmgr` (gradual traffic shifting between two functions on an HTTPTrigger).
They publish to `/fission-function/...` on the internal listener; `cmd/fission-bundle/main.go` resolves `ROUTER_INTERNAL_URL` from the env once and forwards it as the `routerUrl` argument into each subsystem's `Start` function — keep library constructors like `publisher.MakeWebhookPublisher` deterministic (no env reads) so unit tests with `httptest.Server` aren't broken.

MCP subsystem (`pkg/mcp`, `--mcpPort`): exposes opted-in Functions as Model Context Protocol tools for LLM agents over the official `github.com/modelcontextprotocol/go-sdk` Streamable HTTP transport (`/mcp` on the ClusterIP-only `svc/mcp`, Helm `mcp.enabled`, off by default).
A function opts in via the optional `FunctionSpec.Tool *ToolConfig` field (presence is the on switch, like `Streaming`; `Description` + raw-JSON `InputSchema` + optional `ToolName`).
`Start` mirrors `pkg/timer` but runs **without** leader election — every replica reconciles `Function` CRDs into its own in-memory `*mcp.Registry` and serves the full tool list, so each must reconcile (the SDK's `*mcp.Server` mutex serializes `AddTool`/`RemoveTools` against serving).
`tools/call` is **buffered, not streamed** (the SDK returns one `CallToolResult`) and is proxied to `/fission-function/...` on the router internal listener built with `utils.UrlForFunction` (which **folds the default namespace** to `/fission-function/<name>`) and signed with the same `ServiceRouterInternal` HMAC key as the other publishers.
AuthZ: bearer JWT (`JWT_SIGNING_KEY`, shared with the router secret) whose `allowed_namespaces` claim scopes `tools/list`/`tools/call`; it **fails closed** — `Start` refuses to run without a key unless `MCP_ALLOW_INSECURE=true`, and the chart fails render when `mcp.enabled && !authentication.enabled && !mcp.allowInsecure`.
RBAC is read-only on functions + functions/status; `/readyz` gates on the Function cache sync (via a manager `RunnableFunc`) so a warming replica isn't added to the Service.

EndpointSlice data plane (RFC-0002, ON by default): with `router.endpointSliceCache.mode=on` (default; `off` keeps the legacy executor-RPC plane) the router serves poolmgr **warm** traffic from a slice-fed endpoint index (`pkg/router/endpointcache`, one informer per replica, no leader election) instead of RPC-ing the executor per request — "router admits, executor provisions".
The executor publishes specialized pods via an async headless selector Service per function (`pkg/executor/executortype/poolmgr/gp_service.go`) and the `fission.io/served` + `fission.io/function-generation` labels; the cold-start RPC path (~100ms) is byte-identical with gates on or off.
Resolution is behind `AddressResolver` (`pkg/router/resolver*.go`); `endpointLB` (default false) additionally dials newdeploy/container pod IPs directly.
One CI leg pins the gates off (`push_pr.yaml` "Pin legacy data plane") so the legacy path stays covered.

CRDs live in `pkg/apis/core/v1/` (`Function`, `Package`, `Environment`, `HTTPTrigger`, `KubernetesWatchTrigger`, `MessageQueueTrigger`, `TimeTrigger`, `CanaryConfig`).
Validation lives in the same package (`validation.go`).
When adding a new CRD type, follow the 10-step checklist in the comment at the top of `pkg/apis/core/v1/types.go` (create spec → type → list → register → CRUD interface → regenerate).
`pkg/crd/client.go` wires the typed clients via `ClientGenerator`, which is what every `Start` function in `fission-bundle` receives.
`pkg/webhook/` is a controller-runtime validating/mutating admission webhook for those CRDs; webhook YAML is generated from kubebuilder markers into `charts/fission-all/templates/webhook-server/`.

The CLI (`cmd/fission-cli` + `pkg/fission-cli/`) talks to Kubernetes directly through the generated clientset rather than going through a controller — it creates/updates the CRDs and the controllers in `fission-bundle` reconcile.

## Things that bite

- After editing `pkg/apis/core/v1/types.go`, you must run `make codegen` and `make generate-crds`; CI will fail otherwise.
  If you also change webhook markers, run `make generate-webhooks`.
- `pkg/generated/`, `zz_generated_*.go`, and CRD YAMLs in `crds/v1/` are generated — edit the source types, not the output.
- `hack/runtests.sh` deletes all Fission CRs in the `default` namespace of `$KUBECONFIG` if a `ok-to-destroy` configmap exists there.
  Don't point it at a shared cluster.
- `skaffold-deploy` depends on `skaffold-prebuild`, which builds linux/amd64 binaries with goreleaser into `dist/` and copies Dockerfiles in.
  If a build looks stale, `make clean` and rerun.
- E2E tests on macOS require GNU coreutils on `PATH` — BSD versions silently behave differently.
- New source files need an SPDX license header or the `lint` CI job fails (`make license-check`).
  Run `make license` to add it (template in `hack/license-header.tmpl`); the legacy 15-line Apache block is gone — do not reintroduce it.
  Generated code gets its header from `hack/boilerplate.go.txt`, so that file (not the output) is the source of truth for generated headers.
- Any **new pod that calls the router internal listener** (port 8889) must be added to the `from` allowlist in `charts/fission-all/templates/router/networkpolicy.yaml` by its `svc:` label, or its requests are silently dropped in CI (`dial tcp <clusterIP>:8889: i/o timeout`).
  Full debugging detail — and the gotcha that `fission`-namespace pod logs (e.g. MCP) are in the CI `kind-logs-<run>-<ver>` artifact, not the `default`-scoped dump — is in `.claude/resources/fission-ci-failure-patterns.md` / `fission-integration-test-quirks.md`.
- When building a `/fission-function/<ns>/<name>` URL in Go, use `utils.UrlForFunction(name, namespace)` — it **folds the default namespace** to `/fission-function/<name>`, which is the form the router actually registers.
  A hardcoded `/fission-function/default/<name>` does not resolve.
- Poolmgr request accounting has **two disjoint modes that must never mix**: router-admitted requests (index `Admit`) decrement via the `ResolvedEntry.Release` closure, executor-resolved requests via the UnTap RPC into `PoolCache.activeRequests`.
  Calling Release for an executor-resolved entry (or skipping UnTap for one) corrupts the executor's concurrency accounting — when touching `pkg/router/transport.go` or the resolvers, check which mode owns the entry (`Release != nil` ⟺ router-admitted).
- gorilla/mux's `Route.Methods()` **uppercases the slice it is handed in place** and keeps it as the route's matcher — passing a shared slice (an informer-owned object's field, or the route table's canonical spec) mutates the owner AND races the still-serving previous mux's matcher under churn.
  Always pass a clone (`registerRouteShape` does); the race only surfaces under `-race` with concurrent build+serve.
- `EXECUTOR_SPECIALIZATION_CONCURRENCY` bounds **one semaphore shared by all executor types**: a small bound lets newdeploy's minutes-long `waitForDeployment` holds starve poolmgr's ~100ms specializations (head-of-line blocking; surfaced as mass cold-start timeouts in CI).
  Leave it 0 (unbounded) unless poolmgr-only pressure is the proven problem.

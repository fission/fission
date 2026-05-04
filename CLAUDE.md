## CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Fission is a Kubernetes-native serverless framework, written in Go (`github.com/fission/fission`, Go 1.26). The control plane is shipped as a single multi-headed binary (`fission-bundle`) that runs one of several subsystems based on flags. Functions execute inside per-environment pods that the executor manages; the user-facing CLI is `fission`.

## Common commands

Build / lint / test (run from repo root):
- `make build-fission-cli` — build the `fission` CLI via goreleaser (snapshot, single target). `make install-fission-cli` copies it to `/usr/local/bin/fission`.
- `make code-checks` — `golangci-lint run` (config in `.golangci.yaml`; goimports local prefix `github.com/fission/fission`).
- `make test-run` — runs `hack/runtests.sh`: pulls `setup-envtest` Kubebuilder assets for k8s 1.30.x, then `go test -race -coverprofile=coverage.txt ./...`. Requires `KUBEBUILDER_ASSETS` (the script sets it).
- `make check` — full local gate: `test-run` + `build-fission-cli` + `clean`.
- Run a single Go test: `go test -race -run TestName ./pkg/router/...` (set `KUBEBUILDER_ASSETS=$(go tool setup-envtest -p path use 1.30.x)` first if the package needs envtest).

Code generation (run after editing `pkg/apis/core/v1/types.go`):
- `make codegen` — regenerates clientset/listers/informers under `pkg/generated/` (via `hack/update-codegen.sh`) and deepcopy via controller-gen. Never hand-edit `pkg/generated/` or `zz_generated_*.go`.
- `make generate-crds` — regenerates CRD YAMLs under `crds/v1/` from the Go types.
- `make generate-webhooks` — regenerates webhook configs into the Helm chart from `// +kubebuilder:webhook` markers in `pkg/webhook/`.
- `make all-generators` — runs all generators including swagger and CLI/CRD ref docs.

Local cluster development (skaffold + kind):
- `kind create cluster --config kind.yaml` then `kubectl create ns fission && make create-crds`.
- `SKAFFOLD_PROFILE=kind make skaffold-deploy` — builds linux/amd64 images via goreleaser, copies per-binary Dockerfiles into `dist/*_linux_amd64_v1/`, and Helm-installs `charts/fission-all`. Other profiles: `kind-debug` (pprof + debugEnv), `kind-ci` (full observability), `kind-opentelemetry`.

Integration tests (`test/integration/`, Go + testify, build tag `//go:build integration`, expect a running Fission cluster with `kubectl port-forward svc/router 8888:80 -nfission`):
- Run the full suite: `go test -tags=integration -timeout=30m -parallel 6 -v ./test/integration/suites/common/...`. Set runtime/builder image env vars (`NODE_RUNTIME_IMAGE`, `PYTHON_RUNTIME_IMAGE`, etc.) — tests `t.Skip` when their required image is unset. `TEST_NOCLEANUP=1` leaves resources for debugging.
- Run a single test: `go test -tags=integration -run TestNodeHelloHTTP -v ./test/integration/suites/common/...`.
- Framework reference + "Adding a new test" 12-step guide: `docs/test-migration/02-framework-api.md`.
- The previous bash test suite (`test/tests/`, `test/run_test.sh`, `test/kind_CI.sh`, `test/utils.sh`, etc.) was retired in 2026-05; the migration history lives in `docs/test-migration/`.

## Architecture

`cmd/fission-bundle/main.go` is the dispatch point — the same binary becomes a different service depending on which `--<flag>` is passed (`--routerPort`, `--executorPort`, `--kubewatcher`, `--timer`, `--mqt`, `--mqt_keda`, `--builderMgr`, `--canaryConfig`, `--webhookPort`, `--storageServicePort`, `--logger`). Each flag dispatches to a `Start` function in the corresponding `pkg/` package. The Helm chart deploys this binary multiple times with different flags. Other binaries: `cmd/fission-cli` (user CLI), `cmd/builder` (per-env build sidecar), `cmd/fetcher` (per-env code-fetch sidecar), `cmd/preupgradechecks`, `cmd/reporter`.

Request path for an HTTP-triggered function:
1. `pkg/router` receives the HTTP request, resolves the trigger to a function via `functionReferenceResolver`, looks up a service URL in `functionServiceMap`, and proxies the request. The mux is a `mutablemux` that hot-swaps routes when triggers change.
2. On a cache miss the router asks `pkg/executor` (over HTTP, see `pkg/executor/client`) for a function service URL.
3. `pkg/executor/executortype/{poolmgr,newdeploy,container}` provide the three execution backends. `poolmgr` is the default warm-pool path: generic env pods are created up front (`gpm`/`gp`/`poolpodcontroller`) and specialized on demand by calling `fetcher` to load the user's package; `newdeploy` creates a Deployment+Service per function; `container` runs an arbitrary user container image.
4. `pkg/buildermgr` watches `Package` CRDs in `pending` state and runs the env's `builder` sidecar (which calls into `pkg/builder`) to produce a deployment archive, uploaded to `pkg/storagesvc` (local FS or S3).

Other trigger paths invoke the router URL: `pkg/kubewatcher` (watches arbitrary k8s resources), `pkg/timer` (cron), `pkg/mqtrigger` (Kafka/NATS/etc., plus a KEDA-driven scaler manager via `--mqt_keda`), `pkg/canaryconfigmgr` (gradual traffic shifting between two functions on an HTTPTrigger).

CRDs live in `pkg/apis/core/v1/` (`Function`, `Package`, `Environment`, `HTTPTrigger`, `KubernetesWatchTrigger`, `MessageQueueTrigger`, `TimeTrigger`, `CanaryConfig`). Validation lives in the same package (`validation.go`). When adding a new CRD type, follow the 10-step checklist in the comment at the top of `pkg/apis/core/v1/types.go` (create spec → type → list → register → CRUD interface → regenerate). `pkg/crd/client.go` wires the typed clients via `ClientGenerator`, which is what every `Start` function in `fission-bundle` receives. `pkg/webhook/` is a controller-runtime validating/mutating admission webhook for those CRDs; webhook YAML is generated from kubebuilder markers into `charts/fission-all/templates/webhook-server/`.

The CLI (`cmd/fission-cli` + `pkg/fission-cli/`) talks to Kubernetes directly through the generated clientset rather than going through a controller — it creates/updates the CRDs and the controllers in `fission-bundle` reconcile.

## Things that bite

- After editing `pkg/apis/core/v1/types.go`, you must run `make codegen` and `make generate-crds`; CI will fail otherwise. If you also change webhook markers, run `make generate-webhooks`.
- `pkg/generated/`, `zz_generated_*.go`, and CRD YAMLs in `crds/v1/` are generated — edit the source types, not the output.
- `hack/runtests.sh` deletes all Fission CRs in the `default` namespace of `$KUBECONFIG` if a `ok-to-destroy` configmap exists there. Don't point it at a shared cluster.
- `skaffold-deploy` depends on `skaffold-prebuild`, which builds linux/amd64 binaries with goreleaser into `dist/` and copies Dockerfiles in. If a build looks stale, `make clean` and rerun.
- E2E tests on macOS require GNU coreutils on `PATH` — BSD versions silently behave differently.

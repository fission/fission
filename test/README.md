# Test suites

Three flavors of tests live under `test/`. Pick the one matching the layer you're touching.

| Directory | Framework | When to use |
|---|---|---|
| `test/integration/` | Go + `testify`, build tag `//go:build integration` | End-to-end Fission behavior against a real Kind cluster (router, executor, buildermgr, storagesvc, CRDs). |
| `test/e2e/` | Go + envtest (in-process kube-apiserver) | Controller-level reconcile loops that don't need a real cluster. |
| `test/upgrade_test/` | Bash + helm | Upgrade-from-stable smoke run via `.github/workflows/upgrade_test.yaml`. |
| `test/benchmark/` | Go | Throughput / latency benchmarks (`picasso.go`). |

## Integration tests (`test/integration/`)

The Go integration suite replaced the previous bash test runner. Every PR runs it on Kind across three Kubernetes versions (1.28, 1.32, 1.34) via `.github/workflows/push_pr.yaml`.

### Running locally

```bash
# 1. Bring up a Kind cluster + Skaffold-deploy Fission (one-time per test session)
kind create cluster --config kind.yaml
kubectl create ns fission && make create-crds
SKAFFOLD_PROFILE=kind-ci make skaffold-deploy

# 2. Port-forward the router
kubectl port-forward svc/router 8888:80 -nfission &

# 3. Set required image env vars (or unset to t.Skip those tests)
export NODE_RUNTIME_IMAGE=ghcr.io/fission/node-env-22
export NODE_BUILDER_IMAGE=ghcr.io/fission/node-builder-22
export PYTHON_RUNTIME_IMAGE=ghcr.io/fission/python-env
export PYTHON_BUILDER_IMAGE=ghcr.io/fission/python-builder
export GO_RUNTIME_IMAGE=ghcr.io/fission/go-env-1.23
export GO_BUILDER_IMAGE=ghcr.io/fission/go-builder-1.23

# 4. Run the suite
go test -tags=integration -timeout=30m -parallel 6 -v ./test/integration/suites/common/...

# Run a single test
go test -tags=integration -timeout=10m -run TestNodeHelloHTTP -v ./test/integration/suites/common/...

# Keep resources around on failure for debugging
TEST_NOCLEANUP=1 go test -tags=integration -v -run TestFooBar ./test/integration/suites/common/...
```

### Adding a new test

See `docs/test-migration/02-framework-api.md` for the framework reference and the 12-step "Adding a new test" checklist (suite layout, build tag, fixture vendoring, cleanup conventions, RFC-1123 name rules, builder env handling, raw-stdout subcommands, spec tests).

### Optional / env-gated tests

These are in the suite but `t.Skip` when their image env vars aren't set:

| Test | Required env vars |
|---|---|
| `TestTensorflowServingEnv` | `TS_RUNTIME_IMAGE` |
| `TestJVMJerseyEnv` | `JVM_JERSEY_RUNTIME_IMAGE` + `JVM_JERSEY_JAR_PATH` |
| `TestJavaEnv` | `JVM_RUNTIME_IMAGE` + `JAVA_HELLO_JAR_PATH` |
| `TestJavaBuilder` | `JVM_RUNTIME_IMAGE` + `JVM_BUILDER_IMAGE` |

## Envtest tests (`test/e2e/`)

```bash
make test-run    # also covers everything under pkg/
```

Uses `setup-envtest` from kubebuilder; `hack/runtests.sh` fetches the right binaries.

## Upgrade tests (`test/upgrade_test/`)

Driven by `.github/workflows/upgrade_test.yaml` on push/PR with path filters. Installs a stable release, builds the candidate images locally, helm-upgrades, then exercises a small set of fission objects against the upgraded cluster. Not typically run locally.

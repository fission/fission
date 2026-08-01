# Test suites

Three flavors of tests live under `test/`.
Pick the one matching the layer you're touching.

| Directory | Framework | When to use |
|---|---|---|
| `test/integration/` | Go + `testify`, build tag `//go:build integration` | End-to-end Fission behavior against a real Kind cluster (router, executor, buildermgr, storagesvc, CRDs). |
| `test/e2e/` | Go + envtest (in-process kube-apiserver) | Controller-level reconcile loops that don't need a real cluster. |
| `test/benchmark/` | Go | E2E performance benchmarking suite (RFC-0020): the `fission-benchmark` CLI + engine, including the `upgrade-under-load` scenario behind `.github/workflows/upgrade_test.yaml`. See `test/benchmark/README.md`. |

## Integration tests (`test/integration/`)

The Go integration suite replaced the previous bash test runner.
Every PR runs it on Kind across three Kubernetes versions (1.28, 1.32, 1.34) via `.github/workflows/push_pr.yaml`.

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

## Upgrade tests (`upgrade-under-load` benchmark scenario)

Driven by `.github/workflows/upgrade_test.yaml` on push/PR with path filters (RFC-0028 phase 1).
The workflow installs the latest published release, builds HEAD images into Kind, and runs the `upgrade-under-load` scenario from `test/benchmark`: fixtures are seeded, constant-rate load runs against the router through the whole `helm upgrade`, and every request outcome is classified (ok / transport failure / RFC-0015 platform-attributed / unattributed error), alongside specialized warm-pod UID survival and async queue durability.
The scenario only exists when `--upgrade-cmd` is passed, so a regular benchmark run can never upgrade the cluster it is measuring.
Run it locally against a disposable cluster with `fission-benchmark run --scenarios upgrade-under-load --upgrade-cmd "helm upgrade …"`.
The error-budget evaluation in the workflow is non-gating while the RFC-0028 rollout-posture phases land.

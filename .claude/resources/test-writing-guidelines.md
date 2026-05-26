# Test-writing conventions

Follow these when adding or modifying tests in this repo.
They reflect the patterns already established across the codebase — match them so tests stay consistent and reviewable.

## Assertions

- Use `github.com/stretchr/testify/require` and `github.com/stretchr/testify/assert` rather than hand-written `if got != want { t.Errorf(...) }`.
- Use `require.*` for preconditions that must hold for the rest of the test to make sense — it stops the test on failure.
- Use `assert.*` for independent checks where you want every failure reported in a single run.

## Context

- Use `t.Context()` to obtain a context scoped to the test; it is cancelled automatically when the test ends.
- Do **not** use `context.Background()` or `context.TODO()` in tests when a `*testing.T` (or `*testing.B`) is in scope.

## Structure

- Prefer table-driven tests with `t.Run(tt.name, func(t *testing.T){ ... })`; give each case a descriptive name.
- Add `t.Parallel()` to independent tests, and to subtests that share no mutable state.
- Use `t.TempDir()` for filesystem fixtures; never write into the repo or a hard-coded path.
- Register mid-test teardown with `t.Cleanup(...)` rather than a bare `defer`.

## Kubernetes / Fission code

- Prefer fake clientsets for unit tests — `k8s.io/client-go/kubernetes/fake` and the generated Fission fake clientset — over spinning up `envtest`.
- Reserve `envtest` and the integration suite for behavior that needs a real control plane: reconcilers, webhooks, finalizers, and end-to-end request flow.
- For pod/Deployment spec builders, assert on the returned spec object directly without touching a cluster.
- Models to copy: `pkg/executor/executortype/newdeploy/newdeploy_test.go` and `pkg/executor/util/hpa/hpa_test.go`.

## HTTP handlers

- Use `net/http/httptest` (`NewRecorder` / `NewServer`).
- Models to copy: `pkg/router/auth_test.go` and `pkg/storagesvc/storagesvc_auth_test.go`.

## Snapshots

- For large or structured expected output (e.g. aggregated validation errors), use the `go-snaps` snapshot pattern already in `pkg/apis/core/v1/validation_test.go`.

## Integration tests

- Start the file with `//go:build integration`, a blank line, then `package common_test`, under `test/integration/suites/common/`.
- Use the framework: `framework.Connect(t)`, `ns := f.NewTestNamespace(t)`, and the `Create*` builders — cleanup is automatic.
- Env-gate runtime images via `f.Images().Require*(t)` so the test skips cleanly when the image env var is unset.
- Invoke functions through `f.Router(t)`, which auto-routes internal `/fission-function/` paths to the internal listener.
- Follow the 12-step "Adding a new test" guide in `docs/test-migration/02-framework-api.md`.

## Misc

- New source files need the SPDX header — run `make license` (CI gate: `make license-check`).
- Prefer the Go 1.26 builtin `new(value)` over `k8s.io/utils/ptr.To(value)`.
- Name tests `TestXxx`, and test behavior through exported APIs rather than reflecting over unexported fields.

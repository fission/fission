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

## Time-dependent code

- Test timeouts, TTLs, backoff schedules, lease expiry, debounces, and cron windows inside `testing/synctest.Test` bubbles (stable since Go 1.25; this repo is on Go 1.26).
  The bubble virtualizes `time.Now`, timers, and sleeps: a "wait 6h then act" test completes instantly and deterministically.
- Because the bubble fakes the standard `time` package, code under test should just use `time` directly — do **not** add an injectable clock seam only for testability.
- Everything the bubbled code touches must be in-process and in-memory (fakes, memory drivers, channels); real network or filesystem waits are not virtualized and will hang the bubble.
- Never `time.Sleep` in a test to "wait for" concurrent code outside a bubble; restructure around synctest or an explicit synchronization point.

## Property-based tests

- Use `pgregory.net/rapid` for property-based tests.
  Do **not** use the stdlib `testing/quick` — it is frozen and rapid is its maintained successor.
- Reach for properties when the code has algebraic structure examples can't cover: round-trips (encode/decode, publish/resolve), idempotence (`f(f(x)) == f(x)`), invariant bounds (quotas, attempt budgets, jitter ranges), distribution claims (hashing balance), and determinism (same inputs → same output on every replica).
- Model-based testing: when an in-memory reference implementation exists (e.g. a memory driver next to a real-backend driver), have rapid generate random operation sequences and assert the real implementation's observable behavior equals the model's.
  This one pattern replaces most hand-written matrix tests for storage-like code.
- Property tests run with rapid's default deterministic seeds in PR CI; longer randomized runs belong in nightly jobs, and any failing seed goes into the test as a regression case.

## Fuzzing

- Give native `go test -fuzz` targets to parser/decoder/verifier boundaries: token or signature verification, wire envelopes, path and expression parsing, header grammars.
  The two baseline properties are round-trip stability and never-panic on arbitrary input.
- For an authn/authz check, fuzz the adversary: mutate valid credentials (bit flips, truncation, field splices, re-encoding) and assert nothing but the exact expected input authenticates.
- Check the seed corpus into `testdata/`; PR CI runs the corpus as regression tests (`go test` does this automatically), extended fuzzing time belongs in a nightly job.

## Concurrency and crash safety

- Prefer **crash-point enumeration** over random fault injection when the code has identifiable persistence boundaries: list every crash point (before write, between write and side effect, after side effect) and drive a table-driven kill-and-resume test through each.
  Exhaustive and deterministic beats probabilistic.
- For concurrent access to a store with consistency claims (CAS, versions), record real concurrent histories and check them with `porcupine` linearizability checking rather than asserting on final state only.
- Reader/body edge cases (`http.MaxBytesReader` caps, mid-stream failures) are covered by composing `testing/iotest` readers (`ErrReader`, `TimeoutReader`, `OneByteReader`, `HalfReader`) with the code under test.
- When a component implements a protocol that has a TLA+ spec (see `docs/rfc/specs/` next to the RFC that defines it), the spec is the design authority: change the spec and re-run TLC **before** changing the protocol code, and keep the spec's numbered invariants mirrored as test names so the correspondence is auditable.
- State the invariants a stateful component guarantees (the RFC's "Invariants & verification" section, if it has one) and write tests that assert the invariant under generated interleavings — not just the happy-path example.

## Integration tests

- Start the file with `//go:build integration`, a blank line, then `package common_test`, under `test/integration/suites/common/`.
- Use the framework: `framework.Connect(t)`, `ns := f.NewTestNamespace(t)`, and the `Create*` builders — cleanup is automatic.
- Env-gate runtime images via `f.Images().Require*(t)` so the test skips cleanly when the image env var is unset.
- Invoke functions through `f.Router(t)`, which auto-routes internal `/fission-function/` paths to the internal listener.
- Follow the 12-step "Adding a new test" guide in `docs/test-migration/02-framework-api.md`.

## Misc

- New source files need the SPDX header — run `make license` (CI gate: `make license-check`).
- Prefer the Go 1.26 builtin `new(value)` over `k8s.io/utils/ptr.To(value)`. As of Go 1.26 `new` accepts a value expression and returns a pointer to a copy, so `new(true)` yields a `*bool` (see `pkg/executor/executortype/newdeploy/newdeploy_test.go`); it is no longer type-only.
- Name tests `TestXxx`, and test behavior through exported APIs rather than reflecting over unexported fields.

# Fission dependency groups & couplings

Repo-specific input to the generic `go-deps-security-sweep` skill: which dependency groups must move in lockstep, the `replace`/`exclude` directives in play, and the codegen that follows a platform bump.
The skill carries the generic methodology (govulncheck baseline/after, one commit per group, bisect-within-group); this file is the part MVS gets wrong without help.

## The groups (lowest risk first)

1. **Kubernetes core** — `k8s.io/api`, `k8s.io/apimachinery`, `k8s.io/client-go`, `k8s.io/apiextensions-apiserver`, `k8s.io/metrics`.
   All to the same patch/minor.
   Lowest risk on a patch bump.
2. **Controller / CRD tooling** — `sigs.k8s.io/controller-runtime`, `sigs.k8s.io/structured-merge-diff/v*`, `sigs.k8s.io/yaml`. controller-runtime minors break admission-webhook consumers (see below).
3. **OpenTelemetry** — all `go.opentelemetry.io/otel/*` and `go.opentelemetry.io/contrib/*`.
   Bump together — core and contrib use linked version lines (`v1.X` / `v0.Y` where `Y ≈ X+39`).
   `otel/sdk` has carried CVEs.
4. **gRPC + x/net** — `google.golang.org/grpc`, `golang.org/x/net`.
   Commonly paired in CVE fixes; often pulled up transitively by the otel bump, so check before bumping explicitly.
5. **Messaging** — `github.com/IBM/sarama`, `github.com/kedacore/keda/v2`.
   See the KEDA note.
6. **Misc** — everything else with a CVE or minor available (`minio-go`, `go-git`, `go-snaps`, `fatih/color`, …).
   Low blast radius; last.

## The k8s-minor lockstep commit

On a k8s **minor** bump, groups 1 (k8s core), 2 (controller-runtime), KEDA from group 5, **and** the code-generator fork are version-locked and must land as **one** commit. controller-runtime's cache code and KEDA's webhook code each compile against a specific k8s minor (the `ResourceEventHandlerRegistration.HasSyncedChecker` coupling: that interface method arrived at k8s v0.36, implemented only by controller-runtime v0.24+).
MVS will otherwise pick an incompatible mix.
A k8s **patch** bump (e.g. v0.36.1→v0.36.2) stays in its own small group.

Worked example (2026-06-02 sweep): k8s v0.36.1 + controller-runtime v0.24.1 + KEDA v2.20.0 + code-generator 0.36 in a single commit, with the controller-runtime v0.24 source migrations alongside (next section).

## controller-runtime v0.24 source migrations

If you bump into / out of controller-runtime v0.24, replicate these:
- The webhook builder went **generic**: `NewWebhookManagedBy[T](mgr, obj)` (no `.For(obj)`), and `admission.Defaulter[T]` / `admission.Validator[T]` receive a typed `T` instead of `runtime.Object`.
  `pkg/webhook/generic.go` lost its type-assertion boilerplate; per-CRD compile-time asserts became `admission.Defaulter[*v1.X]` / `admission.Validator[*v1.X]` (import `.../webhook/admission`).
- `scheme.Builder` was deprecated (staticcheck SA1019).
  `pkg/apis/core/v1` migrated to the apimachinery builder (`runtime.NewSchemeBuilder(addKnownTypes)` + `metav1.AddToGroupVersion`), preserving exact `AddKnownTypes` semantics.

## The code-generator fork

`go.mod` has `replace k8s.io/code-generator => github.com/fission/code-generator <pseudo-version>` — Fission's fork tracks upstream.
It is **version-coupled to the k8s-core group**: when you bump `k8s.io/* → vN`, bump the fork to its matching `vN` ref (the fork carries `release-1.x` branches and `vN.0` tags; the 2026-06-02 sweep used fork master `ce5e0619` (0.36) per maintainer preference over the `v0.36.0` tag).
**After** the bump, run `make codegen` + `make generate-crds` and commit the regenerated `pkg/generated/` + `crds/v1/` **in the same commit** as the k8s bump.

## The `prometheus/common` phantom-tag exclude

`go.mod` keeps `exclude github.com/prometheus/common v1.20.99` plus an explicit `go get github.com/prometheus/common@<real-latest>`.
`v1.20.99` is a retracted high-semver tag that otherwise wins as "latest" for the direct require.
Symptom: `tidy` warns `retracted by module author`.
(This is the only phantom directive still needed — the old KEDA `replace`/`exclude` are gone.)

## KEDA phantom-require history (resolved)

Pre-v2.20.0, KEDA shipped a go.mod that `require`d `controller-runtime`/`client-go` at versions it internally `replace`d to sane ones; since `replace` isn't transitive, consumers inherited the bogus requires and MVS broke the build.
**Resolved as of KEDA v2.20.0** (2026-06): its requires match its compile target and it dropped the bogus `client-go v1.5.2`, so k8s core + controller-runtime + KEDA now all move together.
If a future KEDA re-caps controller-runtime at vX, cap k8s core at the highest minor controller-runtime vX supports, and expect the unblock to be a single event (KEDA shipping against the newer controller-runtime).
Diagnose a re-regression with `go mod graph | grep '<bad-version>'` and `go list -m sigs.k8s.io/controller-runtime`.

## Fission build/gate commands

- Lint: `make code-checks` (golangci-lint against `.golangci.yaml`, + `make verify-gomod`).
- Tests: `make test-run` (envtest via `hack/runtests.sh`; sets `KUBEBUILDER_ASSETS`).
- Full gate: `make check` (test-run + `build-fission-cli` + `clean`).
- Build scope: `go build ./pkg/... ./cmd/...` — never `./...`.
  Test fixtures (e.g. a `package main` with no `main`) fail `./...` on `main` too.
- Dependabot groups *all* Go deps weekly (`.github/dependabot.yml`); a security sweep bypasses that grouping on purpose so each group stays bisectable.

## Out of scope (matches the generic skill)

Go toolchain bumps; indirect-only deps (let `go mod tidy` carry them); major-version bumps (`v1→v2`) — dedicated review, not a batch sweep.

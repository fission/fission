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

On a k8s **minor** bump, groups 1 (k8s core), 2 (controller-runtime), `sigs.k8s.io/controller-tools`, `k8s.io/code-generator`, **and** `github.com/google/go-containerregistry/pkg/authn/kubernetes` are version-locked and must land as **one** commit, together with the regenerated `crds/v1/`. controller-runtime's cache code compiles against a specific k8s minor (the `ResourceEventHandlerRegistration.HasSyncedChecker` coupling: that interface method arrived at k8s v0.36, implemented only by controller-runtime v0.24+).
MVS will otherwise pick an incompatible mix.
A k8s **patch** bump (e.g. v0.36.1→v0.36.2) stays in its own small group.

KEDA is **not** part of the lockstep.
Fission imports only KEDA's `apis/keda/v1alpha1` types and its generated clientset,
so KEDA's `go.mod` sets minimum versions, not a ceiling:
a KEDA release built against the previous k8s minor is fine as long as those two packages still compile.
Check with `go build ./pkg/...`, not by reading KEDA's `go.mod`.
(Sweeps 2026-09 and 2026-10 deferred k8s 0.37 on the wrong belief that KEDA had to move first.)

Worked examples:
2026-06-02, k8s v0.36.1 + controller-runtime v0.24.1 + KEDA v2.20.0 + code-generator 0.36, with the controller-runtime v0.24 source migrations alongside (next section);
2026-10-15, k8s v0.37.1 + controller-runtime v0.25.2 + controller-tools v0.22.0 + `authn/kubernetes`, with KEDA left at v2.21.0 (built against k8s 0.36).
The only source change was `runtime.ParseDocumentationFrom` in `tools/genswaggertypedocs`, which now also returns an error.

## controller-runtime v0.24 source migrations

If you bump into / out of controller-runtime v0.24, replicate these:
- The webhook builder went **generic**: `NewWebhookManagedBy[T](mgr, obj)` (no `.For(obj)`), and `admission.Defaulter[T]` / `admission.Validator[T]` receive a typed `T` instead of `runtime.Object`.
  `pkg/webhook/generic.go` lost its type-assertion boilerplate; per-CRD compile-time asserts became `admission.Defaulter[*v1.X]` / `admission.Validator[*v1.X]` (import `.../webhook/admission`).
- `scheme.Builder` was deprecated (staticcheck SA1019).
  `pkg/apis/core/v1` migrated to the apimachinery builder (`runtime.NewSchemeBuilder(addKnownTypes)` + `metav1.AddToGroupVersion`), preserving exact `AddKnownTypes` semantics.

## code-generator and other k8s-minor riders

`k8s.io/code-generator` is a plain direct require (the Fission fork and its `replace` were removed in #3672).
`hack/update-codegen.sh` copies upstream code-generator out of the module cache at whatever version `go.mod` requires, so bump it with the `k8s.io/*` group.
**After** a k8s bump, run `make codegen` + `make generate-crds` and commit the regenerated `pkg/generated/` + `crds/v1/` **in the same commit** as the k8s bump.

`github.com/google/go-containerregistry/pkg/authn/kubernetes` is published only as pseudo-versions, and every snapshot after 2026-08-31 requires `k8s.io/* v0.37`.
A `go get` of a newer snapshot silently pulls k8s core up a minor; keep it on its current pseudo-version until the k8s-minor lockstep commit, and bump it there.

## The `prometheus/common` phantom-tag exclude

`go.mod` keeps `exclude github.com/prometheus/common v1.20.99` plus an explicit `go get github.com/prometheus/common@<real-latest>`.
`v1.20.99` is a retracted high-semver tag that otherwise wins as "latest" for the direct require.
Symptom: `tidy` warns `retracted by module author`.
(This is the only phantom directive still needed — the old KEDA `replace`/`exclude` are gone.)

## KEDA phantom-require history (resolved)

Pre-v2.20.0, KEDA shipped a go.mod that `require`d `controller-runtime`/`client-go` at versions it internally `replace`d to sane ones; since `replace` isn't transitive, consumers inherited the bogus requires and MVS broke the build.
**Resolved as of KEDA v2.20.0** (2026-06): its requires match its compile target and it dropped the bogus `client-go v1.5.2`.
Its requires are now plain minimums, so KEDA can trail the k8s minor (see the lockstep section).
Only a `replace`-style phantom require (a version above what Fission wants) would block a bump again.
Diagnose a re-regression with `go mod graph | grep '<bad-version>'` and `go list -m sigs.k8s.io/controller-runtime`.

## The nested `test/benchmark` module

`test/benchmark/go.mod` is a **separate module** that ends with `replace github.com/fission/fission => ../../`.
Because it resolves the root module from the working tree, every root dependency bump leaves its own `go.mod`/`go.sum` stale.
Run `(cd test/benchmark && go mod tidy)` in the **same commit** as each root bump, and stage `test/benchmark/go.mod` + `test/benchmark/go.sum` alongside `go.mod` + `go.sum`.

Symptom if you skip it: `make codegen` fails — controller-gen's `paths=./...` walks into the nested module and reports
`load packages in root ".../test/benchmark": err: exit status 1: ... go: updates to go.mod needed; to update it: go mod tidy`.
The error names a missing transitive dep (e.g. `hdrhistogram-go`), which misleadingly looks like a root-module tidy problem.
`go build ./pkg/... ./cmd/...` does **not** cover this module — build it explicitly with `(cd test/benchmark && go build ./...)`.

## Build scopes that the default gate misses

Two consumer sets compile only under extra flags, so a dep they own can break with `make code-checks` still green:
- **Integration suite** (`test/integration/`, build tag `integration`) — owns `go-portless`, and uses `gateway-api` and the k8s clients.
  Verify with `go vet -tags=integration ./test/...`.
- **Nested benchmark module** — see above.

## Fission build/gate commands

- Lint: `make code-checks` (golangci-lint against `.golangci.yaml`, + `make verify-gomod`).
- Tests: `make test-run` (envtest via `hack/runtests.sh`; sets `KUBEBUILDER_ASSETS`).
- Full gate: `make check` (test-run + `build-fission-cli` + `clean`).
- Build scope: `go build ./pkg/... ./cmd/...` — never `./...`.
  Test fixtures (e.g. a `package main` with no `main`) fail `./...` on `main` too.
- Dependabot groups *all* Go deps weekly (`.github/dependabot.yml`); a security sweep bypasses that grouping on purpose so each group stays bisectable.

## Out of scope (matches the generic skill)

Go toolchain bumps; indirect-only deps (let `go mod tidy` carry them); major-version bumps (`v1→v2`) — dedicated review, not a batch sweep.

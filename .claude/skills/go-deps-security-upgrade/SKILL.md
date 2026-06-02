---
name: go-deps-security-upgrade
description: Run a grouped, bisectable Go dependency security sweep on the Fission repo. Use when the user asks to upgrade outdated/vulnerable Go dependencies, run a dep security pass, or process CVE findings from govulncheck. Produces one commit per logical dependency group on a dedicated branch so failures are attributable and revertable.
---

# Go dependency security upgrade (Fission)

Playbook for upgrading outdated Go dependencies as a security sweep. Optimized for **isolating failures**: each logical group of related dependencies lands as a separate commit so `git bisect` can attribute any regression to one group, then one dep within it.

## When to invoke

Trigger phrases: "upgrade outdated Go deps", "security sweep on go.mod", "run govulncheck and fix what's found", "bump dependencies for security".

Skip if the user asks for a single, named dep bump — that's just `go get <pkg>@<ver>` + tidy + lint + commit, no grouping needed.

## Phase 0 — Baseline

1. Create branch off `main`. **First check for a stale collision** — a prior month's sweep branch is often still present locally and on `origin` after its PR merged (`git checkout -b deps/security-sweep-<YYYY-MM>` then fails with "branch already exists", and its diff vs `main` looks like a huge *reverse* because `main` moved on). If `git rev-parse --verify deps/security-sweep-<YYYY-MM>` succeeds, run `gh pr list --head <branch> --state all` — if that branch's PR is MERGED, it's leftover; do NOT reuse it (its "upgrades" are now downgrades vs `main`). Use a **date-stamped** name instead: `git checkout main && git checkout -b deps/security-sweep-<YYYY-MM-DD>`.
2. Install scanner (if missing): `go install golang.org/x/vuln/cmd/govulncheck@latest`.
3. Capture baseline: `govulncheck ./... | tee /tmp/govulncheck-before.txt`. Extract the "Your code is affected by N vulnerabilities" line and the per-vuln "Fixed in:" versions — those dictate minimum target versions.
4. List outdated direct deps: `go list -m -u -json all` filtered to entries with both `"Update"` and no `"Indirect": true`. Use this Python one-liner:
   ```bash
   go list -m -u -json all 2>/dev/null | python3 -c "
   import json, sys
   buf=''; mods=[]
   for line in sys.stdin:
       buf += line
       if line.rstrip()=='}':
           try: mods.append(json.loads(buf)); buf=''
           except: pass
   for m in mods:
       if not m.get('Indirect') and m.get('Update'):
           print(f\"{m['Path']:<70} {m['Version']:<20} -> {m['Update']['Version']}\")
   "
   ```

## Phase 1 — Group the upgrades

Bucket the outdated deps into these groups (in order — lowest risk first):

1. **Kubernetes core** — `k8s.io/api`, `k8s.io/apimachinery`, `k8s.io/client-go`, `k8s.io/apiextensions-apiserver`, `k8s.io/metrics`. All must move to the same patch/minor. Typically lowest risk when it's a patch bump.
2. **Controller / CRD tooling** — `sigs.k8s.io/controller-runtime`, `sigs.k8s.io/structured-merge-diff/v*`, `sigs.k8s.io/yaml`. controller-runtime minors may break admission-webhook consumers (see pitfall below).
3. **OpenTelemetry** — all `go.opentelemetry.io/otel/*` and `go.opentelemetry.io/contrib/*`. Must bump together — otel core and contrib use linked version lines (`v1.X` / `v0.Y` where Y = X+39-ish). `otel/sdk` carries CVEs historically.
4. **gRPC + x/net** — `google.golang.org/grpc`, `golang.org/x/net`. Commonly paired in CVE fixes. Often pulled up transitively by the otel bump — check before bumping explicitly.
5. **Messaging** — `github.com/IBM/sarama`, `github.com/kedacore/keda/v2`. KEDA historically shipped broken go.mod declarations for consumers — but v2.20.0 (2026-06) is clean and moves with the k8s-core group; see the KEDA pitfall for the history and the current state.
6. **Misc** — everything else with a CVE or minor-bump available: `minio-go`, `go-git`, `influxdb`, `go-snaps`, `fatih/color`, etc. Leave for last; low blast radius.

If a dep in a group carries a CVE that the baseline scan flagged, keep that group's order but note it in the commit message.

**Coupling on a k8s *minor* bump:** groups 1 (k8s core), 2 (controller-runtime), 5's KEDA, *and* the code-generator fork are version-locked and must land as **one** commit, because controller-runtime's cache code and KEDA's webhook code each compile against a specific k8s minor (the `ResourceEventHandlerRegistration.HasSyncedChecker` coupling). MVS will otherwise pick an incompatible mix. A k8s *patch* bump (e.g. v0.36.1→v0.36.2) stays in its own small group. The 2026-06-02 sweep is the worked example: k8s v0.36.1 + controller-runtime v0.24.1 + KEDA v2.20.0 + code-generator 0.36 in a single commit, with the controller-runtime v0.24 webhook/scheme source migrations alongside.

## Phase 2 — Per-group workflow

For each group, on the branch:

```bash
go get <pkg1>@<version1> <pkg2>@<version2> ...   # all deps in the group in one command
go mod tidy
go build ./pkg/... ./cmd/...                      # NOT ./... — see pitfall about test fixtures
make code-checks                                  # golangci-lint via Makefile
```

If build + lint both pass, commit:
```
Bump <group name> (<pkg@ver>, <pkg@ver>)

<One-line rationale. If this group closes a GO-YYYY-NNNN vuln from the baseline scan, name it.>
```

If the group fails, bisect **within** the group: drop one dep at a time from the `go get` command, re-run tidy+build+lint. Pin back the offender and commit the rest. Do NOT skip the commit for the group entirely — a partial group is still progress.

## Phase 3 — Final verification (once, after all groups)

1. `git diff main -- go.mod | head -80` — sanity-check the direct-deps diff matches the groups.
2. `make check` — full local gate (lint + envtest + CLI build). See pitfall about Docker-required tests.
3. `govulncheck ./... | tee /tmp/govulncheck-after.txt` and compare against the baseline. Include "CVEs closed" in the PR description.

## Pitfalls learned the hard way

### KEDA's phantom `require` pins — UNBLOCKED 2026-06-02 (KEDA v2.20.0)

> **STATUS: the long-running KEDA cap is resolved.** KEDA **v2.20.0** ships a go.mod
> whose `require sigs.k8s.io/controller-runtime v0.23.3` now *matches* its compile
> target (it `replace`s controller-runtime to the same v0.23.3, not down to v0.22.4),
> and it **dropped** the bogus `client-go v1.5.2` phantom require entirely. That was the
> single unblock event predicted below. As of the 2026-06-02 sweep the tree is at:
> **k8s.io/* v0.36.1, controller-runtime v0.24.1, KEDA v2.20.0** — they all moved
> together in one commit. The `replace controller-runtime => v0.22.4` and
> `exclude client-go v1.5.2` directives are **gone**.
>
> What this required in Fission source (do this again if you ever revert/re-bump):
> - controller-runtime **v0.24** made the webhook builder generic:
>   `NewWebhookManagedBy[T](mgr, obj)` (no more `.For(obj)`), and
>   `admission.Defaulter[T]` / `admission.Validator[T]` now receive a **typed T**
>   instead of `runtime.Object`. `pkg/webhook/generic.go` lost its type-assertion
>   boilerplate; per-CRD compile-time asserts became `admission.Defaulter[*v1.X]` /
>   `admission.Validator[*v1.X]` (import `.../webhook/admission`, not `.../webhook`).
> - controller-runtime **v0.24** deprecated `scheme.Builder` (staticcheck SA1019).
>   `pkg/apis/core/v1` migrated to the apimachinery builder
>   (`runtime.NewSchemeBuilder(addKnownTypes)` + `metav1.AddToGroupVersion`),
>   preserving exact `AddKnownTypes` semantics.
> - The **only** phantom directive still needed is `exclude github.com/prometheus/common
>   v1.20.99` (retracted high-semver tag that still wins as "latest" for our *direct*
>   require) plus an explicit `go get github.com/prometheus/common@<real-latest>`.

**Historical mechanism (pre-v2.20.0), kept for the next time KEDA regresses:**
older KEDA releases declared `require k8s.io/client-go v1.5.2` and `require
sigs.k8s.io/controller-runtime v0.23.1` while internally `replace`-ing them to sane
versions. Replace directives are **not** transitive, so consumers inherited the bogus
`require` and MVS picked the ancient (v1.5.2) or wrong-minor version, breaking the build.
The fix then was: `exclude` the bogus high-semver tags, and pin controller-runtime with
`replace` (NOT `exclude` — a bare exclude resolves *up* to the next available version,
e.g. v0.24.1, which broke the old KEDA webhooks):
```go.mod
exclude (
	github.com/prometheus/common v1.20.99
	k8s.io/client-go v1.5.2
)
replace sigs.k8s.io/controller-runtime => sigs.k8s.io/controller-runtime v0.22.4
```
Verify with `go mod graph | grep '<bad-version>'` to find which consumer declares a
phantom require, and `go list -m sigs.k8s.io/controller-runtime` to confirm a replace took.

**Decision rule (still valid):** if controller-runtime is *re-capped* by a future KEDA at
vX, cap k8s core at the highest minor controller-runtime vX supports (the k8s
`ResourceEventHandlerRegistration` interface gained `HasSyncedChecker` at v0.36, which
only controller-runtime v0.24+ implements — so cr v0.22.4 ⇒ k8s ≤ v0.35.x). The unblock
is always a single event: KEDA shipping against the newer controller-runtime, after which
k8s core + controller-runtime + KEDA all move together in one commit.

### Retracted module tags (e.g. `prometheus/common v1.20.99`)
Some transitive deps push accidental high-version tags (`v1.20.99` on a project that's actually on the `v0.x` line). `go get` then picks the retracted tag as "latest". Symptom: `tidy` warns `retracted by module author`.
**Fix**: add `exclude github.com/prometheus/common v1.20.99` and explicit `go get <pkg>@<correct-latest>`.

### `go build ./...` vs test fixtures
The Fission repo contains test fixture Go files (e.g. `test/tests/test_huge_response/hello.go`) that declare `package main` without a `main` function — they're not meant to be compiled standalone. `go build ./...` fails on them **on `main` too**. Always scope the compile check to `./pkg/... ./cmd/...` to avoid false-positive noise.

### Docker-dependent tests
`pkg/storagesvc/client` uses `ory/dockertest` to spin up a MinIO container. Without Docker running, `make check` fails there. This is environmental, not a dep regression. Verify by checking out `go.mod` + `go.sum` from `main` and re-running the single package — if it still fails, it's environmental.

### Disk exhaustion during `make test-run` (macOS, learned 2026-06-02)
A k8s-minor bump invalidates the entire `$(go env GOCACHE)` (it keyed on the old client-go), so the first `make test-run` after the bump recompiles everything and the cache balloons (saw 22G). On a near-full disk the symptom is a **cascade of `[build failed]` across unrelated packages** with `LLVM ERROR: IO failure ... No space left on device` / `dsymutil failed` / `strip: can't write output file` — this is NOT a dep regression. Check `df -h /` and `du -sh $(go env GOCACHE)`; clear with `go clean -cache` (frees the bulk) and re-run. Don't chase the per-package failures.

### `make test-run` pipes through `tail` — failures scroll off
`hack/runtests.sh` runs `go test ./...` and a single failing package prints `FAIL` near the very end, but the **package name** is hundreds of lines up. If you capture with `| tail -N` you'll see a bare `FAIL` with no attribution. Capture full output (`make test-run > /tmp/out.txt 2>&1`) and `grep -nE '^(FAIL|--- FAIL)' ` to find which package actually failed before concluding anything.

### `git stash pop` hazard
The user's stash list may contain pre-existing WIP from other sessions. `git stash` with a clean working tree reports "No local changes to save" and returns silently; a later `git stash pop` then unstashes the **pre-existing** entry unexpectedly and can conflict with the current branch. Before any `git stash pop`, run `git stash list` first to verify you're popping what you think you are. If you accidentally contaminate the working tree, `git checkout HEAD -- <files>` restores them without touching the stash list.

## Fission-specific commands reference

- Lint: `make code-checks` (runs `golangci-lint run` against `.golangci.yaml`)
- Tests: `make test-run` (envtest via `hack/runtests.sh`; needs `KUBEBUILDER_ASSETS` which the script sets)
- Full gate: `make check` (test-run + `build-fission-cli` + `clean`)
- Build scope: `go build ./pkg/... ./cmd/...` (never `./...` — see pitfall)
- Dependabot groups *all* Go deps weekly (`.github/dependabot.yml`). For a security sweep we bypass that grouping on purpose — merging a single 20-dep PR defeats bisect.
- go.mod has a `replace k8s.io/code-generator => github.com/fission/code-generator <pseudo-version>` directive — Fission's fork tracks upstream code-generator. It is **version-coupled to the k8s-core group**: when you bump `k8s.io/* → vN`, bump the fork to its matching `vN` ref too (the fork carries `release-1.x` branches and `vN.0` tags). 2026-06-02 bumped it to fork master `ce5e0619` (0.36) alongside k8s v0.36.1, per the maintainer's preference for the master commit over the `v0.36.0` tag. After bumping, run `make codegen` + `make generate-crds` and commit the regenerated `pkg/generated/` + `crds/v1/` in the same commit as the k8s bump. (The other `replace`/`exclude` directives are the prometheus/common phantom-tag handling — see the KEDA pitfall.)

## Out of scope for a security sweep

- Go toolchain version bumps.
- Indirect-only deps (let `go mod tidy` carry them forward).
- Major-version bumps (e.g. `v1 → v2`) — those need dedicated review, not a batch sweep.

## Canonical execution (copy-paste outline for the model)

```
1. TaskCreate: baseline + one task per group + final verification
2. git checkout -b deps/security-sweep-<YYYY-MM>
3. govulncheck ./... | tee /tmp/govulncheck-before.txt
4. For each group in order (k8s → ctrl-runtime → otel → grpc/x-net → messaging → misc):
     a. go get <deps> ; go mod tidy
     b. go build ./pkg/... ./cmd/... ; make code-checks
     c. On failure, bisect within the group; on success, commit
5. make check
6. govulncheck ./... | tee /tmp/govulncheck-after.txt ; diff against baseline
7. Summarize: commits on branch, CVEs closed, deferred groups (with upstream blocker noted)
```

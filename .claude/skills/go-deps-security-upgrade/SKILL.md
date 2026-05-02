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

1. Create branch: `git checkout -b deps/security-sweep-<YYYY-MM>` off `main`.
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
5. **Messaging** — `github.com/IBM/sarama`, `github.com/kedacore/keda/v2`. KEDA has repeatedly shipped broken go.mod declarations for consumers — see pitfall.
6. **Misc** — everything else with a CVE or minor-bump available: `minio-go`, `go-git`, `influxdb`, `go-snaps`, `fatih/color`, etc. Leave for last; low blast radius.

If a dep in a group carries a CVE that the baseline scan flagged, keep that group's order but note it in the commit message.

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

### KEDA's phantom `require` pins
`github.com/kedacore/keda/v2` ships go.mod files that declare `require k8s.io/client-go v1.5.2` and `require sigs.k8s.io/controller-runtime v0.23.x` but internally `replace`s them to sane versions (v0.3x.x and v0.22.x). Replace directives are **not** transitive. Consumers inherit the bogus `require` and MVS picks the ancient (v1.5.2) or wrong-minor version, breaking the build.

**Fix**: add `exclude` directives to the Fission `go.mod`:
```go.mod
// KEDA phantom requires — not propagated through their internal `replace`.
exclude k8s.io/client-go v1.5.2
exclude sigs.k8s.io/controller-runtime v0.23.1
```
Verify with `go mod graph | grep '<bad-version>'` to confirm which consumer declares the phantom require.

### KEDA ↔ controller-runtime coupling
KEDA's webhook files (`apis/keda/v1alpha1/*_webhook.go`) are compiled whenever Fission imports KEDA CRD types. If controller-runtime's `NewWebhookManagedBy` signature changed (happened at v0.23), KEDA webhook files written against the old signature won't compile. Symptoms:
```
not enough arguments in call to ctrl.NewWebhookManagedBy
    have (controllerruntime.Manager)
    want (manager.Manager, T)
```
**Fix**: pin controller-runtime to the KEDA-compatible version (currently v0.22.4) and defer the bump until KEDA ships a cleanly-compatible release. Document in the deferred-group commit message.

### Retracted module tags (e.g. `prometheus/common v1.20.99`)
Some transitive deps push accidental high-version tags (`v1.20.99` on a project that's actually on the `v0.x` line). `go get` then picks the retracted tag as "latest". Symptom: `tidy` warns `retracted by module author`.
**Fix**: add `exclude github.com/prometheus/common v1.20.99` and explicit `go get <pkg>@<correct-latest>`.

### `go build ./...` vs test fixtures
The Fission repo contains test fixture Go files (e.g. `test/tests/test_huge_response/hello.go`) that declare `package main` without a `main` function — they're not meant to be compiled standalone. `go build ./...` fails on them **on `main` too**. Always scope the compile check to `./pkg/... ./cmd/...` to avoid false-positive noise.

### Docker-dependent tests
`pkg/storagesvc/client` uses `ory/dockertest` to spin up a MinIO container. Without Docker running, `make check` fails there. This is environmental, not a dep regression. Verify by checking out `go.mod` + `go.sum` from `main` and re-running the single package — if it still fails, it's environmental.

### `git stash pop` hazard
The user's stash list may contain pre-existing WIP from other sessions. `git stash` with a clean working tree reports "No local changes to save" and returns silently; a later `git stash pop` then unstashes the **pre-existing** entry unexpectedly and can conflict with the current branch. Before any `git stash pop`, run `git stash list` first to verify you're popping what you think you are. If you accidentally contaminate the working tree, `git checkout HEAD -- <files>` restores them without touching the stash list.

## Fission-specific commands reference

- Lint: `make code-checks` (runs `golangci-lint run` against `.golangci.yaml`)
- Tests: `make test-run` (envtest via `hack/runtests.sh`; needs `KUBEBUILDER_ASSETS` which the script sets)
- Full gate: `make check` (test-run + `build-fission-cli` + `clean`)
- Build scope: `go build ./pkg/... ./cmd/...` (never `./...` — see pitfall)
- Dependabot groups *all* Go deps weekly (`.github/dependabot.yml`). For a security sweep we bypass that grouping on purpose — merging a single 20-dep PR defeats bisect.
- There's one `replace` directive in go.mod: `k8s.io/code-generator => github.com/fission/code-generator <sha>`. Leave it alone.

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

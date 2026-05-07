---
name: debug-github-ci
description: Investigate and fix GitHub Actions CI failures on the Fission repo's PRs efficiently. Use when CI is red on an open PR, when an integration test needs triaging, when the user asks "why is X failing in CI", or after pushing changes to verify CI before claiming work is done. Optimised for the push-fix-monitor loop and the Fission-specific failure patterns we hit repeatedly: builder/fetcher build pipeline, storagesvc archive flow, NetworkPolicy selectors, /packages shared-volume permissions, kind-ci profile patches.
---

# Debug GitHub CI failures (Fission)

Playbook for triaging and fixing CI failures on PRs in this repo. Optimised for **separating real regressions from pre-existing noise** and for **pattern-matching the same handful of recurring failure modes** before reading the full log.

This file is the orchestrator. Detailed playbooks for each domain live under `resources/` — read them on demand when a phase decision points there.

## When to invoke

Trigger phrases: "CI failed on PR #X", "investigate the failed checks", "why are integration tests red", "the pipeline is broken on my branch", "did CI pass after the push".

Skip if the user already knows the root cause and wants you to apply a specific fix — that's an edit + commit, no triage needed.

## Phase 0 — Separate noise from regression (do this first; saves hours)

1. List checks for the PR:
   ```bash
   gh pr checks <PR> --json name,bucket,state,link \
     --jq '.[] | select(.name != null) | "\(.bucket)\t\(.name)\t\(.link)"'
   ```
   Don't read logs yet. Just see who's red.

2. Compare against `main`:
   ```bash
   gh run list --branch main --workflow=push_pr.yaml --limit 5 \
     --json conclusion,headSha
   gh api repos/fission/fission/commits/<main-sha>/status \
     --jq '.statuses[] | {context, state, description}'
   ```
   If the same check is also failing on `main`, it is **pre-existing noise** and the rest of this playbook does not apply to that check.

3. Known stickers in this repo (don't chase):
   - **`License Compliance` (FOSSA)** — fails persistently on main with "11 issues found".
   - **Tests requiring a local Docker daemon** (e.g. `TestS3StorageService` in `pkg/storagesvc/client`) — failures locally without `dockerd` are environmental.

4. If main is green and PR is red → real regression. Continue.

## Phase 1 — Get the right logs (cheapest first)

Order from cheapest to most expensive — escalate only if the previous step doesn't surface the failing line.

| Step | Command | Notes |
|---|---|---|
| 1 | `gh run view <runId> --log-failed` | Failed steps only. Blocked while other jobs in the run are still pending. |
| 2 | `gh api repos/fission/fission/actions/jobs/<jobId>/logs` | Per-job. Works mid-run. `<jobId>` comes from the `link` URL in `gh pr checks`. |
| 3 | `gh api repos/fission/fission/commits/<sha>/status` | External checks (FOSSA, codecov). Run-level not job-level. |
| 4 | `gh run download <runId> -n go-integration-logs-<runId>-v<k8s>` | Per-test diag dirs with pod logs, yamls. The big artefact for integration-test failures. |

Filter aggressively. Useful first-pass grep:
```bash
grep -E '(FAIL|--- FAIL|fatal:|panic|error:|Error:|denied|connection refused|i/o timeout|context deadline exceeded)'
```

Don't pull `gh run view <runId> --log` (the full archive) unless 1-3 above failed. It's tens of MB.

Full cheatsheet: `resources/gh-commands-cheatsheet.md`.

## Phase 2 — Pattern-match the symptom

Most failures in this repo land in one of these categories. Match the error string first, then load the matching resource for deeper diagnosis.

### Network / connectivity

| Symptom | Root cause | Resource |
|---|---|---|
| `dial tcp <ip>:<port>: i/o timeout` (Fission pod → storagesvc) | NetworkPolicy dropping the request, or pod labels don't match policy selectors | `resources/networkpolicy-debugging.md` |
| `dial tcp ...: connection refused` to `127.0.0.1:8888` | `kubectl port-forward svc/router` race in the test runner. CI has a 30s wait loop. Usually a flake. | — |
| `404 Not Found` from `raw.githubusercontent.com/...` | Test data URL broken upstream. Not a regression. | — |

### Filesystem / permissions

| Symptom | Root cause | Resource |
|---|---|---|
| `permission denied` / `openfdat …: permission denied` on `/packages/...` | Cross-container UID mismatch on shared volume. `0o750` breaks fetcher access; need `0o755`. | `resources/shared-volume-permissions.md` |
| `no files found for globs: [/packages/<deploy>/*]` | Deploy dir empty or unreadable from fetcher | `resources/shared-volume-permissions.md` + `resources/build-pipeline-flow.md` |
| `failed to get source directory info: stat /packages/<path>: no such file or directory` | Build script never wrote the path; pipeline broke earlier | `resources/build-pipeline-flow.md` |

### Build pipeline

| Symptom | Root cause | Resource |
|---|---|---|
| `package "<pkg>" never reached build status "succeeded"` | One of the buildermgr → builder → fetcher steps failed | `resources/build-pipeline-flow.md` |
| `Error uploading deployment package: ...` | fetcher's UploadHandler failing — wrapped error is the real cause | `resources/build-pipeline-flow.md` |
| `bufio.Scanner: token too long` in builder log | Build script line >64KB (pip progress bars do this) | `resources/build-pipeline-flow.md` |
| `dial tcp <env-svc>:8000: i/o timeout` after `CreateEnv(Builder=...)` | Builder/runtime pod readiness race; layered settle fix in framework | `resources/integration-test-framework.md` |
| Selector `envName=<env>` returns no pods | Wrong label key — runtime pods use `environmentName=`, builder pods use `envName=` | `resources/integration-test-framework.md` |
| `ns.CLI` returns empty string for a subcommand that's known to print | Subcommand prints to `os.Stdout` directly (e.g. `archive list`, `function logs`) — use `ns.CLICaptureStdout` | `resources/integration-test-framework.md` |
| Test fixture file is missing from `embed.FS` despite being on disk | The fixture's parent dir contains a `go.mod` (nested module) — `embed` skips it | `resources/integration-test-framework.md` |
| Spec test fails with `unknown flag: --specdir` or wrong cwd | `fission env create --spec` writes `./specs` relative to cwd; needs `ns.WithCWD` | `resources/integration-test-framework.md` |
| Package update doesn't trigger a rebuild | Package CR has no `/status` subresource; must explicitly set `Status.BuildStatus = pending` along with spec change | `resources/integration-test-framework.md` |

### Lint / Go module

| Symptom | Root cause |
|---|---|
| `printf: non-constant format string in call to ... (govet)` | Caller passed `fmt.Sprintf(...)` as a printf-helper format arg. Pass `format, args...` directly, or wrap the dynamic string with `"%s", str`. |
| `invalid version: unknown revision v29.x.y` for `github.com/docker/docker` | Docker re-tagged that import path with `docker-vX.Y.Z` prefixed tags; Go module proxy doesn't index those. Fix: bump `docker/cli` to v29.x — its transitive `moby/moby/v2` import drops `docker/docker` from go.mod entirely. |
| `go: github.com/foo/bar@vX: invalid version` | Module path moved or got retracted. Check upstream `releases/latest` and actual git tags. |

## Phase 3 — Verify hypothesis before pushing

1. **Check the running binary's version**, not your local source.
   - `fission-bundle`, `fetcher`, `pre-upgrade-checks`, `reporter` images are built per-PR by `make skaffold-deploy`.
   - **Env-builder images** (`python-builder`, `node-builder-22`, `go-builder-1.23`, etc.) are pre-built on GHCR. Their `/builder` binary was compiled at image-build time, NOT per-PR. Changes to `pkg/builder/builder.go` do not affect their behaviour in CI integration tests.
   - Confirm with the `caller":"builder/builder.go:NN"` field in pod logs vs. local file line numbers.
   - Full explanation: `resources/builder-image-origin.md`.

2. **Read the source at the cited line** before iterating on a flake. Per the user's `feedback_read_source_before_iterating.md` memory, after 2 failed timing-based fixes, stop guessing — read the code path.

3. **Sanity-test locally** for the affected scope:
   ```bash
   make code-checks                                              # lint
   go test -race -count=1 -timeout 5m ./pkg/<affected>/...       # focused tests
   helm lint charts/fission-all                                  # if Helm changed
   helm template charts/fission-all --set <vals> | sed -n '/^kind: <Kind>/,/^---/p'   # render check
   ```

## Phase 4 — Push and monitor

After pushing the fix, arm the `Monitor` tool with the standard poll loop so terminal-state notifications arrive instead of you polling. Full recipe and rationale: `resources/monitor-poll-loop.md`.

If a check flips back to red after a fix, **don't push another fix immediately** — return to Phase 1 with the new logs. The new failure is often a different root cause exposed by the previous fix; reusing the previous hypothesis wastes a CI cycle.

## CI-only Helm features via skaffold profile

When a Helm-chart feature should be on in CI but off by default for users (e.g. `networkPolicy.enabled`), patch it via the `kind-ci` skaffold profile. Two-step: declare the chart-default in base `setValues`, then `replace`-patch it in the profile. Full instructions and rationale: `resources/skaffold-kind-ci-profile.md`.

## Other resources

- `resources/networkpolicy-debugging.md` — NetworkPolicy selectors, cross-namespace rules, pod label reference.
- `resources/build-pipeline-flow.md` — buildermgr → builder → fetcher → storagesvc dance, where each container's log lives, mapping symptom to failing step.
- `resources/shared-volume-permissions.md` — `/packages` shared volume, builder vs. fetcher UID model, why `0o750` breaks things.
- `resources/skaffold-kind-ci-profile.md` — adding CI-only Helm flags via the kind-ci profile.
- `resources/builder-image-origin.md` — the gotcha that env-builder images are not rebuilt per-PR.
- `resources/integration-test-framework.md` — Go-test-framework quirks the bash→Go migration uncovered: builder/runtime readiness race (8 layered fixes), pod-label conventions, `ns.CLI` capture limitations, `embed.FS` nested-module skip, spec-test cwd handling, Package CR `/status` subresource gap.
- `resources/monitor-poll-loop.md` — the canonical `gh pr checks` poll loop for the push-fix-monitor cycle.
- `resources/gh-commands-cheatsheet.md` — every `gh` invocation we use during a debug session, with notes.

## Out of scope

- Triaging non-Fission GitHub Actions workflows (no shared CI infra).
- Re-running the SAST scanner — that's the `go-deps-security-upgrade` skill or a maintainer-driven scan.
- Fixing the underlying business logic of a failing test — once *cause of failure* is identified, the actual code change is normal edit-test-commit.
- Forensic analysis of merged-main breakage — main reverts and hotfixes have their own flow.

## Canonical execution outline

```
1. Phase 0:
   gh pr checks <PR>                                 # see who's red
   gh run list --branch main --limit 5               # cross-check: red on main too?
   → cross out pre-existing noise (FOSSA, etc.)

2. Phase 1 (escalate from cheap to expensive):
   a. gh run view <runId> --log-failed | grep -E '<patterns>'
   b. gh api .../jobs/<jobId>/logs | grep -B2 -A8 '<patterns>'
   c. gh run download <runId> -n go-integration-logs-* -D /tmp/

3. Phase 2:
   Match error string against the tables above.
   Load the matching resources/<topic>.md for the deep dive.

4. Phase 3:
   Read source at cited line. Verify which binary is actually running
   (fission-bundle/fetcher = per-PR; env-builder images = pre-built GHCR).
   Run focused local tests + helm template if Helm/skaffold changed.

5. Phase 4:
   Push. Arm Monitor with the poll loop (timeout 2400000ms).
   On notification: if green, done. If red, restart Phase 1 with new logs —
   don't reuse the previous hypothesis without re-reading.
```

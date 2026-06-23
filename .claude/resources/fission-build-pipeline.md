# Fission build pipeline

How a `Package` gets built, where each container's log lives, the `/packages` shared-volume permission model, and why env-builder images aren't rebuilt per-PR.
Reference for the build-pipeline rows in [`fission-ci-failure-patterns.md`](fission-ci-failure-patterns.md).

## End-to-end flow for a v2 (source) package build

```
User CLI → k8s API → Package CR (state=pending)
                          │  watched by:
                          ▼
                     buildermgr controller (in fission-bundle)
                          │  1. fetch source archive
                          ▼
                     fetcher sidecar (in env-builder pod)  ──HTTP GET archive.URL──▶ storagesvc
                          │                                     extracts to /packages/<srcPkgFilename>/
                          │  2. trigger build
                          ▼
                     builder container (in env-builder pod)
                          │  exec.Command(buildCommand)
                          │  SRC_PKG=/packages/<srcPkgFilename>
                          │  DEPLOY_PKG=/packages/<srcPkgFilename>-<rand>
                          │  3. upload deploy archive
                          ▼
                     fetcher sidecar  ──utils.Archive + storagesvc.Upload──▶ Package CR (state=succeeded)
                          │  4. cleanup
                          ▼
                     builder Clean handler removes /packages/<srcPkgFilename>
                     fetcher UploadHandler defer removes /packages/<deployPkg>
```

## Symptom → failing step

| Test message | Failing step |
|---|---|
| `package "<pkg>" never reached build status "succeeded" (last="pending")` | Step 1 missed — buildermgr didn't pick up the CR. Check `logs-buildermgr-*` (in the `kind-logs-*` artifact). |
| `(last="running", last build log: )` (empty) | Step 2 stuck — env-builder pod not Ready / builder probe failing. Look at the env-builder Deployment's `events.yaml`. |
| `(last="failed", last build log: <stuff>)` | Step 2 ran but the script returned non-zero, OR step 3 failed. Read the build log. |
| `... no files found for globs: [/packages/<deploy>/*]` | Step 3 — deploy dir empty/unreadable. Cross-check shared-volume permissions below. |
| `... failed to get source directory info: stat /packages/<deploy>: no such file or directory` | Step 2 build script never wrote `$DEPLOY_PKG` (crashed mid-way or wrote the wrong path). |
| `... dial tcp <ip>:80: i/o timeout` | Step 3 reached fetcher but fetcher → storagesvc was blocked. NetworkPolicy — see [`fission-ci-failure-patterns.md`](fission-ci-failure-patterns.md). |

## Where each container's log lives

In a downloaded `go-integration-logs-*` artifact, per-test diag dirs contain:

| File | Source |
|---|---|
| `logs-<env>-<rv>-<hash>-builder.log` | The `builder` container — env image's `/builder` binary. Logs build-script stdout/stderr between `===== START =====`/`===== END =====` markers. |
| `logs-<env>-<rv>-<hash>-fetcher.log` | The `fetcher` sidecar of the **env-builder pod**. Handles fetch (step 2 input) and upload (step 3 output). |
| `logs-poolmgr-<fn>-*-fetcher.log` / `logs-newdeploy-<fn>-*-fetcher.log` | The fetcher sidecar of a **function pod**. Only specialise-time downloads, never uploads. |
| `logs-poolmgr-<fn>-*-<env-runtime>.log` | The user function's runtime container. |

Controller logs (buildermgr, executor, router, storagesvc) come from the `kind-logs-<runId>-v<k8s>` artifact under `fission/`-namespace pod logs — download those only when you suspect a step-1 controller issue.

Reading a builder log: a success shows `builder received request` → `building source package` → `START` → script output → `END` → `build request complete`.
Failure modes: `error starting cmd` (the `exec.Command` failed before running — script perms / PATH); `error waiting for cmd` (process exited non-zero — read between `START`/`END`); no `END` marker (capture truncated: `bufio.Scanner` hit its 64KB line limit, or the pod was killed mid-build).

## `/packages` shared-volume permissions

The env-builder pod is **two containers** sharing `/packages` as an `emptyDir`:
- `builder` — the env image (e.g. `python-builder`), typically root (UID 0).
- `fetcher` — Fission's sidecar, UID 10001 (`values.yaml` `podSpec.securityContext.runAsUser`).

The build script (builder) writes deploy artefacts; the fetcher reads them back to upload.
Cross-container access needs the right modes.

**The trap:** parent dirs created `0o750` (`rwxr-x---`) are owned by root but the fetcher (UID 10001, different group) can't traverse them.
Symptoms:
```
"error":"openfdat /packages/<pkg>-<rand>: permission denied"
"error":"failed to get source directory info: stat /packages/<pkg>-<rand>: no such file or directory"
"error":"no files found for globs: [/packages/<pkg>-<rand>/*]"
```
(The third happens when the fetcher *can* stat the dir — parent was world-readable — but can't list its contents.)

**Fix:** `/packages`-rooted directories must be world-readable — `0o755` for dirs (files inherit umask, so `0o644` is fine; only directory traversal mode matters).
Enforced in `pkg/utils/zip.go` `Unarchive` (`os.MkdirAll(..., os.ModeDir|0o755)`); the build script's `cp -r ${SRC_PKG} ${DEPLOY_PKG}` preserves perms, so the `Unarchive` mode flows through.

When you tighten a file mode elsewhere, check whether the path is reachable by the fetcher sidecar:
- On `/packages/...` → dirs must be `0o755`.
- On the user's local filesystem (CLI tooling — `dump.go`, package util, support resources) → `0o700`/`0o600` is fine.
- On a log volume read by node-level log shipping → `0o755`.

Why not run both containers as the same UID: env-builder images are pre-built upstream and many install deps as root; forcing non-root breaks user environments.
And control-plane pods run as UID 10001 to limit blast radius.
So the UID asymmetry stands and we use world-readable dirs.

Verify before pushing (unit tests can't simulate UID separation — the integration suite is what catches this):
```bash
go test -race -count=1 ./pkg/utils/... ./pkg/storagesvc/...
grep -E 'permission denied|openfdat|EACCES' /tmp/ci-logs/test*/logs-*-fetcher.log | head
```

## Env-builder images are NOT rebuilt per-PR

Binaries reach the cluster two ways:
1. **Control-plane images** — built per-PR by `make skaffold-deploy` (goreleaser): `fission-bundle` (multi-headed: buildermgr, executor, router, storagesvc, kubewatcher, timer, mqtrigger, canary, webhook, …), `fetcher`, `pre-upgrade-checks`, `reporter`.
2. **Environment images** — pre-built and published to GHCR by env maintainers, separately from this repo: `ghcr.io/fission/python-builder`, `node-builder-22`, `go-builder-1.23`, plus one runtime image per env.
   Each is `<lang base> + /builder binary baked in + /build script`.
   **The `/builder` binary was compiled when that image was built and pushed — not from this PR's source.**

So a change to `pkg/builder/builder.go` does not change env-builder pod behaviour in CI integration tests until those images are republished.
If integration tests fail with **old** behaviour while your unit tests pass and lint is clean, this is almost certainly the cause — check the `caller":"builder/builder.go:NN"` line in the builder pod log against your local source.

`pkg/builder` Go tests (`builder_test.go`) run in `make test-run` (isolated package logic), NOT in the integration job.
Ways to actually exercise `cmd/builder`/`pkg/builder` changes in a cluster: a Go unit/integration test that avoids the env image; build a local env-builder image (Dockerfiles live in the separate `fission/environments` repo) with your patched binary and override `Builder.Image` in the test Environment CR; or move the behaviour into `pkg/buildermgr` (the controller, in fission-bundle, which IS rebuilt per-PR).

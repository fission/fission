# Fission build pipeline — what happens when a Package is built

Most integration-test failures involving `pkg/builder`, `pkg/buildermgr`, `pkg/fetcher`, or `pkg/storagesvc` come from misunderstanding which component does what at which step. This file is the reference.

## End-to-end flow for a v2 (source) package build

```
User CLI  →  k8s API  →  Package CR (state=pending)
                              │
                              │  watched by:
                              ▼
                         buildermgr controller (in fission-bundle)
                              │
                              │  1. fetch source archive
                              ▼
                         fetcher sidecar (in env-builder pod)
                              │  HTTP GET archive.URL
                              ▼
                         storagesvc  ──→  /packages/<srcPkgFilename>/  (extracted)
                              │
                              │  2. trigger build
                              ▼
                         builder container (in env-builder pod)
                              │  exec.Command(buildCommand)
                              │  with SRC_PKG=/packages/<srcPkgFilename>
                              │       DEPLOY_PKG=/packages/<srcPkgFilename>-<rand>
                              │
                              │  3. upload deploy archive
                              ▼
                         fetcher sidecar (same env-builder pod)
                              │  utils.Archive(srcFilepath, dstFilepath)
                              │  storagesvc.Upload(dstFilepath)
                              ▼
                         storagesvc  ──→  Package CR (state=succeeded, with deploy archive ID)
                              │
                              │  4. cleanup
                              ▼
                         builder container (Clean handler) — removes /packages/<srcPkgFilename>
                         fetcher (UploadHandler defer) — removes /packages/<deployPkg>
```

## Mapping symptom → likely failing step

| Test message | Failing step |
|---|---|
| `package "<pkg>" never reached build status "succeeded" (last="pending")` | Step 1 missed entirely. buildermgr controller didn't pick up the CR. Check `logs-buildermgr-*` (in `kind-logs-*` artifact, not the per-test artifact). |
| `(last="running", last build log: )` (empty log) | Step 2 stuck — env-builder pod not Ready, or builder container probe failing. Look at events.yaml for the env-builder Deployment. |
| `(last="failed", last build log: <stuff>)` with build-script output | Step 2 ran but the script returned non-zero, OR step 3 failed. Read the build log for clues. |
| `Error uploading deployment package: ... no files found for globs: [/packages/<deploy>/*]` | Step 3 — the deploy directory is empty or unreadable. Cross-check shared-volume permissions (see `shared-volume-permissions.md`). |
| `Error uploading deployment package: ... failed to get source directory info: stat /packages/<deploy>: no such file or directory` | Step 2 build script never wrote to `$DEPLOY_PKG`. Either the script crashed mid-way (check `cmd.Wait` errors in builder log), or it wrote to the wrong path. |
| `Error uploading deployment package: ... dial tcp <ip>:80: i/o timeout` | Step 3 reached fetcher but fetcher → storagesvc was blocked. NetworkPolicy issue (see `networkpolicy-debugging.md`). |

## Where each container's log lives

In a downloaded `go-integration-logs-*` artifact, per-test diag dirs contain:

| File | Comes from |
|---|---|
| `logs-<env>-<rv>-<deploy-hash>-builder.log` | The `builder` container — env image's `/builder` binary. Logs build-script stdout/stderr verbatim between `===== START =====` / `===== END =====` markers, plus structured logger lines around them. |
| `logs-<env>-<rv>-<deploy-hash>-fetcher.log` | The `fetcher` sidecar of the **env-builder pod**. Handles fetch (step 2 input) and upload (step 3 output). |
| `logs-poolmgr-<fn>-*-fetcher.log` / `logs-newdeploy-<fn>-*-fetcher.log` | The fetcher sidecar of a **function pod**. Only handles specialise-time downloads (step 2 of a different flow), never uploads. |
| `logs-poolmgr-<fn>-*-<env-runtime-name>.log` | The user function's *runtime* container (the actual code execution). |

Higher-level logs (buildermgr controller, executor controller, router, storagesvc) come from a different artifact — `kind-logs-<runId>-v<k8s>` — under `fission/` namespace pod logs. Only download those if you suspect a controller issue (step 1 failure).

## Reading a builder log

A successful build log looks like:
```
{"level":"info","msg":"builder received request","srcPkgFilename":"...","buildCommandLen":11}
{"level":"info","msg":"building source package","command":"./build.sh","args":[],"env":[...]}
========= START =========
<actual build script stdout/stderr>
========= END ===========
{"level":"info","msg":"build request complete","elapsed_time":...}
```

Failure modes:
- Logger line `"error starting cmd"` → the `exec.Command()` call failed before the process ran. Permission issue on the script, or PATH lookup failure.
- Logger line `"error waiting for cmd"` → the process started but exited non-zero. Look at the captured stdout/stderr between `START`/`END` for the script's own error message.
- No `END` marker at all → log capture was truncated. Either `bufio.Scanner` hit its 64KB line limit (pip progress bars do this), or the pod was killed mid-build.

## Builder image vs Fission image distinction

The env-builder pods run images from `ghcr.io/fission/<lang>-builder` (e.g. `python-builder`, `node-builder-22`). Each image has the `/builder` binary baked in **at build time** of that image — not rebuilt per-PR.

Fission CI's `make skaffold-deploy` step builds:
- `fission-bundle` (multi-headed, hosts buildermgr, executor, router, storagesvc, etc.)
- `fetcher` (sidecar image)
- `pre-upgrade-checks`
- `reporter`

It does **not** rebuild the env-builder images. So a change to `pkg/builder/builder.go` does not affect env-builder pod behaviour in CI integration tests until those images are republished. Confirm by reading the `caller":"builder/builder.go:NN"` field in pod logs and comparing line numbers to your local file. See `builder-image-origin.md` for more.

## The `BuildCommand` allowlist

After the 2026-05 security fixes, `pkg/builder/builder.go` rejects `BuildCommand` strings that contain shell metacharacters (`;|&\`$()<>\n\r`). If a previously-working test starts failing with HTTP 400 and message "error: invalid buildCommand: contains shell metacharacter", the fix is to rewrite the command to not need shell features (e.g. push the `&&` into the script's shebang line — `#!/bin/sh` will interpret it inside the script body). See `pkg/builder/builder.go` `resolveBuildCommand`.

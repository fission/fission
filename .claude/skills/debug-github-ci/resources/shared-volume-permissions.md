# Shared-volume (`/packages`) permissions

The env-builder pod is a **two-container pod**:
- `builder` — the env image (e.g. `python-builder`), typically running as **root** (UID 0).
- `fetcher` — Fission's sidecar, running as **UID 10001** (Fission pod default per `values.yaml`'s `podSpec.securityContext.runAsUser`).

Both mount `/packages` as a shared `emptyDir` volume. The build script (in `builder`) writes deploy artefacts there; the `fetcher` sidecar reads them back to upload to storagesvc. Cross-container access requires the right modes.

## The trap: `0o750` breaks the v2 builder flow

If parent directories on `/packages` are created with mode `0o750` (`rwxr-x---`), the file owner is root (the builder container's UID) but the fetcher (UID 10001, in a different group) can't traverse them. Symptoms:

```
"error":"openfdat /packages/<pkg>-<rand>: permission denied"
"error":"failed to get source directory info: stat /packages/<pkg>-<rand>: no such file or directory"
"error":"no files found for globs: [/packages/<pkg>-<rand>/*]"
```

(The third one happens when the fetcher *can* stat the dir — because the parent above it was world-readable — but can't list its contents.)

The fix: `/packages`-rooted directories must be world-readable, i.e. mode `0o755` (`rwxr-xr-x`) for dirs, default-mode (`0o644` after umask) for files. The build script's output files inherit umask from the builder process, so they're fine; only the *directory* mode matters for traversal.

## Where this is enforced

| File | What it does | Required mode |
|---|---|---|
| `pkg/utils/zip.go` `Unarchive` | Creates parent directories during package extraction (`os.MkdirAll(filepath.Dir(destPath), os.ModeDir|<mode>)`) | `0o755` |
| `pkg/builder/builder.go` | The build script's `cp -r ${SRC_PKG} ${DEPLOY_PKG}` preserves perms via busybox cp on Alpine; if SRC was `0o755` then DEPLOY is too. So `Unarchive`'s mode flows through. | (inherits from upstream) |

If you're tightening file modes elsewhere (CLI dump output, log directories, etc.), it's safe — those don't go through `/packages` and don't need cross-container access. The `0o600` / `0o700` modes used in `pkg/fission-cli/cmd/support/dump.go`, `pkg/fission-cli/cmd/package/util/util.go`, and `pkg/fission-cli/cmd/support/resources/resource.go` are all on the user's local filesystem, not the cluster shared volume.

## How to verify before pushing

```bash
go test -race -count=1 ./pkg/utils/... ./pkg/storagesvc/...
```
The unit tests catch obvious regressions but **don't catch the cross-container case** (single-process tests can't simulate UID separation). The integration test suite is what catches it. The smoking-gun symptom in CI is `permission denied` on `openfdat` — search fetcher logs:

```bash
grep -E 'permission denied|openfdat|EACCES' /tmp/ci-logs/test*/logs-*-fetcher.log | head
```

## Why we can't just run both containers as the same UID

Two reasons:
1. The env-builder images are pre-built on GHCR by upstream maintainers; many of them (e.g. `python-builder`) are designed to install dependencies system-wide as root. Forcing them to non-root would break a lot of existing user environments.
2. The Fission default `podSpec.securityContext` runs control-plane pods as UID 10001 to limit blast radius of any compromise. Loosening that affects more than just this pod.

So we accept the UID asymmetry and use world-readable dirs on the shared volume.

## When you change a mode in this area

Always check whether the path can be reached by the fetcher sidecar. Quick decision tree:
- Path is on `/packages/...`? → must be `0o755` for dirs.
- Path is on the user's local filesystem (CLI tooling)? → tightening to `0o700`/`0o600` is fine.
- Path is on a log volume read by something like fluent-bit on the host? → `0o755` to allow node-level log shipping.

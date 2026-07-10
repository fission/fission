# Fission CI failure patterns

Repo-specific knowledge for the generic `debug-ci` skill: the handful of failure modes Fission CI hits repeatedly, plus the NetworkPolicy and CI-only-Helm-flag mechanics behind them.
Match the error string here before reading a full log.
Companion docs: [`fission-build-pipeline.md`](fission-build-pipeline.md), [`fission-integration-test-quirks.md`](fission-integration-test-quirks.md).

## Known stickers (don't chase — red on `main` by policy)

- **`License Compliance` (FOSSA)** — fails persistently on `main` with "11 issues found".
- **Tests requiring a local Docker daemon** (e.g. `TestS3StorageService` in `pkg/storagesvc/client`) — failures locally without `dockerd` are environmental.

## Symptom → cause

### Network / connectivity

| Symptom | Root cause | More |
|---|---|---|
| `dial tcp <ip>:<port>: i/o timeout` (Fission pod → storagesvc) | NetworkPolicy dropping the request, or pod labels don't match policy selectors | "NetworkPolicy" below |
| `dial tcp <router-internal-clusterIP>:8889: i/o timeout` in a *caller's* pod log | A fission-bundle head that calls the router internal listener is missing from the allowlist | "Router internal-listener allowlist" below |
| `portless: waiting for route "router.fission": ... not ready within 60s` (or another `*.fission` route) | The in-process port-forward found no ready pod behind the Service — the deploy is unhealthy or `FISSION_NAMESPACE` is wrong. Check the Service's pods, not a kubectl forward (there is none) | [`fission-integration-test-quirks.md`](fission-integration-test-quirks.md) |
| `404` from `/fission-function/...` on the public route | Request went to the public listener; the listener split moved those routes to `router-internal.fission` (use `f.Router(t)` / `f.RouterInternalBaseURL()`) | [`fission-integration-test-quirks.md`](fission-integration-test-quirks.md) |
| `404 Not Found` from `raw.githubusercontent.com/...` | Test-data URL broken upstream. Not a regression | — |

### Filesystem / permissions

| Symptom | Root cause | More |
|---|---|---|
| `permission denied` / `openfdat …: permission denied` on `/packages/...` | Cross-container UID mismatch on the shared volume; `0o750` breaks fetcher access, need `0o755` | [`fission-build-pipeline.md`](fission-build-pipeline.md) |
| `no files found for globs: [/packages/<deploy>/*]` | Deploy dir empty or unreadable from fetcher | [`fission-build-pipeline.md`](fission-build-pipeline.md) |
| `failed to get source directory info: stat /packages/<path>: no such file or directory` | Build script never wrote the path; pipeline broke earlier | [`fission-build-pipeline.md`](fission-build-pipeline.md) |

### Build pipeline

| Symptom | Root cause | More |
|---|---|---|
| `package "<pkg>" never reached build status "succeeded"` | One of the buildermgr → builder → fetcher steps failed | [`fission-build-pipeline.md`](fission-build-pipeline.md) |
| `Error uploading deployment package: ...` | fetcher's UploadHandler failing — the wrapped error is the real cause | [`fission-build-pipeline.md`](fission-build-pipeline.md) |
| `bufio.Scanner: token too long` in builder log | Build-script line >64 KB (pip progress bars do this) | [`fission-build-pipeline.md`](fission-build-pipeline.md) |
| `error: invalid buildCommand: contains shell metacharacter` (HTTP 400) | Post-2026-05 `BuildCommand` allowlist rejects shell metachars; rewrite the command to push `&&` into the script's shebang body | `pkg/builder/builder.go` `resolveBuildCommand` |
| `dial tcp <env-svc>:8000: i/o timeout` after `CreateEnv(Builder=...)` | Builder/runtime pod readiness race | [`fission-integration-test-quirks.md`](fission-integration-test-quirks.md) |

### Lint / Go module

| Symptom | Root cause |
|---|---|
| `printf: non-constant format string in call to ... (govet)` | Caller passed `fmt.Sprintf(...)` as a printf-helper format arg. Pass `format, args...` directly, or wrap the dynamic string with `"%s", str`. |
| `invalid version: unknown revision v29.x.y` for `github.com/docker/docker` | Docker re-tagged that import path with `docker-vX.Y.Z` prefixed tags the proxy doesn't index. Bump `docker/cli` to v29.x — its transitive `moby/moby/v2` import drops `docker/docker` from go.mod entirely. |
| `go: github.com/foo/bar@vX: invalid version` | Module path moved or got retracted. Check upstream `releases/latest` and actual git tags. |

## Which binary actually ran

`fission-bundle`, `fetcher`, `pre-upgrade-checks`, `reporter` images are built **per-PR** by `make skaffold-deploy`.
Env-builder images (`python-builder`, `node-builder-22`, `go-builder-1.23`, …) are **pre-built on GHCR** — their `/builder` binary was compiled at image-build time, not per-PR, so a change to `pkg/builder/builder.go` does NOT affect their behaviour in CI integration tests.
Confirm with the `caller":"builder/builder.go:NN"` field in pod logs vs your local line numbers.
Full explanation in [`fission-build-pipeline.md`](fission-build-pipeline.md).

## NetworkPolicy

When a Fission pod can't reach another Fission service and the symptom is `dial tcp <ip>:<port>: i/o timeout`, NetworkPolicy is the usual suspect.
`networkPolicy.enabled=true` in the **kind-ci** skaffold profile, so a policy bug passes unit tests and `helm lint` but fails the integration leg only.

```bash
kubectl get networkpolicies -n fission
kubectl describe networkpolicy <name> -n fission
```

### Pod labels are the source of truth

A `from: [{ podSelector: ... }]` rule matches **pod labels**, not Service/Deployment names.
Controllers and the worker pods they create use different conventions:

| Pod kind | Labels |
|---|---|
| `buildermgr` / `executor` controller | `svc=buildermgr` / `svc=executor` |
| `router` controller | `svc=router, application=fission-router` |
| `storagesvc` | `svc=storagesvc, application=fission-storage` |
| Per-env builder pods (builder + fetcher sidecar) | `owner=buildermgr, envName=<env>, envNamespace=<ns>, envResourceVersion=<rv>` |
| Function pods | `executorType=poolmgr\|newdeploy\|container, functionName=<name>, executorInstanceId=<id>, managed=<bool>` |

A rule targeting the *fetcher sidecar in worker pods* selects on `owner=buildermgr` (env-builder pods) and `executorType in [...]` (function pods) — **not** `svc=*` (controllers don't talk to storagesvc directly).
Constants: `pkg/apis/core/v1/const.go` (function-pod labels), `pkg/buildermgr/envwatcher.go` `getLabels()` (env-builder pod labels).

### Cross-namespace vs same-namespace selectors

storagesvc lives in `fission`; env-builder and function pods live in user namespaces (`default` in tests).
A `podSelector` rule matches **only the policy's own namespace** unless paired with a `namespaceSelector`:
```yaml
- from:
    - namespaceSelector: {}        # match labelled pods in ANY namespace
      podSelector:
        matchLabels: { owner: buildermgr }
```
For same-namespace rules (caller and target both in `fission`), use `podSelector` alone — it implicitly scopes to the policy's namespace.
Do **not** add `namespaceSelector.matchLabels.kubernetes.io/metadata.name: <ns>` "to be explicit": that label is auto-populated by an admission plugin that isn't guaranteed across every k8s version/distribution, so it silently no-ops where the plugin is off.
Canonical example: `charts/fission-all/templates/networkpolicy.yaml`.

### Router internal-listener allowlist — add every new internal client

`charts/fission-all/templates/router/networkpolicy.yaml` gates ingress to the internal listener (port 8889, serving `/fission-function/...`) to an **explicit allowlist** of source pod labels: `svc: kubewatcher | timer | mqtrigger | mqtrigger-keda | canaryconfig | executor | buildermgr | mcp`.
Any **new** fission-bundle head that calls the internal listener must be added to that `from` block or its signed requests are silently dropped — surfacing only in the integration leg as `dial tcp <router-internal-clusterIP>:8889: i/o timeout` in the *caller's* pod log (NOT the router's).
These are same-namespace rules, so `podSelector` alone is correct.
A caller running in `fission` (e.g. MCP) has its pod log in the CI `kind-logs-<run>-<ver>` artifact, not the `default`-scoped diagnostics dump.

### Is the CNI even enforcing?

kindnet enforces NetworkPolicy from kind v0.27 / k8s 1.30+.
On older kind or EKS-without-addon, policies are accepted by the API but never enforced.
If a policy looks correct but doesn't change behaviour:
```bash
kubectl get pods -n kube-system -o name | grep -E 'kindnet|cilium|calico|weave'
```

## CI-only Helm features via the `kind-ci` skaffold profile

Some chart features are off by default for users but should be **on** in CI to exercise the code path.
The repo flips them via the `kind-ci` profile.
Examples already using it: `canaryDeployment.enabled`, `podMonitor`/`serviceMonitor.enabled`, `networkPolicy.enabled` (2026-05), `storagesvc.archivePruner.interval: 1`, and `mcp.enabled`+`mcp.allowInsecure` (2026-06, set in the **base** `setValues` so the MCP head runs in every kind profile).

Two-step pattern (skaffold uses JSONPatch, whose `replace` requires the path to exist):
1. Declare the value with its **chart-default** in the base `manifests.helm.releases[0].setValues` map in `skaffold.yaml` (e.g. `networkPolicy.enabled: "false"`).
2. Add a `replace` patch in the `kind-ci` profile: ```yaml
   - op: replace path: /manifests/helm/releases/0/setValues/networkPolicy.enabled value: true
   ```
Skipping step 1 makes render fail with `path does not exist`. The path uses dotted key segments because the base map is keyed that way. Mirror into `kind-ci-old` only if the feature should also be tested against older releases.

Verify the render:
```bash
helm lint charts/fission-all helm template charts/fission-all --set networkPolicy.enabled=true | sed -n '/^kind: NetworkPolicy/,/^---/p' | head -50 helm template charts/fission-all --set networkPolicy.enabled=false | grep -c 'kind: NetworkPolicy'   # 0
```
If the feature is user-tunable, also add it to `charts/fission-all/values.yaml`; if it's a pure internal CI knob, the skaffold patch alone is enough.

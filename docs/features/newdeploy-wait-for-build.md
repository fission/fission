# Newdeploy wait-for-build

Make the newdeploy executor wait for a function's package to finish building
before it provisions the function's Deployment. Without this, a function whose
package is still building gets a Deployment whose fetcher sidecar
CrashLoopBackOffs trying to fetch a deploy archive that does not exist yet.

## How it works

`waitForBuild` (`pkg/executor/executortype/newdeploy/newdeploymgr.go`) is called
at the start of both `fnCreate` and `updateFuncDeployment`. It polls the
referenced package's `BuildStatus`:

| BuildStatus | Action |
|---|---|
| `succeeded` / `none` | ready — provision |
| `failed` | return an error, skip provisioning (the pod would crash-loop forever) |
| `pending` / `running` / empty / not-yet-visible | keep polling |

If the build does not settle within `NEWDEPLOY_BUILD_WAIT_TIMEOUT` it provisions
**anyway** — a slow build can never permanently block the function (the fetcher
sidecar then retries until the build lands). The poll is cancellation-aware: on
executor shutdown / loss of leadership it returns promptly instead of holding a
reconcile worker for the full window.

> The poolmgr executor does not need this: pool pods are generic and specialize
> lazily, so they are not tied to a specific unbuilt package at creation time.

## Configuration

| Setting | Default | Meaning |
|---|---|---|
| `NEWDEPLOY_BUILD_WAIT_TIMEOUT` (executor env) | 600s | Max seconds to wait for the build before provisioning anyway |

## Relationship to the buildermgr

The buildermgr's Package reconciler builds packages independently. This feature is
the executor-side guard so newdeploy provisioning and the build don't race. The
two are complementary: the buildermgr produces the deploy archive; `waitForBuild`
ensures newdeploy doesn't provision until it exists.

## Key files

- `pkg/executor/executortype/newdeploy/newdeploymgr.go` — `waitForBuild`, called in `fnCreate` and `updateFuncDeployment`

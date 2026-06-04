# Builder scale-to-zero

Scale an environment's builder Deployment to zero replicas after it has been idle
(no builds) for a configurable window, and warm it back on demand when the next
build arrives. Frees resources for environments that build rarely without
permanently tearing the builder down.

## How it works (controller-runtime)

A leader-only `idleBuilderReaper` Runnable (`pkg/buildermgr/idle_reaper.go`) is
registered on the buildermgr Manager. Every `BUILDER_IDLE_REAPER_INTERVAL` it asks
the shared `BuilderPoolManager` for environments that are idle — no in-flight
builds, a positive idle timeout, and the idle window elapsed since the last build
— and scales those builder Deployments to zero (via the `deployments/scale`
subresource). It re-checks "not building" immediately before each scale to avoid
racing a build that just started, and flags a builder it has scaled so it is not
re-scaled every tick.

Warm-up is automatic: the next build's [demand scaling](builder-concurrency.md)
scales the Deployment back up, and the package reconciler waits for a Ready pod
before dispatching (a missing/0-replica builder requeues rather than failing).

## Configuration

| Setting | Default | Meaning |
|---|---|---|
| `spec.builder.idleTimeout` / `fission env … --builder-idletimeout` | 600 (10 min) | Seconds of idleness before scaling to zero. **0 disables** scale-to-zero (builder stays warm). |
| `BUILDER_IDLE_REAPER_INTERVAL` (buildermgr env) | 10s | How often the reaper sweeps. Falls back to `OBJECT_REAPER_INTERVAL`. |

```bash
# keep this env's builder warm for an hour of idleness
fission env create --name go --image fission/go-env --builder fission/go-builder --builder-idletimeout 3600

# never scale this builder to zero
fission env update --name go --builder-idletimeout 0
```

## RBAC

The buildermgr Role/ClusterRole must grant `apps/deployments/scale` (`update`) and
`apps/deployments` (`get`) — provisioned by the chart
(`_fission-kubernetes-roles.tpl`). Without it the reaper and the demand scaler are
RBAC-forbidden and builds fail to dispatch.

## Key files

- `pkg/buildermgr/idle_reaper.go` — the reaper Runnable
- `pkg/buildermgr/builderpool.go` — idle eligibility (`ReapTargets`), `IsBuilding`
- `pkg/buildermgr/buildermgr.go` — `builderIdleReaperInterval`, reaper registration
- `pkg/apis/core/v1/types.go` — `Builder.IdleTimeout`
- `charts/fission-all/templates/_fission-kubernetes-roles.tpl` — `deployments/scale` RBAC

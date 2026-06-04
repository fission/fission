# Concurrent builder pods

Provision builder pods on demand — one dedicated pod per concurrent package
build, up to a per-environment cap — so independent builds run in parallel and in
isolation (an OOM in one build does not affect the others).

## How it works (controller-runtime)

The buildermgr runs an Environment reconciler and a Package reconciler that share
a `BuilderPoolManager` (`pkg/buildermgr/builderpool.go`), the in-memory state that
replaced the old `environmentWatcher` cache.

When a package build starts, the `PackageReconciler` (`package_reconciler.go`):

1. records the build as in-flight (demand) in the pool manager — demand is a set
   keyed by package, so reconcile requeues never double-count it;
2. scales the environment's builder Deployment **up** toward the number of
   concurrent builds, capped at `spec.builder.poolsize`;
3. claims a Ready builder pod that no other build is using and routes the whole
   build (fetch + build + upload) to **that pod's IP**. Pod-IP pinning is required
   for correctness with more than one replica: the source archive is fetched onto
   one pod's local volume, so the build must run on the same pod.

Scaling is **up-only** here; the [idle reaper](builder-scale-to-zero.md) is the
only thing that scales builders down, so a pod running a build is never
terminated underneath it. If every Ready pod is busy (the pool is at its cap),
the build requeues and waits its turn rather than blocking a worker.

## Configuration

| Setting | Default | Meaning |
|---|---|---|
| `spec.builder.poolsize` / `fission env … --builder-poolsize` | 1 | Max builder pods per environment (a ceiling, not a fixed replica count) |
| `MAX_PARALLEL_BUILDS` (builder pod env) | 1 | Builds allowed concurrently **inside one** builder pod (semaphore in `pkg/builder/builder.go`) |
| `BUILDERMGR_PACKAGE_CONCURRENCY` (buildermgr env) | 5 | Max package reconciles (i.e. in-flight builds) the buildermgr runs at once |

Effective parallelism per environment ≈ `poolsize × MAX_PARALLEL_BUILDS`, bounded
by `BUILDERMGR_PACKAGE_CONCURRENCY` across all environments. For pod-per-build
isolation keep `MAX_PARALLEL_BUILDS=1` and raise `poolsize`.

```bash
fission env create --name go --image fission/go-env --builder fission/go-builder --builder-poolsize 4
```

## Key files

- `pkg/buildermgr/builderpool.go` — `BuilderPoolManager` (demand, pod claims)
- `pkg/buildermgr/package_reconciler.go` — `scaleBuilderForDemand`, pod claim, pod-IP routing
- `pkg/buildermgr/common.go` — `buildPackage` takes a builder pod IP
- `pkg/builder/builder.go`, `cmd/builder/app/server.go` — per-pod build semaphore
- `pkg/apis/core/v1/types.go` — `Builder.PoolSize`

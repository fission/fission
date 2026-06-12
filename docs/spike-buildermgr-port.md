# Spike: porting buildermgr fork features onto controller-runtime (Phases 3–4)

Status: **IMPLEMENTED.** Signed-off decisions: D1 = requeue-based (non-blocking),
D2 = uniform pod-IP, D3 = regression test only (no skip ported). Delivered as
`builderpool.go`, `idle_reaper.go`, `scale.go` (+ tests) and changes to
`environment_reconciler.go`, `package_reconciler.go`, `common.go`, `buildermgr.go`.
One correctness addition beyond the proposal: `BuilderPoolManager.RemoveBuild`,
called from the package reconciler's deleted-package branch, releases a demand
slot if a package is deleted while requeue-waiting (prevents a phantom-demand leak).

## 1. How upstream works today (grounded)

**EnvironmentReconciler** (`environment_reconciler.go`) — level-based, stateless.
Per Environment reconcile it deletes stale builders (RV mismatch) and ensures a
builder **Service + Deployment** named `<env.Name>-<env.ResourceVersion>` exist.
`createBuilderDeployment` hardcodes `replicas = 1` (`:368`). Status-silent.

**PackageReconciler** (`package_reconciler.go`) — triggered by `BuildStatus → pending`
(`buildTriggerPredicate`, `:281`), bounded by `MaxConcurrentReconciles` (default 5,
`BUILDERMGR_PACKAGE_CONCURRENCY`). `build()` (`:106`):
1. cross-namespace guard → get env → resolve `builderNs`
2. `builderPodReady()` — is *any* builder pod ready? if not, `RequeueAfter: 5s` (`:142`)
3. set `Running` → `buildPackage(...)` → update functions → set `Succeeded`/`Failed`

**buildPackage** (`common.go:57`) routes via **service DNS**:
```go
svcName := fmt.Sprintf("%s-%s.%s", env.Name, env.ResourceVersion, envBuilderNamespace)
fetcherC := ...("http://%s:8000", svcName)   // fetch source
builderC := ...("http://%s:8001", svcName)   // build
```

### The core incompatibility
With `replicas > 1`, the ClusterIP service load-balances **per connection**, so the
fetch (8000) and the build (8001) can land on **different pods** — but the fetched
source lives on the *local* volume of the pod that received the fetch. So pod-IP
pinning is **not an optimization, it is required** for any concurrency > 1.

And the new reconcilers are **stateless**, while the fork's concurrency needs shared,
in-memory, per-environment state (active builds, busy pod IPs, last-build time).

## 2. Fork features to port

| # | Feature | Where it has to live now |
|---|---------|--------------------------|
| 1 | Scale-to-zero idle reaper (`IdleTimeout`, default 600s; 0 = never) | new periodic leader-only Runnable |
| 2 | On-demand concurrent builder pods (`PoolSize`, default 1) | `PackageReconciler.build()` scale-up |
| 3 | Pod-IP routing (pin fetch+build+upload to one pod) | `common.go buildPackage` + claim in `build()` |
| 4 | "Infinite build" fix (skip `Running`) | **likely already handled** upstream — see §6 |

## 3. Proposed design — `BuilderPoolManager`

A single thread-safe struct constructed in `Start()` and injected into **both**
reconcilers and the reaper. Replaces the old `environmentWatcher`'s in-memory cache.

```go
type buildKey struct{ Namespace, Name string } // a Package identity

type builderState struct {
    mu            sync.Mutex
    envName       string
    envNamespace  string
    envRV         string
    builderNS     string
    idleTimeout   int64                  // seconds; 0 = never scale to zero
    poolSize      int32                  // max pods (>=1)
    inFlight      map[buildKey]struct{}  // packages currently building (IDEMPOTENT set)
    busyPodIPs    map[string]bool        // live pod claims
    lastBuildTime time.Time
}

type BuilderPoolManager struct {
    mu     sync.RWMutex
    states map[types.UID]*builderState   // keyed by Environment UID
    logger logr.Logger
}
```

Methods (each takes the env so state is lazily created / kept fresh by whoever calls):
- `Ensure(env, builderNS)` — upsert descriptive fields; init `lastBuildTime=now` if new.
  Called by **EnvironmentReconciler** every reconcile and by `build()` defensively.
- `Forget(uid)` — drop state. Called by EnvironmentReconciler's NotFound branch.
- `StartBuild(env, pkg, builderNS) (demand int32)` — `Ensure` + add `pkg` to `inFlight`
  (idempotent) + refresh `lastBuildTime`; returns `len(inFlight)`.
- `FinishBuild(uid, pkg)` — remove from `inFlight` + refresh `lastBuildTime`.
- `ClaimFreeBuilderPod(uid, candidateIPs) (ip, ok)` / `ReleaseBuilderPod(uid, ip)`
- `RefreshLastBuildTime(uid)`
- `ReapTargets(now) []{builderNS, builderName}` — snapshot of envs that are idle
  (`len(inFlight)==0 && idleTimeout>0 && now-lastBuildTime >= idleTimeout`).

**Why an idempotent `inFlight` *set* keyed by package** instead of the fork's
`activeBuilds int` counter: the new model uses bounded reconcile workers + requeue, so
the same package can re-enter `build()` several times before a pod frees up. A set makes
`StartBuild` idempotent, so requeues never double-count demand. (The old fork ran one
unbounded goroutine per build, so a plain counter was safe there — it is not here.)

## 4. Reconciler changes

**EnvironmentReconciler**
- `createBuilderDeployment`: keep `replicas` starting at 1 (the reaper/scaler move it).
- At end of a successful `Reconcile`: `poolMgr.Ensure(env, ns)`.
- NotFound branch: `poolMgr.Forget(req UID)` — but `req` has no UID; resolve by
  name/ns match in the manager (small helper `ForgetByName`).

**PackageReconciler.build()** — restructured (keeps every upstream security guard):
```
guard → getEnv → builderNs
demand := poolMgr.StartBuild(env, pkg, builderNs)
if err := scaleBuilderUp(builderNs, name, clamp(demand, 1, poolSize(env))); err != nil {
    poolMgr.FinishBuild(...); return failBuild(...)
}
readyIPs := listReadyBuilderPodIPs(env, builderNs)        // raw clientset, like builderPodReady
podIP, ok := poolMgr.ClaimFreeBuilderPod(env.UID, readyIPs)
if !ok {                                                  // no free ready pod yet
    return ctrl.Result{RequeueAfter: 5s}, nil             // stays in-flight = still demanding
}
defer poolMgr.ReleaseBuilderPod(env.UID, podIP)
defer poolMgr.FinishBuild(env.UID, pkg)                   // terminal from here
set Running → buildPackage(..., podIP, ...) → update fns → Succeeded/Failed
```
- `scaleBuilderUp` only ever raises replicas (GetScale/UpdateScale), capped at
  `poolSize`; the reaper is the only thing that lowers them, so a building pod is
  never scaled out from under a build.

**common.go**
- `buildPackage(..., builderPodIP string, ...)`: route to `http://<podIP>:8000/8001`.
  If `builderPodIP == ""`, fall back to the existing service-DNS string (defensive;
  the package reconciler always passes a claimed IP).

**Reaper** — a `manager.Runnable` with `NeedLeaderElection()==true` (same pattern as
`readinessRunnable`), ticking every `BUILDER_IDLE_REAPER_INTERVAL` (→ `OBJECT_REAPER_INTERVAL`
→ default). Each tick: for every `ReapTargets`, re-check not-building, then `UpdateScale → 0`.

## 5. Wiring in `Start()` (`buildermgr.go`)
```go
poolMgr := newBuilderPoolManager(bmLogger)
envReconciler := makeEnvironmentReconciler(..., poolMgr)
pkgReconciler := makePackageReconciler(..., poolMgr)
mgr.Add(newIdleReaper(bmLogger, kubernetesClient, poolMgr, reaperInterval)) // leader-only
```

## 6. Open decisions for sign-off

- **D1 — build-wait strategy.** Recommended: **requeue-based** (non-blocking, above) so
  it composes with the 5 bounded workers. Alternative: fork-faithful **blocking** wait
  inside `build()` — simpler/closer to the fork, but blocks a worker during pod
  cold-start and can starve the 5-worker pool when many envs build at once. *I recommend
  requeue-based.*
- **D2 — pod-IP for `PoolSize==1`.** Recommended: **uniform pod-IP** (claim the single
  pod per build) — simplest, and serialisation at poolsize 1 matches Phase 2's default
  `MAX_PARALLEL_BUILDS=1`. Alternative: keep service-DNS when `PoolSize==1` to minimise
  behaviour change for the common case. *I recommend uniform pod-IP.*
- **D3 — "infinite build" fix.** Upstream re-drives `Running` deliberately
  (`package_reconciler.go:89`, idempotent) and the predicate only enqueues on the
  `→pending` transition, so the old loop can't recur. Plan: **don't port the skip**, add
  a regression test instead. (Confirm.)

## 7. What I'll write once signed off
1. `pkg/buildermgr/builderpool.go` (+ unit test) — the manager, pure/in-memory, fully testable.
2. Reaper Runnable (+ test).
3. EnvironmentReconciler: `Ensure`/`Forget` hooks (deployment stays replicas=1).
4. PackageReconciler.build(): scale-up + claim + requeue; `common.go` pod-IP param.
5. Wire in `Start()`; port `GetBuilderIdleReaperInterval` (as `logr`).
6. `go build ./... && go test ./pkg/buildermgr/...`; commit as Phases 3–4.

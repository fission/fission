# RFC-0004: Reconciler Consolidation & Dependency-Driven Watches

- **Status:** Implemented ([#3457](https://github.com/fission/fission/pull/3457)–[#3461](https://github.com/fission/fission/pull/3461), all merged 2026-06-03) — see "As shipped" for deviations
- **Supersedes:** none
- **Related:** RFC-0002 (EndpointSlice-native data plane), RFC-0003 (CRD modernization)

## Summary

Collapse the executor process's nine controller-runtime reconcilers into three by making a single Function-centric reconciler that uses `.For(Function)` + `.Watches(...)` to react to its dependencies (Environment, ConfigMap, Secret, and the Deployment/Service/HPA it manages), instead of one bare `.For()` reconciler per concern.
The same change retires a redundant standalone informer factory in the executor and adds opt-in finalizers for reliable cross-namespace teardown — with no change to the 9-pod Helm topology and no CRD/API/Helm-value changes.

## Motivation

Fission's control plane was fully migrated to controller-runtime reconcilers (completed 2026-06-01).
That migration was a faithful 1:1 port: every former informer + event-handler became its own reconciler wired with the shared helper in `pkg/controller/builder.go`, which only ever calls `.For(obj).WithPredicates(...).Complete(r)`.
There is, today, **no `.Owns()`, no `.Watches()`, no `OwnerReferences`/`SetControllerReference`, and no finalizer** anywhere in non-generated code.

The port works, but it left structural duplication that the controller-runtime model is designed to remove.

### Current topology

Nine manager processes run ~19 reconcilers (each Helm pod runs `fission-bundle` with one subsystem flag — see `cmd/fission-bundle/main.go`):

| Process | Reconcilers (`.For` root) | Leader-elected |
|---|---|---|
| router | `httpTriggerReconciler` (HTTPTrigger), `functionReconciler` (Function) | no |
| **executor** | **9** — see below | opt-in |
| kubewatcher | KubernetesWatchTrigger | opt-in |
| timer | TimeTrigger | opt-in |
| buildermgr | Environment, Package | opt-in |
| mqtrigger | MessageQueueTrigger | opt-in |
| mqt_keda | MessageQueueTrigger → KEDA ScaledObject/TriggerAuth/Deployment | opt-in |
| canaryconfig | CanaryConfig | opt-in |
| logger | Pod (DaemonSet) | no |

The duplication is concentrated in the **executor** process, which registers nine reconcilers (`pkg/executor/executor.go:512-574`):

- **Three `functionReconciler`s**, one per executor type, all rooted on `fv1.Function` — `pkg/executor/executortype/{poolmgr,newdeploy,container}/reconciler.go`.
- **Two `environmentReconciler`s** (poolmgr pool sync + newdeploy runtime-image propagation), both rooted on `fv1.Environment`.
- **Two cms reconcilers** (`ConfigMap`, `Secret`) whose entire job is to call `RefreshFuncPods` on functions that mount the changed object — `pkg/executor/cms/reconciler.go`.
- **Two poolmgr lifecycle reconcilers** rooted on `ReplicaSet` and `Pod` (warm-pool internals).

controller-runtime shares **one** informer per GVK across all reconcilers in a manager, so the three Function reconcilers do **not** triplicate the Function cache.
What they do triplicate is everything downstream of the informer: every Function event is fanned into **three workqueues**, evaluated by **three predicates**, and processed by **three reconcile goroutine pools**, each keeping its own `lastReconciled`/`lastSeen` `sync.Map`.
Two of the three backends early-return for functions of the wrong executor type (`Spec.InvokeStrategy.ExecutionStrategy.ExecutorType`, `pkg/apis/core/v1/types.go:374`), so two-thirds of that processing is pure overhead.
The two Environment reconcilers duplicate the same way.

On top of that, the executor runs a **second, redundant caching layer**: newdeploy and container still build standalone client-go `SharedInformerFactory` instances for their Deployment/Service listers (`pkg/utils/informer.go:GetInformerFactoryByExecutor`, started by the `startFactories` runnable in `pkg/executor/executor.go:547`) — running alongside the manager cache that already holds the same objects.

### Why this matters

- **Compute:** redundant event fan-out, predicate evaluation, and reconcile goroutines scale with Function/Environment churn for no benefit.
- **Memory:** the standalone Deployment/Service informer factory is a whole second informer infrastructure in the same process.
- **Correctness gap:** with no `.Watches()` on managed Deployment/Service/HPA, drift (someone deletes a function's Deployment) is silent until the next spec change or until the periodic reaper happens to run — the executor does not self-heal.
- **Cleanup fragility:** deletion teardown rides the NotFound path plus the in-memory `lastReconciled` cache plus a label reaper; there is no guarantee a function's cross-namespace workload is torn down if the executor missed the delete event.

## Goals

- Reduce the executor from nine reconcilers to three with no behavior regression.
- Make the executor react to its real dependency graph via `.Watches()` rather than separate reconcilers.
- Add self-healing for executor-managed Deployment/Service/HPA.
- Remove the redundant standalone Deployment/Service informer factory.
- Add reliable, opt-in cross-namespace teardown via finalizers, with a force-remove escape hatch.

## Non-goals

- **No cross-process merging.**
  The single-reconciler pods (timer, kubewatcher, canaryconfig) stay separate processes; merging them changes Helm topology, leader-election leases, RBAC, and failure domains, and is explicitly out of scope here.
- **No `.Owns()`-based garbage collection.**
  See the namespace constraint below — owner-reference GC cannot span namespaces, so it is the wrong tool for Fission's workload placement.
- **No CRD schema, CLI, or Helm-value changes.**
  This RFC is internal control-plane structure only.

## The decisive constraint: workloads can live in a different namespace than the CR

`NamespaceResolver.GetFunctionNS` (`pkg/utils/namespace.go:149`) maps a Function CR in the `default` namespace to a configured `FunctionNamespace` (e.g. `fission-function`) when placing its Deployment/Service/HPA.
So a Function's managed workload can sit in a **different namespace** than the Function object itself.

Kubernetes owner references — and therefore controller-runtime `.Owns()` and built-in GC — **cannot cross namespaces** (a namespaced object may only be owned by something in its own namespace).
This rules out `.Owns()` for Function→Deployment in the general case.
The correct tools are:

- **`.Watches()` with a label→owner mapping** for drift/self-healing (works regardless of namespace), and
- **finalizers** for guaranteed teardown (works regardless of namespace).

## Design

### A. Collapse the three Function reconcilers into one (dependency-driven)

Replace the three per-type `functionReconciler`s with a single executor-level reconciler:

```go
builder.ControllerManagedBy(mgr).
    For(&fv1.Function{}).                                          // one workqueue, one predicate
    Watches(&fv1.Environment{}, envToFunctions).                  // re-roll funcs on runtime-image change
    Watches(&corev1.ConfigMap{}, cmToFunctions, contentChanged).  // recycle pods on referenced CM change
    Watches(&corev1.Secret{},    secretToFunctions, contentChanged).
    Watches(&appsv1.Deployment{}, deployToFunction, byFnLabel).   // self-heal drift/deletion
    Watches(&corev1.Service{},    svcToFunction,    byFnLabel).
    Watches(&autoscalingv2.HorizontalPodAutoscaler{}, hpaToFunction, byFnLabel).
    Complete(r)
```

- The reconcile body reads `f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType` (default `poolmgr`) and **dispatches** to the existing `executortype.ExecutorType` backend — the per-type create/update/delete logic stays exactly where it is; only the *routing* moves up one level.
- **Executor-type transitions get simpler and more correct.**
  One reconciler holds both the previous type (from `lastReconciled`) and the new type, so it can tear down the old backend's objects and build the new in a single reconcile.
  Today each of the three reconcilers must independently detect "this function stopped being mine" and clean up.
- controller-runtime serializes reconciles per `NamespacedName`, so the single `lastReconciled` `sync.Map` is as safe as the three it replaces.

This requires the helper in `pkg/controller/builder.go` to grow a variant that accepts `.Watches()` sources (the current `Register*` helpers only take `.For()` + predicates).

### B. Fold the Environment and cms reconcilers into the Function reconciler

The new `.Watches()` edges absorb four of the nine reconcilers:

- `Watches(&fv1.Environment{}, envToFunctions)` replaces **both** `environmentReconciler`s.
  `envToFunctions` lists the functions using the changed Environment and enqueues them; the reconcile path then does what the newdeploy env reconciler did (re-roll the Deployment if the runtime image changed).
  The poolmgr pool's periodic re-sync stays as a `RequeueAfter` on the Function/pool path.
- `Watches(&corev1.ConfigMap{}, ...)` and `Watches(&corev1.Secret{}, ...)` with the existing `contentChanged` predicate replace **both** cms reconcilers.
  The mapping lists functions mounting the object and enqueues them; the reconcile path calls the existing `refreshPods`/`RefreshFuncPods` (`pkg/executor/cms/reconciler.go:132`).

### C. `.Watches()` on managed objects for self-healing

`deployToFunction`/`svcToFunction`/`hpaToFunction` map a managed object back to its owning Function using the **existing** deploy labels (`getDeployLabels`, `pkg/executor/executortype/newdeploy/newdeploymgr.go:814` and `.../container/containermgr.go:657`).
This works cross-namespace where `.Owns()` cannot, and gives drift correction the executor lacks today: deleting a function's Deployment re-enqueues the Function, which re-creates it.
With this in place the periodic object reaper becomes a backstop, not the primary repair path.

> Implementation note: the newdeploy and container `getDeployLabels` signatures differ (newdeploy also takes the env meta).
> The shared mapping must key on the stable function-identifying labels (function name/uid) common to both, not the full label set.

### D. Retire the standalone Deployment/Service informer factory

Once Deployment/Service are watched by the manager (B/C above) and a label-scoped `ByObject` filter for them is added to `executorCacheOptions` (mirroring the existing poolmgr Pod filter), the manager cache becomes the single source for those reads.
Then delete `GetInformerFactoryByExecutor`, the `ndmInformerFactory`/`cnmInformerFactory` maps, the `deplLister*`/`svcLister*` fields, and the `startFactories` runnable, routing those reads through `mgr.GetClient()`.
This removes the second informer infrastructure from the executor — the clearest memory line-item in this RFC.

### E. Finalizers for reliable cross-namespace teardown (opt-in, phased)

Because owner-ref GC cannot reach into `FunctionNamespace`, add a Fission finalizer (`fission.io/function-cleanup`) on Function (and, if needed, Environment):

- added on first reconcile;
- on delete (DeletionTimestamp set), the reconcile tears down the cross-namespace Deployment/Service/HPA, then removes the finalizer;
- gated behind an env flag (default off) for the first release so the behavior can be validated before becoming default;
- documented escape hatch for the executor-down case so deletes never wedge: `kubectl patch function <name> -n <ns> --type=merge -p '{"metadata":{"finalizers":[]}}'`.

This replaces the fragile "NotFound + `lastReconciled` cache + reaper" cleanup with a guaranteed teardown that does not depend on the executor having observed the delete event live.

### What this does and does not save (stated honestly)

- It does **not** shrink the Function/Environment informer/cache memory — controller-runtime already shares one informer per GVK per manager.
- It **does** remove: two of three Function workqueues + goroutine pools, one of two Environment ones, two cms reconcilers, two duplicate state maps, and (item D) the standalone Deployment/Service informer factory.
- Net: executor reconcilers **9 → 3** (unified `functionReconciler` + poolmgr `replicaSetReconciler` + poolmgr `readyPodReconciler`), Function-event processing **3× → 1×**, standalone informer factories **2 → 0**.

## Alternatives considered

- **`.Owns()` + owner references for GC.**
  Rejected: owner references cannot cross namespaces, and Fission routinely places workloads in `FunctionNamespace`, distinct from the CR's namespace.
  `.Owns()` would silently fail to GC exactly the common case.
- **Keep three reconcilers, just share predicates/state.**
  Marginal; leaves the triple fan-out, the duplicate state maps, and the redundant factory in place, and still no self-healing.
- **Merge the small single-reconciler pods (timer/kubewatcher/canaryconfig) into one process.**
  Bigger baseline memory win (fewer Go runtimes + manager caches), but changes Helm topology, leases, RBAC, and failure isolation.
  Deferred to a possible future RFC; out of scope here by decision.

## Backward compatibility

- No CRD schema, CLI, or Helm-value changes.
- The unified reconciler preserves each backend's existing create/update/delete semantics; `AdoptExistingResources` continues to single-flight with the reconciler's initial sync.
- Finalizers ship **opt-in (default off)**; enabling them is a deliberate operator action, and the force-remove path is documented.
- Cluster floor unchanged (Kubernetes 1.32, per `rfc/README.md`); `.Watches()`, label-mapped enqueue, and finalizers are all long-GA.

## Rollout phases

Incremental, one reviewable PR per phase, each referencing this RFC:

1. Extend `pkg/controller/builder.go` with a `.Watches()`-capable registration variant (no behavior change).
2. Merge the two Environment reconcilers into one (lowest risk; isolated).
3. Merge the three Function reconcilers into one dispatch-by-type reconciler, preserving each backend's create/update/delete + transition logic.
4. Fold ConfigMap/Secret recycling into `.Watches()` on the unified Function reconciler; delete the cms reconcilers.
5. Add `.Watches()` for Deployment/Service/HPA drift correction.
6. Retire the standalone `SharedInformerFactory`; route Deployment/Service reads through the manager cache + add the label-scoped `ByObject` filter.
7. Finalizers (opt-in, env-gated) + force-remove docs.

## Verification / test plan

- **Unit:** table-driven tests for each mapping function (`envToFunctions`, `cmToFunctions`, `secretToFunctions`, `deployToFunction`, …) over a fake clientset — assert the correct Function keys are enqueued and that wrong-namespace/wrong-label objects enqueue nothing.
- **Reconcile dispatch:** assert a Function with each `ExecutorType` routes to the right backend, and that an executor-type transition tears down the old backend's objects and builds the new (the integration suite already exercises transitions).
- **Self-healing:** integration test — create a function, delete its Deployment out-of-band, assert it is recreated without a spec change.
- **Finalizers:** integration test — with the flag on, delete a Function and assert its cross-namespace Deployment/Service/HPA are gone before the object disappears; assert the force-remove patch unblocks a wedged delete.
- **Regression:** the existing executor integration suites (`test/integration/suites/{common,serial}`) must stay green; the serial suite's `AdoptExistingResources` test guards the adopt/reconcile single-flight.
- **Resource delta (informational):** capture the executor's CI pprof goroutine/heap profiles before and after phase 6 to confirm the factory removal and reduced goroutine count.

## Open questions

- Should finalizers also cover Environment (its builder Deployment/Service in `buildermgr`), or stay Function-only in the first cut?
- Does the poolmgr pool's periodic re-sync belong on the unified Function reconciler's `RequeueAfter`, or stay coupled to the Environment path it lives on today?
- Is a single label-scoped `ByObject` filter sufficient for Deployment/Service across all three executor types, given their differing label sets?

## As shipped

The rollout deviated from the plan above in a few ways:

- **Phase 4 (fold cms into `.Watches()`) was skipped.**
  The cms reconcilers call `RefreshFuncPods` directly; folding them into the Function reconciler would need referenced-resource RV-sum change detection (a level-triggered redesign) to remove two thin reconcilers — disproportionate.
  So the executor settled at **6 reconcilers** (unified Function, unified Environment, cms ConfigMap, cms Secret, poolmgr ReplicaSet, poolmgr readyPod), not the ~3 the design sketched.
- **Finalizers became a chart-wide default-on toggle**, not env-gated/opt-in: a top-level `finalizerEnabled` value (default `true`, mirroring `disableOwnerReference`) that other controllers can adopt later, rather than an executor-only knob.
- **Drift self-healing scope:** Deployment + Service only (HPA is not in the Manager cache); a **delete-only** watch predicate (the idle reaper *scales*, never deletes, so reacting to updates would fight it); recreate via the idempotent get-or-create path.
- **Concurrency:** merging the three per-type Function reconcilers (each formerly its own 1-worker controller) into one collapsed cross-type isolation, so a `createFunction` blocked in `waitForDeploy` head-of-line-blocked unrelated functions.
  Fixed by `MaxConcurrentReconciles=10` on the shared reconciler.

Shipped across PRs #3457 (Environment merge), #3458 (Function merge), #3459 (retire the standalone informer factory), #3460 (finalizers), #3461 (drift self-healing + the concurrency fix).

## Profiling results (measured)

CI captures executor heap + goroutine pprof per integration leg (kind-ci profile).
All figures are k8s v1.34.3.

### Cumulative: before RFC-0004 vs after (full consolidation + drift)

Before = `4395fbef` (pre-#3457; the nearest pre-RFC main commit whose scrape captured the executor — `2e45f84d` and `1c7fc17f` only captured the router).
After = #3461 (run 26880909337).

| Executor goroutines (by frame)            | Before | After | Δ    |
|-------------------------------------------|-------:|------:|-----:|
| controller-runtime controller machinery   |     27 |    18 |  −9  |
| reconcile workers (`processNextWorkItem`)  |      9 |     6 |  −3  |
| workqueue goroutines                       |     34 |     7 | −27  |
| informer reflectors                        |     24 |    20 |  −4  |
| informer sharedProcessor / listeners       |  12/22 | 10/20 | −2/−2|
| total                                      |    233 |   225 |  −8  |

Heap (inuse): 4.67 MB → 6.08 MB — within run-to-run scrape noise, not a regression.
The consolidation frees goroutines/workqueues, not cache memory: controller-runtime shares one informer per GVK either way, so heap stays flat (an earlier Phase-3 sample swung 6.4 → 4.4 MB the other way).
Router (control, untouched by RFC-0004): 74 → 72 goroutines.

Caveat: that "after" snapshot predates the `MaxConcurrentReconciles=10` fix, so the Function reconciler still had one worker.
The merged final adds ~9 idle reconcile-worker goroutines (parked on `queue.Get`, cheap) as a deliberate trade for non-starvation, so the final total is roughly flat vs the pre-RFC baseline but with far less workqueue/controller machinery.

### Phase 3 isolated: 8 → 6 reconcilers (Function merge)

Main `e494f61c` (Phase 2 merged, 8 reconcilers) vs #3458 (6 reconcilers): reconcile workers 8 → 6, controller machinery 24 → 18, per-controller event-source goroutines 8 → 6; heap flat; client-go informer frames byte-identical (informers shared per GVK).

The durable signal across both comparisons is **composition** (fewer controllers, workqueues, informers), not raw totals — which vary run to run with cluster activity.

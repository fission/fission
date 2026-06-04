# Spike: porting watch-all-namespaces onto controller-runtime

Status: **design proposal — awaiting sign-off before code.**

## The key finding (why this is small on the Go side)

The new controller-runtime Manager cache **already supports watch-all** with no
crmanager change. `crmanager.FissionCacheOptions()` builds
`cache.Options{DefaultNamespaces: ...}` from `NamespaceResolver.FissionResourceNS`:

```go
for _, ns := range utils.DefaultNSResolver().FissionResourceNS {
    nsConfig[ns] = cache.Config{}
}
return cache.Options{DefaultNamespaces: nsConfig}
```

controller-runtime **v0.24.1** treats the `""` key (`cache.AllNamespaces ==
metav1.NamespaceAll`) as "cache all namespaces" (pkg/cache/cache.go:138-203,
verified). So if `FissionResourceNS == {"": ""}`, all four managers that call
`FissionCacheOptions()` — **buildermgr, executor, router, mqtrigger** — go
cluster-wide automatically.

So the Go enabler is just `GetNamespaces()` returning `{"": ""}` when the flag
is set — exactly what the fork did, and it composes cleanly with the reconcilers
already ported (their cache reads simply see every namespace).

## Go changes (small)

1. **pkg/utils/namespace.go** — add `ENV_WATCH_ALL_NAMESPACES =
   "FISSION_WATCH_ALL_NAMESPACES"`; in `GetNamespaces()`, when it is `"true"`,
   return `{metav1.NamespaceAll: metav1.NamespaceAll}`. *This is the enabler.*
   Port the fork's `namespace_test.go` case.

2. **pkg/utils/serviceaccount.go** — port `EnsureFetcherSA` / `EnsureBuilderSA`
   (logr, not zap) and make `runSACheck` skip `""`/empty namespaces (so the
   startup `CreateMissingPermissionForSA` doesn't try to set up SAs for the
   AllNamespaces sentinel).

3. **On-demand SA creation** for dynamically-discovered namespaces — with
   watch-all, functions/builders can land in any namespace, and their pods need
   the fetcher/builder SA present there (the static startup pass can't pre-create
   in unknown namespaces). Wire (idempotent; harmless when not watch-all):
   - poolmgr `GenericPool.setup` → `EnsureFetcherSA(gp.fnNamespace)`
   - newdeploy `fnCreate` → `EnsureFetcherSA(function namespace)`
   - buildermgr `EnvironmentReconciler.ensureBuilder` → `EnsureBuilderSA(builder namespace)`

   (This is the real reason the fork's `EnsureFetcherSA` existed — see the Phase 5
   scope note. It belongs here, not in Phase 5.)

## Helm changes (the bulk — a cluster-wide cache REQUIRES cluster-scoped RBAC)

A cluster-wide cache list/watch is forbidden under per-namespace Roles (the
buildermgr.go comment spells this out), so RBAC must become cluster-scoped when
watch-all is on. The fork already worked out the template pattern:

4. **values.yaml** — `watchAllNamespaces: false` (default OFF / opt-in).
5. **_helpers.tpl** — when `watchAllNamespaces`, set `FISSION_RESOURCE_NAMESPACES=""`
   and `FISSION_WATCH_ALL_NAMESPACES=true` on every component (the shared env block).
6. **_fission-role-generator.tpl** + **_fission-kuberntes-role-generator.tpl** —
   emit `ClusterRole`/`ClusterRoleBinding` (a single one, gated on the default
   namespace so it isn't duplicated per namespace) when `watchAllNamespaces=true`,
   else the per-namespace `Role`/`RoleBinding` exactly as today.
7. **_fission-component-roles.tpl** / webhook templates — port the fork's
   additions as needed; re-verify against upstream's current webhook templates.

Consistency invariant: the same `watchAllNamespaces` value drives **both** the
cache scope (via the env var) and the RBAC scope (via ClusterRoles). They must
agree or the manager's cache sync is forbidden and it exits.

## Decisions for sign-off

- **D1 — default.** `watchAllNamespaces: false` (opt-in). Preserves upstream
  behavior; nobody gets cluster-wide RBAC unless they ask. *Recommended.*
- **D2 — on-demand SA.** Always call `EnsureFetcherSA`/`EnsureBuilderSA` at
  pod/builder creation (idempotent, cheap, harmless when not watch-all), rather
  than gating the calls behind the flag. Simpler and also self-heals a missing SA
  in the static-namespace case. *Recommended.*
- **D3 — Helm RBAC scope.** Port the fork's ClusterRole-generator templating
  (full opt-in toggle). Alternative: ship a separate static ClusterRole only when
  watch-all — but that diverges from the fork and the per-component role structure.
  *Recommend the fork's templating approach.*

## Verification plan
- `go build ./... && go test ./pkg/utils/...` (namespace + SA).
- `helm template --set watchAllNamespaces=true` → assert ClusterRole/ClusterRoleBinding
  rendered and `FISSION_WATCH_ALL_NAMESPACES=true` on component env; default render
  unchanged (Roles, per-namespace).
- Unit test: `GetNamespaces` returns `{"":""}` under the flag; `FissionCacheOptions`
  then yields `DefaultNamespaces[""]` (cluster-wide).

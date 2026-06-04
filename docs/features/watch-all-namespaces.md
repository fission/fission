# Watch all namespaces

Watch Fission custom resources (functions, environments, packages, triggers, …)
across **every** namespace in the cluster, instead of only `defaultNamespace` plus
an enumerated `additionalFissionNamespaces` list. Lets you create functions in any
namespace — including namespaces created after install — without re-upgrading the
chart to add them.

## How it works (controller-runtime)

Every Fission control-plane component runs a controller-runtime Manager whose
shared cache is scoped by `crmanager.FissionCacheOptions()`, which maps
`NamespaceResolver.FissionResourceNS` into `cache.Options.DefaultNamespaces`.
controller-runtime treats the `""` (`cache.AllNamespaces`) key as **"cache every
namespace."**

So when `FISSION_WATCH_ALL_NAMESPACES=true`, `GetNamespaces()`
(`pkg/utils/namespace.go`) returns the single `""` sentinel, and all four managers
— buildermgr, executor, router, mqtrigger — go cluster-wide. No per-component
informer wiring is needed; the cache configuration does it.

### Service accounts in dynamic namespaces

With a static namespace list the chart pre-creates the `fission-fetcher` /
`fission-builder` ServiceAccounts (and their roles) per namespace. Under watch-all
the namespaces aren't known at install time, so the SAs are ensured **on demand**:

- the executor calls `EnsureFetcherSA` when it sets up a pool (poolmgr) or creates
  a function Deployment (newdeploy);
- the buildermgr calls `EnsureBuilderSA` when it ensures an environment's builder.

These are idempotent (`pkg/utils/serviceaccount.go`) and harmless when the SA
already exists, so they also self-heal a missing SA in the static case.

## RBAC (important)

A cluster-wide cache's list/watch is **forbidden under per-namespace Roles**, so
watch-all requires cluster-scoped RBAC. When `watchAllNamespaces=true` the chart
emits `ClusterRole`/`ClusterRoleBinding` (one per component, gated to the default
namespace) instead of per-namespace `Role`/`RoleBinding`, and grants the
buildermgr/executor permission to create ServiceAccounts, Roles and RoleBindings
(for the on-demand SA provisioning above). The `watchAllNamespaces` value drives
**both** the cache scope (via `FISSION_WATCH_ALL_NAMESPACES`) and the RBAC scope —
they must agree or a manager's cache sync is forbidden and it exits.

## Configuration

| Setting | Default | Meaning |
|---|---|---|
| `watchAllNamespaces` (Helm value) | `true` | Watch all namespaces (cluster-wide cache + ClusterRoles) |
| `FISSION_WATCH_ALL_NAMESPACES` (component env) | set by the chart | Runtime toggle read by `GetNamespaces()` |

```bash
# opt out (per-namespace, enumerate via additionalFissionNamespaces)
helm upgrade --install fission … --set watchAllNamespaces=false
```

## Key files

- `pkg/utils/namespace.go` — `GetNamespaces()` watch-all branch (the enabler)
- `pkg/utils/crmanager/crmanager.go` — `FissionCacheOptions()` (unchanged; consumes `""` as cluster-wide)
- `pkg/utils/serviceaccount.go` — `EnsureFetcherSA` / `EnsureBuilderSA`, `runSACheck` skip-empty
- `charts/fission-all/templates/_fission-role-generator.tpl`, `_fission-kuberntes-role-generator.tpl` — ClusterRole toggle
- `charts/fission-all/templates/_fission-component-roles.tpl` — SA-create RBAC
- `charts/fission-all/templates/_helpers.tpl`, `values.yaml` — env + value

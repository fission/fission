# Cross-namespace invocation isolation (router-internal)

The router's internal listener (`router-internal:8889`) serves
`/fission-function/<ns>/<name>` — the path in-cluster callers use to invoke
private (un-triggered) functions, including ones that scale to zero (the router
cold-starts them on demand). By default that endpoint will invoke a function in
**any** namespace, and with `internalAuth` disabled it is unauthenticated. On a
multi-tenant cluster that means a function pod in one tenant namespace can reach a
private function in another namespace.

`router.enforceSameNamespaceInvocation` closes that gap.

## What it does

When enabled, the internal listener rejects (`403`) any
`/fission-function/<ns>/<name>` request unless **either**:

- the caller's pod is in the **same namespace** as the target function, or
- the caller is an **internal Fission component** — a pod in the Fission install
  namespace (executor, kubewatcher, timer, mqtrigger, …), which legitimately
  invoke functions across namespaces.

This fits the common patterns exactly: a GraphQL federation gateway invoking its
own-namespace subgraphs passes; a KEDA connector (deployed in the function's
namespace) invoking that function passes; internal triggers pass; a function in
namespace A invoking a private function in namespace B is blocked.

## How it decides the caller

The router attributes the request's source pod IP (the TCP peer — `X-Forwarded-For`
is ignored, since it's caller-controlled) to a namespace via a cluster-wide pod
cache (a pod informer keeping `podIP → namespace`), falling back to a direct API
lookup (`status.podIP`) on a cache miss so a freshly-created caller isn't rejected
while the watch catches up. The check is applied **per-function handler**, so the
target namespace is taken from the function object, not parsed from the (ambiguous)
path. A caller IP that cannot be resolved to a pod is **rejected** (fail closed).

> Cold-start window: a brand-new pod can issue a request in the instant before its
> `status.podIP` is observable to the API/informer. Such a first request fails
> closed (403) and succeeds on the caller's normal retry. Long-running callers
> (the federation gateway, ordinary functions) are unaffected — they resolve
> immediately after their first second. The federation→subgraph cold-start path is
> safe regardless: the *caller* is the long-running federation, not the
> scaled-from-zero subgraph.

## Enabling

```bash
helm upgrade fission ... --set router.enforceSameNamespaceInvocation=true
```

This:
- sets `ROUTER_ENFORCE_SAME_NAMESPACE_INVOCATION=true` on the router;
- creates a cluster-scoped ClusterRole granting the router `pods` get/list/watch
  (needed for the IP→namespace cache — a caller can be in any namespace).

Intended for multi-tenant clusters, typically alongside `watchAllNamespaces`.
Default **off** (upstream behavior). It works with `internalAuth` either on or off
— it is an independent, identity-based authorization layer on top of (or instead
of) the HMAC/NetworkPolicy "who can reach the port" controls.

## Relationship to the other controls

| Control | Restricts |
|---|---|
| `internalAuth` (HMAC) | *who* may call router-internal (signed callers only) |
| `networkPolicy` (router-internal ingress) | *which pods* may reach port 8889 |
| **`enforceSameNamespaceInvocation`** | *which namespace's functions* a caller may invoke |

The first two gate reachability; this one gates the target. Use it when callers
that *can* reach the endpoint (e.g. a federation gateway) must still be confined
to their own namespace's functions.

## Key files

- `pkg/router/same_namespace_guard.go` — the guard + pod-IP cache
- `pkg/router/httpTriggers.go` — per-function handler wrapping in `buildMuxes`
- `pkg/router/router.go` — wiring (`ROUTER_ENFORCE_SAME_NAMESPACE_INVOCATION`)
- `charts/fission-all/templates/router/same-namespace-guard-rbac.yaml` — pods ClusterRole

# NetworkPolicy debugging

When a Fission pod can't reach another Fission service and the symptom is `dial tcp <ip>:<port>: i/o timeout`, the cause is almost always one of these three.

## Quickly check whether NetworkPolicy is the suspect

```bash
kubectl get networkpolicies -n fission        # which policies are active
kubectl describe networkpolicy <name> -n fission
```

If `networkPolicy.enabled=false` in the install (or the policy isn't there at all), this section doesn't apply — debug as a regular Service / DNS / firewall issue.

## Pod labels — the source of truth

A `NetworkPolicy` `from: [{ podSelector: ... }]` rule matches pod labels, **not** Service / Deployment names. The Fission control plane uses different label conventions for *controllers* vs the *worker pods* they create:

| Pod kind | Labels |
|---|---|
| `buildermgr` controller (Deployment) | `svc=buildermgr` |
| `executor` controller (Deployment) | `svc=executor` |
| `router` controller (Deployment) | `svc=router, application=fission-router` |
| `storagesvc` (Deployment) | `svc=storagesvc, application=fission-storage` |
| **Per-env builder pods** (env builder + fetcher sidecar; created by buildermgr per Environment CR) | `owner=buildermgr, envName=<env>, envNamespace=<ns>, envResourceVersion=<rv>` |
| **Function pods** (created by executor per Function CR) | `executorType=poolmgr|newdeploy|container, functionName=<name>, executorInstanceId=<id>, managed=<true|false>` |

When writing a NetworkPolicy ingress rule that targets the *fetcher sidecar in worker pods*, you need to select on `owner=buildermgr` (env-builder pods) and `executorType in [...]` (function pods) — **not** on `svc=*` (those are the controllers, which don't actually talk to storagesvc directly).

Constants: `pkg/apis/core/v1/const.go` (function-pod labels), `pkg/buildermgr/envwatcher.go` `getLabels()` (env-builder pod labels).

## Cross-namespace selectors are mandatory

Storagesvc lives in the Fission install namespace (`fission`). Env-builder pods and function pods live in user namespaces (`default` for the test framework, plus any namespace the operator deploys functions to).

A `NetworkPolicy` in namespace `fission` with this rule:
```yaml
- from:
    - podSelector:
        matchLabels:
          owner: buildermgr
```
matches **only pods in namespace `fission`**. Pods in other namespaces are dropped.

To match labelled pods in any namespace, pair `podSelector` with an empty `namespaceSelector`:
```yaml
- from:
    - namespaceSelector: {}
      podSelector:
        matchLabels:
          owner: buildermgr
```

Symptom of missing this: `dial tcp <storagesvc-ip>:80: i/o timeout` from the fetcher sidecar, even though the pod's labels look correct.

## Reference: storagesvc NetworkPolicy template

`charts/fission-all/templates/networkpolicy.yaml` is the canonical example. It demonstrates:
- `podSelector` on the *target* pod (storagesvc itself).
- Three `ingress` rules: env-builder pods (port 8000), function pods (port 8000), Prometheus / metrics scraping (port 8080, no selector — allow from anywhere).
- Each `from` peer pairs `namespaceSelector: {}` with the right `podSelector`.

## Validation workflow before pushing a NetworkPolicy change

1. `helm lint charts/fission-all` — clean YAML.
2. `helm template charts/fission-all --set networkPolicy.enabled=true` — render and inspect the resource.
3. Check that every `from` peer has `namespaceSelector: {}` if the target pod and source pod live in different namespaces.
4. `kubectl get pods -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}: {.metadata.labels}{"\n"}{end}'` on a real cluster (kind-ci) to confirm real pod labels match the selectors. The CI integration test suite is a good proxy for this — failures will show up as `i/o timeout` in fetcher logs.

## Verifying enforcement actually happens

The default kindnet CNI enforces NetworkPolicy from kind v0.27 / k8s 1.30+. On older kind, or on EKS without an addon, `NetworkPolicy` resources are accepted by the API but never enforced. If a policy looks correct on disk but doesn't change behaviour, check the CNI:
```bash
kubectl get pods -n kube-system -o name | grep -E 'kindnet|cilium|calico|weave'
```

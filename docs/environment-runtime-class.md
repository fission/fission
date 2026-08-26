# Environment RuntimeClass

An Environment can opt its pods into a syscall-isolating Kubernetes `RuntimeClass` — for example gVisor's `gvisor` or Kata Containers' `kata` — instead of the node's default container runtime (typically `runc`).
This is useful for environments that run untrusted or agent-generated code and want host-level isolation, not just the container boundary.

## Usage

```sh
fission env create --name node --image ghcr.io/fission/node-env --runtime-class gvisor
```

`--runtime-class` is an alias for the canonical flag `--runtimeclass`; either spelling works on `env create` and `env update`.

On `env update`, passing an empty value clears the setting:

```sh
fission env update --name node --runtime-class ""
```

## Scope

Setting this field affects **all pods this environment creates**: the warm-pool/specialized runtime pods, and the builder pods used to build source packages for this environment.
An author opting into isolation means "this environment's workloads" — builder pods run user build commands and are arguably more arbitrary-code-shaped than the runtime pods, so they get the same isolation.

**Container-executor functions are not covered.** A function that runs as a raw container image (no Environment in scope) does not go through this field — there is no environment for it to inherit a RuntimeClass from. Ironically, container-image functions are often the ones that most want stronger isolation; that gap is tracked separately (Tier C) and is not fixed by this field.

## Cluster prerequisite

The named `RuntimeClass` object, and a node runtime handler for it (gVisor/`runsc` or Kata Containers), must already exist in the cluster.
Fission does not create or validate this at admission time — an Environment with `runtimeClassName: gvisor` is accepted even if no such `RuntimeClass` exists yet.
If the `RuntimeClass` is missing, or no node in the cluster can run it, pods stay `Pending` at scheduling time rather than failing at Environment creation.

Install a runtime handler and register the `RuntimeClass` object before relying on this field:

- gVisor (`runsc`): <https://gvisor.dev/docs/user_guide/install/>, then the [Kubernetes RuntimeClass setup](https://gvisor.dev/docs/user_guide/quick_start/kubernetes/).
- Kata Containers: <https://katacontainers.io/docs/getting-started/kata-deploy/> (the `kata-deploy` DaemonSet registers a `kata` `RuntimeClass`).

## Verifying it reached the pod

```sh
kubectl get pod -l environmentName=node -o jsonpath='{.items[0].spec.runtimeClassName}'
```

This should print the value passed at `env create`/`env update` (e.g. `gvisor`).
If it prints nothing, confirm the Environment's `spec.runtimeClassName` is actually set (`fission env get --name node -o yaml` /`kubectl get environment node -o yaml`), and that no `Runtime.PodSpec`/`Builder.PodSpec` override on the Environment sets its own `runtimeClassName` (see precedence below).

## Precedence

`spec.runtime.podSpec.runtimeClassName` (and `spec.builder.podSpec.runtimeClassName`) — the full pod-spec override escape hatch — always wins over this field when both are set.
`--runtime-class` (`spec.runtimeClassName`) only fills the pod's `runtimeClassName` when the merged pod spec would otherwise leave it unset.
This lets a Runtime/Builder `PodSpec` override stay the more specific, more powerful knob; the dedicated flag is the discoverable shortcut for the common case of "just this one field."

## Validation

`spec.runtimeClassName` must be a valid DNS-1123 label: lowercase alphanumeric characters or `-`, starting and ending with an alphanumeric character, at most 253 characters (e.g. `gvisor`, `kata`, `my-runtime-2`).
This is enforced by the Environment CRD's schema (CEL), so it is rejected at admission — by `kubectl apply`/GitOps writers as well as the `fission` CLI — not just by CLI-side validation.

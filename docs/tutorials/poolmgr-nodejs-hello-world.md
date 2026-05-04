# Hello World with the Poolmanager Executor (Node.js)

This tutorial walks through deploying a Node.js hello-world function on
Fission using the **poolmanager** (`poolmgr`) executor — the default and
most common executor in Fission, designed to keep a small pool of warm
runtime pods so that requests get a sub-100ms cold-start path.

You will go through two paths:

- **Path A — classic tarball deployment**: works on any Fission install
  going back several releases. The CLI ships your `hello.js` file to
  Fission's storage service; on each request, Fission picks a generic
  warm pod from the per-environment pool, fetches the file into a
  shared volume, and specializes the pod for your function.
- **Path B — OCI-native deployment**: works from the release that
  introduced RFC-0001 Phase 5 onward. You build a regular OCI image
  containing your function code and push it to any registry (Docker
  Hub, GHCR, ECR, a local kind registry, …). Fission creates a
  per-Function pre-specialized warm pool that mounts the image
  read-only at the userfunc path — no fetcher download, no storagesvc,
  cross-function layer cache deduplication, and the same ~100 ms warm
  request latency as the classic path.

Both paths target poolmgr; both end up with warm pods serving requests.
The OCI path additionally gives you supply-chain-friendly artifacts
(signing, scanning, retention policies) and zero cold-start fetcher
overhead.

## What you will build

A function `hello-poolmgr` that returns `Hello, World!` over HTTP. By
the end of the tutorial you will have:

- A kind cluster running Kubernetes 1.33 (required for OCI image
  volumes, beta in 1.33).
- Fission installed via Helm.
- A Node.js environment registered with Fission.
- The function deployed twice — once via the tarball path, once via
  the OCI path — both behind HTTP routes.
- An `kubectl get pods` view showing the warm pool pods that will
  serve future requests with no cold-start penalty.

## Prerequisites

Install on your workstation:

| Tool | Version | Purpose |
|---|---|---|
| `kind` | 0.26+ | Local Kubernetes |
| `kubectl` | 1.33+ | Cluster client |
| `helm` | 3.14+ | Fission install |
| `fission` | this branch's CLI | Fission control plane client (build with `make build-fission-cli`) |
| `docker` | any recent | Build the OCI image for Path B |

Verify each is on your PATH:

```bash
kind --version
kubectl version --client
helm version --short
fission --version
docker version --format '{{.Client.Version}}'
```

## Step 0 — Provision the kind cluster

Image volumes (Path B) are beta in Kubernetes 1.33. Pin the kind node
image to the matching release; older clusters work for Path A but will
silently fall back to fetcher-based specialization if you try Path B.

Save this as `kind-fission.yaml`:

```yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    image: kindest/node:v1.33.1
    extraPortMappings:
      - containerPort: 80
        hostPort: 80
      - containerPort: 443
        hostPort: 443
```

Bring it up:

```bash
kind create cluster --name fission --config kind-fission.yaml
kubectl cluster-info --context kind-fission
```

## Step 1 — Install Fission

```bash
kubectl create namespace fission

helm repo add fission-charts https://fission.github.io/fission-charts/
helm repo update

helm install fission fission-charts/fission-all \
  --namespace fission \
  --version 1.21.0 \
  --set image.repository=ghcr.io/fission \
  --wait
```

> If you are testing this branch's images locally, swap the `helm install`
> for `SKAFFOLD_PROFILE=kind make skaffold-deploy` from the repo root —
> it builds and installs the in-tree code instead of the released chart.

Wait for every pod to be `Running`:

```bash
kubectl -n fission get pods
```

## Step 2 — Port-forward the router

Open a second terminal and keep this running for the rest of the
tutorial:

```bash
kubectl -n fission port-forward svc/router 8888:80
```

The Fission router now answers on `http://127.0.0.1:8888`.

## Step 3 — Register the Node.js environment

The environment is the runtime image Fission uses to host your code.
Both Path A and Path B share this environment.

```bash
fission env create \
  --name node \
  --image ghcr.io/fission/node-env-22 \
  --executor-type poolmgr \
  --poolsize 3
```

Confirm the env is up:

```bash
fission env list
```

The poolmgr's generic pool deployment is created lazily on first use,
so you will see it appear after Path A's first request. To verify the
env directly:

```bash
kubectl -n default get env node -o yaml | head -40
```

## Step 4 — Author the function code

Save this as `hello.js`:

```js
module.exports = async function (context) {
  return {
    status: 200,
    body: "Hello, World!\n",
  };
};
```

That is your entire function — Fission's Node.js environment exports
the function via `module.exports`.

---

## Path A — Classic tarball deployment (default poolmgr)

This is what `90 %+` of Fission users do today. The CLI uploads
`hello.js` to Fission's `storagesvc`, which records it as a Package
with an `Archive.URL`. When a request arrives, the executor picks a
warm pod from the per-Environment pool, runs the fetcher sidecar to
download the file, and specializes the runtime.

### A1. Create the function and route

```bash
fission fn create \
  --name hello-poolmgr \
  --env node \
  --code hello.js

fission route create \
  --name hello-poolmgr-route \
  --method GET \
  --url /hello-poolmgr \
  --function hello-poolmgr
```

### A2. Test the function

```bash
curl http://127.0.0.1:8888/hello-poolmgr
# => Hello, World!
```

The first request triggers cold-start: pool pop + fetcher download +
specialize. Expected latency on a kind cluster: ~150–400 ms. Subsequent
requests hit the warm specialized pod and should return in 5–30 ms:

```bash
for i in 1 2 3 4 5; do
  /usr/bin/time -p curl -s http://127.0.0.1:8888/hello-poolmgr >/dev/null
done
```

### A3. Inspect the warm pool

```bash
kubectl -n fission-function get pods -l environmentName=node
```

You will see three `poolmgr-node-…` pods (matching `--poolsize 3`).
After a request to `hello-poolmgr` one of them is now bound to that
function (look for a `functionName=hello-poolmgr` label on a single
pod):

```bash
kubectl -n fission-function get pods \
  -l functionName=hello-poolmgr -o wide
```

That pod is your warm specialized pod — 100 ms latency for every
future request until it is reaped for idleness.

---

## Path B — OCI-native deployment (per-Function warm pool)

Path B replaces `storagesvc` and the fetcher's tarball download with
an OCI image volume. You build a regular container image whose only
job is to carry your function code at `/userfunc/`, push it to a
registry, and tell Fission to deploy a per-Function pool with that
image volume mounted read-only. The fetcher sidecar still runs but
only to specialize the runtime — it skips the download.

### B1. Build the OCI image

Save this as `oci/Dockerfile`:

```dockerfile
# The base layer must be a runtime image that knows how to load
# code from /userfunc. Reuse the Fission Node.js environment image
# you registered in Step 3 — that way the runtime is identical between
# Path A and Path B.
FROM ghcr.io/fission/node-env-22

# Place your code at /userfunc/. Fission's runtime expects the
# function entry point at /userfunc/<filename> when invoked with a
# functionName, or at /userfunc/user when invoked without one.
COPY hello.js /userfunc/user
```

Build and load the image into kind so the cluster's containerd can
pull it without needing a registry:

```bash
docker build -t fission-hello:v1 -f oci/Dockerfile .
kind load docker-image fission-hello:v1 --name fission
```

> If you run an external registry (GHCR, Docker Hub, a local
> distribution registry on `localhost:5000`, etc.), tag and push there
> instead. The image reference is whatever Kubelet on the kind nodes
> can resolve.

### B2. Create the OCI Package and Function

The `--oci` flag produces a `Package` whose `Spec.Deployment.OCI.Image`
points at your image — no upload to storagesvc:

```bash
fission package create \
  --name hello-pkg-oci \
  --env node \
  --oci fission-hello:v1
```

Inspect the resulting Package:

```bash
kubectl -n default get pkg hello-pkg-oci -o yaml | head -40
```

You should see:

```yaml
spec:
  deployment:
    type: oci
    oci:
      image: fission-hello:v1
status:
  buildstatus: succeeded
```

Now create the Function and HTTP route. Note that `--executor-type
poolmgr` is the default, so the explicit flag is just for clarity:

```bash
fission fn create \
  --name hello-oci \
  --env node \
  --pkg hello-pkg-oci \
  --entrypoint user \
  --executor-type poolmgr \
  --minscale 2

fission route create \
  --name hello-oci-route \
  --method GET \
  --url /hello-oci \
  --function hello-oci
```

`--minscale 2` tells poolmgr "always keep 2 warm pods for this
function". The OCI poolmgr path defaults to a single warm pod when
`MinScale` is not set; keeping it explicit makes the warm-pool intent
obvious.

### B3. Watch the per-Function pool come up

The first thing the executor does on a request to an OCI function is
materialize a per-Function `Deployment` + `Service`. Watch the pods
appear in the function namespace:

```bash
kubectl -n fission-function get deploy,svc,pods -l fission.io/poolmgr-oci=true -w
```

In another terminal, send the first request — this triggers the
Deployment's creation if it does not already exist:

```bash
curl http://127.0.0.1:8888/hello-oci
# => Hello, World!
```

Within ~2 s on a kind cluster (image is already on the node from
`kind load`) you should see two Ready pods named
`oci-hello-oci-default-<uid-suffix>-…`. Each pod has two containers:

- the `node` runtime container (image: `ghcr.io/fission/node-env-22`)
- the `fetcher` sidecar (image: `ghcr.io/fission/fetcher`) running
  with the `-skip-fetch -specialize-on-startup` flag combo

```bash
kubectl -n fission-function get pod \
  -l functionName=hello-oci -o jsonpath='{.items[0].spec.containers[*].name}'
# => node fetcher
```

### B4. Verify the image volume and skip-fetch behaviour

The defining feature of Path B is the image volume. Confirm it is
mounted read-only at `/userfunc`, with no `emptyDir` for that path:

```bash
POD=$(kubectl -n fission-function get pod \
  -l functionName=hello-oci -o name | head -1)

kubectl -n fission-function get $POD -o jsonpath='{.spec.volumes}' | jq
```

You should see (formatted for clarity):

```json
[
  {
    "name": "userfunc",
    "image": {
      "reference": "fission-hello:v1",
      "pullPolicy": "IfNotPresent"
    }
  },
  { "name": "secrets",     "emptyDir": {} },
  { "name": "configmaps",  "emptyDir": {} },
  { "name": "podinfo",     "downwardAPI": { … } }
]
```

The `userfunc` volume is sourced from your OCI image; the secrets and
configmaps volumes remain `emptyDir` because they are populated at
runtime. Fetcher's skip-fetch flag is wired so the sidecar only POSTs
the specialize request to the runtime — no archive download.

To confirm:

```bash
kubectl -n fission-function get $POD -o jsonpath='{.spec.containers[?(@.name=="fetcher")].command}' | tr ',' '\n'
```

You should see `-specialize-on-startup`, `-skip-fetch`, and
`-specialize-request <json payload>` in the command.

### B5. Measure latency

Even though the deployment is per-Function, the steady-state request
path is identical to Path A:

```bash
for i in 1 2 3 4 5; do
  /usr/bin/time -p curl -s http://127.0.0.1:8888/hello-oci >/dev/null
done
```

You should see the same ~5–30 ms warm latency. The difference is
visible only on cold start: Path A's first request includes a tarball
download from `storagesvc`; Path B's first request includes an image
volume mount, which is a kubelet operation and typically faster on a
node where the image is already cached.

---

## Update the function

### Path A — update the code in place

```bash
cat > hello.js <<'EOF'
module.exports = async function () {
  return { status: 200, body: "Hello, Fission!\n" };
};
EOF

fission fn update --name hello-poolmgr --code hello.js

curl http://127.0.0.1:8888/hello-poolmgr
# => Hello, Fission!
```

### Path B — bump the image

```bash
cat > hello.js <<'EOF'
module.exports = async function () {
  return { status: 200, body: "Hello from v2!\n" };
};
EOF

docker build -t fission-hello:v2 -f oci/Dockerfile .
kind load docker-image fission-hello:v2 --name fission

fission package update --name hello-pkg-oci --oci fission-hello:v2
```

The Package update propagates to the Function's `PackageRef.ResourceVersion`,
which the executor detects on the next request. The OCI pool path
diffs the live Deployment's image-volume reference against the Package
and rolls forward via Kubernetes' rolling-update strategy:

```bash
kubectl -n fission-function rollout status deploy \
  -l functionName=hello-oci --timeout 60s

curl http://127.0.0.1:8888/hello-oci
# => Hello from v2!
```

You can confirm the live Deployment is now on `:v2`:

```bash
kubectl -n fission-function get deploy \
  -l functionName=hello-oci -o jsonpath='{.items[0].spec.template.spec.volumes[?(@.name=="userfunc")].image.reference}'
# => fission-hello:v2
```

## Always-warm vs. scale-to-zero

| MinScale | Warm pods | Idle reaper behaviour |
|---|---|---|
| `0` (default for OCI) | 1 (a single warm pod) | After `IdleTimeout` (default ~2 min), the entire per-Function Deployment + Service is deleted. Next request triggers re-creation; image is layer-cached at the node so cold-start is ~1–3 s. |
| `> 0` (e.g. `--minscale 3`) | `MinScale` always-warm | Idle reaper **never** touches the Deployment. You always get sub-100 ms warm latency. |

For production functions where the 100 ms guarantee matters, set
`--minscale ≥ 1`. For dev/test or rarely-hit functions, leave it at
the default and pay the cold-start once after each idle window.

## Cleanup

Delete just the function:

```bash
fission fn delete --name hello-oci
```

The per-Function OCI Deployment + Service are removed automatically by
the executor's function-delete handler. Verify:

```bash
kubectl -n fission-function get deploy,svc -l functionName=hello-oci
# No resources found.
```

Tear down everything:

```bash
fission fn delete --name hello-poolmgr || true
fission fn delete --name hello-oci || true
fission package delete --name hello-pkg-oci || true
fission env delete --name node || true

helm uninstall fission --namespace fission
kind delete cluster --name fission
```

## Troubleshooting

**`curl: (52) Empty reply from server`** on the first request

Port-forward dropped or router pod is restarting. Check
`kubectl -n fission get pod -l svc=router` and re-run the
`port-forward`.

**OCI pool pods stuck in `ImagePullBackOff`**

Either kubelet cannot reach the image reference or you forgot to
`kind load docker-image`. Run:

```bash
kubectl -n fission-function describe pod <pod-name>
```

and look at the `Events` section for the precise pull error.

**Function returns `Internal Server Error` on first request**

The runtime hit a specialize error. Look at the fetcher sidecar logs:

```bash
kubectl -n fission-function logs <pod-name> -c fetcher
```

For OCI pools, fetcher's `-skip-fetch` mode requires the deployment
file to exist at `/userfunc/<filename>` inside the image. If your
image puts the code at a different path, set
`Package.Spec.Deployment.OCI.SubPath` (CLI: `--oci-subpath`) so the
mount lands where the runtime expects.

**Image volumes not supported error from kubelet**

You are on a Kubernetes version older than 1.33. Either upgrade the
node image (`kindest/node:v1.33.1` or later), or use Path A which has
no version requirement.

## What's next

- Read the RFC for the design rationale behind OCI poolmgr:
  `rfc/0001-oci-native-package-delivery.md`.
- Add HTTPS, query parameters, and headers — see
  [the official Fission docs](https://fission.io/docs/usage/function/).
- Wire a private registry: `fission package create --oci
  ghcr.io/myorg/fns/hello:v1 --oci-pull-secret ghcr-pull` after
  creating a `kubernetes.io/dockerconfigjson` secret named `ghcr-pull`
  in the `fission-function` namespace.
- Try the BuildKit-flavored builder (`fission env create
  --builder-kind buildkit --builder-registry ghcr.io/myorg/fns ...`)
  to have Fission produce the OCI image for you from source code,
  instead of building it yourself.

# Hello, World — All Three Executor Types

A practical, copy-pasteable walkthrough that deploys the same hello-world
function three ways, one per Fission executor type:

1. **`poolmgr`** — default. Generic pods are pre-warmed from an
   environment image and specialized on demand. Lowest steady-state cold
   start; pod memory shared across functions of the same environment.
2. **`newdeploy`** — one Kubernetes Deployment per function, HPA-driven,
   supports scale-to-zero. Best when you want isolation and elasticity
   per function.
3. **`container`** — bring your own pre-built container image. Fission
   runs it as-is; no source fetching, no specialization. Best when you
   already have an image or need full control of the runtime.

You will deploy the same function three times, invoke each, and inspect
what Kubernetes actually created under the hood. A fourth section
previews **OCI-native packages** (RFC-0001), a new opt-in delivery path
whose CRD and validation are already merged but whose executor
integration is still landing in phases.

Estimated time: ~15 minutes (plus ~5 minutes for the OCI preview).

---

## Prerequisites

- A Kubernetes cluster with Fission installed. The
  [Fission install guide](https://fission.io/docs/installation/) covers
  the options; a local `kind` cluster plus `helm install fission-all`
  is enough.
- `kubectl` pointed at that cluster.
- The `fission` CLI on your `$PATH` (see
  [Install CLI](https://fission.io/docs/installation/#install-the-client-cli)).
- `curl` for making HTTP calls.

Verify:

```bash
fission version
kubectl get pods -n fission
```

Every pod in the `fission` namespace should be `Running`.

---

## Step 0 — Make the router reachable

Each Fission trigger is served by the router Service. For local clusters
(or any cluster without an Ingress/LoadBalancer in front of Fission),
port-forward:

```bash
kubectl port-forward svc/router 8888:80 -n fission
```

Leave that running in one terminal; use a second terminal for the rest
of the tutorial. All `curl` examples below assume `http://localhost:8888`.

> **Tip:** `fission fn test --name <fn>` bypasses the router entirely and
> invokes the function directly via the executor. Use it while iterating.

---

## Path A — `poolmgr` (the default)

### 1. Create a Node.js environment

```bash
fission env create \
  --name node \
  --image ghcr.io/fission/node-env-22
```

A generic pool of 3 pods is started for this environment. They stay warm
and become function-specific on first invocation.

```bash
kubectl get pods -n fission -l environmentName=node
```

You should see three `Running` pods named `poolmgr-node-...`.

### 2. Create the function source

Save this as `hello.js`:

```javascript
module.exports = async function (context) {
    return {
        status: 200,
        body: "hello from poolmgr\n",
    };
};
```

### 3. Create the function

```bash
fission fn create \
  --name hello-poolmgr \
  --env node \
  --code hello.js
```

`--executortype` is not needed; `poolmgr` is the default. Under the hood
Fission uploads `hello.js` as a Package, leaves the pool pods untouched
until the first request, then "specializes" one by HTTP-POSTing the code
into it.

### 4. Expose an HTTP route

```bash
fission httptrigger create \
  --name hello-poolmgr-route \
  --function hello-poolmgr \
  --url /hello-poolmgr \
  --method GET
```

### 5. Invoke it

```bash
curl http://localhost:8888/hello-poolmgr
# → hello from poolmgr
```

Try it a second time immediately — it should return in a few
milliseconds (warm).

### 6. Inspect what happened

```bash
# The pod that got specialized is now labeled with functionName
kubectl get pods -n fission -l functionName=hello-poolmgr

# No function-specific Deployment or Service exists
kubectl get deploy,svc -n fission -l functionName=hello-poolmgr
```

One of the three pool pods is now dedicated to `hello-poolmgr`; the
other two remain generic and available for other functions of the same
environment.

---

## Path B — `newdeploy` (per-function Deployment with scale-to-zero)

`newdeploy` creates its own Deployment + Service per function, and
attaches an HPA. With `minscale: 0` the Deployment scales to zero when
idle and back up on the next request.

### 1. Reuse the Node.js environment from Path A

No need to recreate it. If you skipped Path A, run the `fission env
create` from step A.1 now.

### 2. Save the same `hello.js` (or a variant)

```javascript
module.exports = async function (context) {
    return {
        status: 200,
        body: "hello from newdeploy\n",
    };
};
```

### 3. Create the function with `--executortype newdeploy`

```bash
fission fn create \
  --name hello-newdeploy \
  --env node \
  --code hello.js \
  --executortype newdeploy \
  --minscale 0 \
  --maxscale 3 \
  --targetcpu 80
```

`--minscale 0` enables true scale-to-zero; cold starts on newdeploy are
higher than poolmgr because a Deployment must scale up from 0.

### 4. Expose an HTTP route

```bash
fission httptrigger create \
  --name hello-newdeploy-route \
  --function hello-newdeploy \
  --url /hello-newdeploy \
  --method GET
```

### 5. Invoke it

```bash
curl http://localhost:8888/hello-newdeploy
# → hello from newdeploy
```

The first request takes a few seconds (Deployment scaling up from 0).
Subsequent requests are warm.

### 6. Inspect what happened

```bash
# A Deployment + Service + HPA exist for this function
kubectl get deploy,svc,hpa -n fission -l functionName=hello-newdeploy
```

Watch it scale to zero after the idle timeout (default ~2 minutes):

```bash
kubectl get pods -n fission -l functionName=hello-newdeploy -w
```

---

## Path C — `container` (bring your own image)

The container executor skips environments, Packages, fetchers,
builders, and specialization. Fission just runs the image you provide
behind its router.

No environment needed.

### 1. Create the function with `fn run-container`

```bash
fission fn run-container \
  --name hello-container \
  --image nginxdemos/hello:plain-text \
  --port 80
```

`nginxdemos/hello:plain-text` is a ~20MB public image that returns
plain-text server info on port 80. The `--port` flag tells Fission
which port to forward traffic to inside the pod.

> **Note:** `fission fn create --executortype container` is intentionally
> rejected by the CLI. Use `fission fn run-container` instead.

### 2. Expose an HTTP route

```bash
fission httptrigger create \
  --name hello-container-route \
  --function hello-container \
  --url /hello-container \
  --method GET
```

### 3. Invoke it

```bash
curl http://localhost:8888/hello-container
# → Server address: 10.x.x.x:80
#   Server name: hello-container-...
#   ...
```

### 4. Inspect what happened

```bash
kubectl get deploy,svc -n fission -l functionName=hello-container
kubectl describe pod -n fission -l functionName=hello-container | \
    grep -E 'Image:|Port:'
```

You should see `Image: nginxdemos/hello:plain-text` and no Fission-side
specialization.

### Bring your own image

Any HTTP server listening on a TCP port works. A minimal Dockerfile:

```dockerfile
FROM python:3.13-alpine
COPY app.py /app.py
EXPOSE 8080
CMD ["python", "/app.py"]
```

```python
# app.py
import http.server, socketserver, os
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers()
        self.wfile.write(b"hello from my own container\n")
with socketserver.TCPServer(("", 8080), H) as s: s.serve_forever()
```

Build and push, then:

```bash
fission fn run-container \
  --name hello-mine \
  --image registry.example.com/you/hello:v1 \
  --port 8080
```

---

## Side-by-side: which one should I pick?

|                          | `poolmgr` (default)                     | `newdeploy`                                    | `container`                           |
|--------------------------|-----------------------------------------|------------------------------------------------|---------------------------------------|
| Source or image          | Source + Environment                    | Source + Environment                           | Pre-built image you provide           |
| Cold start (first)       | Seconds (pool warmup, then specialize)  | Seconds–tens of seconds (Deployment scale-up)  | Seconds (image pull + pod start)      |
| Cold start (subsequent)  | ~10ms (warm pool specialize)            | ~10ms (warm) / seconds (after scale-to-zero)   | ~10ms (warm) / image-dependent (cold) |
| Scale-to-zero            | No (pool is always running)             | Yes (`--minscale 0`)                           | Yes (`--minscale 0`)                  |
| Isolation between funcs  | Weak (shares pool capacity)             | Strong (per-function Deployment)               | Strong (per-function Deployment)      |
| Kubernetes objects       | Shared pool Deployment per Environment  | Deployment + Service + HPA per function        | Deployment + Service per function     |
| Good for                 | Many small functions, tight latency    | Long-running or bursty functions, isolation    | Existing containers, custom runtimes  |
| Fission builds for you   | Yes (Environment + Package pipeline)    | Yes (Environment + Package pipeline)           | No — you build and push the image     |

As a rule of thumb: **start with poolmgr**, switch a specific function
to **newdeploy** when it needs scale-to-zero or stronger isolation,
and use **container** when you already have a Docker/OCI image or need
a runtime Fission doesn't ship.

---

## Preview — OCI-native packages (RFC-0001, Phase 1)

> **Status:** Preview. RFC-0001 Phase 1 (CRD + validation) is merged.
> CLI support, executor integration, and an end-to-end invocation path
> land in subsequent phases. See
> [`rfc/0001-oci-native-package-delivery.md`](../../rfc/0001-oci-native-package-delivery.md)
> for the full plan and phase status.
>
> **What works today:** you can author a `Package` that references an
> OCI artifact instead of a tarball, apply it with `kubectl`, and have
> the apiserver admit or reject it based on the new CEL rules.
>
> **What doesn't work yet:** invoking a function whose Package uses
> `oci:` — the executor will reach the same fetcher-based code path it
> does today and fail to find a deployment archive. Wait for Phase 2
> before pointing a `Function` at an OCI Package in production.

OCI-native delivery replaces Fission's tarball + storagesvc code path
with a plain container-registry pull: the function pod gets the code
via a standard `image` pull (with layer cache, cross-node dedup,
signing, etc.) instead of a `fetcher` HTTP download. The opt-in signal
is a new optional field: `Package.Spec.Deployment.OCI`.

### 1. Author a Package that references an OCI artifact

Save as `hello-oci-package.yaml`:

```yaml
apiVersion: fission.io/v1
kind: Package
metadata:
  name: hello-oci-pkg
  namespace: default
spec:
  environment:
    name: node
    namespace: default
  deployment:
    type: oci
    oci:
      image: ghcr.io/example/hello-fn:v1
      # Optional: pin the artifact content hash.
      # digest: sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
      # Optional: registry pull secret in the same namespace.
      # imagePullSecrets:
      #   - name: regcred
      # Optional: path inside the artifact treated as the deployment root.
      # subPath: deploy
```

Apply it:

```bash
kubectl apply -f hello-oci-package.yaml
# package.fission.io/hello-oci-pkg created

kubectl get package hello-oci-pkg -o yaml | grep -A6 'deployment:'
#   deployment:
#     oci:
#       image: ghcr.io/example/hello-fn:v1
#     type: oci
```

### 2. See the new validation rules fire

The Archive schema now carries two `x-kubernetes-validations` (CEL) rules
enforcing that `oci` is mutually exclusive with `literal` and `url`.
Save this intentionally bad Package as `bad-oci-package.yaml`:

```yaml
apiVersion: fission.io/v1
kind: Package
metadata:
  name: bad-oci-pkg
  namespace: default
spec:
  environment:
    name: node
    namespace: default
  deployment:
    type: oci
    url: https://example.com/pkg.zip      # ← conflicts with oci
    oci:
      image: ghcr.io/example/hello-fn:v1
```

Apply it and observe the apiserver rejection:

```bash
kubectl apply -f bad-oci-package.yaml
# The Package "bad-oci-pkg" is invalid: spec.deployment: Invalid value:
#   "object": archive.oci and archive.url are mutually exclusive
```

The same rejection happens for an `oci` + `literal` combination. These
rules run at the apiserver regardless of whether Fission's admission
webhook is running — which is the main win of moving validation from Go
to CEL.

### 3. Inspect the generated CRD schema

The CEL rules and the new `oci` sub-schema are visible in the Package
CRD:

```bash
kubectl explain package.spec.deployment.oci
# KIND:     Package
# VERSION:  fission.io/v1
#
# FIELD: oci <Object>
#
# DESCRIPTION:
#   OCI references an OCI artifact (container image or OCI artifact) that
#   holds the deployment contents. Mutually exclusive with Literal and URL.
#   When set, the function pod mounts the artifact directly instead of
#   fetching a tarball.
# ...

kubectl get crd packages.fission.io -o yaml | \
    yq '.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.deployment.x-kubernetes-validations'
# - message: archive.oci and archive.url are mutually exclusive
#   rule: '!(has(self.oci) && has(self.url) && self.url != '''')'
# - message: archive.oci and archive.literal are mutually exclusive
#   rule: '!(has(self.oci) && has(self.literal) && size(self.literal) > 0)'
```

### 4. What's next (when Phase 2 lands)

When RFC-0001 Phase 2 ships, the following will work end-to-end:

```bash
# Phase 3 CLI — not available today
fission pkg create --name hello-oci-pkg --env node \
    --oci ghcr.io/example/hello-fn:v1

fission fn create --name hello-oci --pkg hello-oci-pkg \
    --executortype newdeploy

fission httptrigger create --function hello-oci --url /hello-oci
curl http://localhost:8888/hello-oci
# → hello from oci
```

Under the hood the executor will build Deployment pod specs whose
container image *is* the OCI artifact from the Package — skipping the
`fetcher` sidecar, the `storagesvc` download, and the specialization
HTTP call entirely. Pod cold start drops to a standard image pull
(which, once cached on a node, is ~hundreds of milliseconds).

Clean up the preview:

```bash
kubectl delete package hello-oci-pkg --ignore-not-found
kubectl delete package bad-oci-pkg --ignore-not-found
```

---

## Cleanup

```bash
fission httptrigger delete --name hello-poolmgr-route
fission httptrigger delete --name hello-newdeploy-route
fission httptrigger delete --name hello-container-route

fission fn delete --name hello-poolmgr
fission fn delete --name hello-newdeploy
fission fn delete --name hello-container

fission env delete --name node
```

Stop the port-forward with `Ctrl+C`.

---

## Troubleshooting

**`fission fn test` returns logs on failure.** If a request returns
non-200, `fission fn test --name <fn>` automatically fetches recent pod
logs. Use it to see stack traces without digging into `kubectl logs`.

**Pool pods stay `Pending`.** Check your cluster has enough CPU/memory
for the default pool size of 3. Either scale the cluster up or create
the environment with `--poolsize 1` for a tighter footprint.

**`curl` hangs on the first request.** You're hitting a cold start.
For `newdeploy` with `--minscale 0`, a scale-up from zero can take
10–20 seconds. For `poolmgr`, specialization of a pool pod takes
~1 second. Subsequent requests should be fast.

**Port-forward drops.** `kubectl port-forward` isn't durable; re-run
the command if it exits. For longer-running setups, expose the router
via an Ingress or LoadBalancer.

---

## What's next

- **Triggers beyond HTTP** — `fission timer create` for cron-style
  invocation, `fission mqtrigger create` for message-queue-driven
  functions, `fission kubewatcher create` to invoke on Kubernetes
  resource events.
- **Packages and builders** — the
  [Package guide](https://fission.io/docs/usage/package/) covers
  multi-file functions, custom build commands, and dependency caching.
- **OCI-native delivery (RFC-0001)** — see the
  [Preview](#preview--oci-native-packages-rfc-0001-phase-1) section
  above. Once Phase 2 lands, the CLI additions described there will
  replace hand-written Package YAML.

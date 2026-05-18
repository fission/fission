# Cross-Origin Threat Model — Per Fission HTTP Listener

One paragraph per listener.
Each paragraph states: (a) who reaches it in a default install, (b) what cross-origin attacks apply today, (c) the round-3 mitigation.

## Router — Public Listener (port 8888)

**Who reaches it.** Any HTTP client reachable from the cluster Ingress / LoadBalancer / NodePort.
That includes browser JavaScript on an attacker-controlled page, since the NetworkPolicy at `charts/fission-all/templates/router/networkpolicy.yaml` intentionally allows any source on port 8888 — HTTPTriggers must be browser-callable.
Serves three categories of routes: router-owned (`/router-healthz`, `/_fission/version`, `/_fission/auth/login` when AuthConfig.IsEnabled), and user-defined HTTPTrigger paths registered by `buildMuxes` at `pkg/router/httpTriggers.go:201+`.

**Attacks applicable today.**
CSRF: a malicious page can issue `POST /any-trigger-path` in the browser of a logged-in user; Fission writes no CSRF token surface today.
Cross-origin read / JSON hijacking: handlers set `application/json` content-type, which by spec prevents `<script src>` execution and CORS reads from another origin without `Access-Control-Allow-Origin` echo — so reading the response cross-origin is blocked.
MIME sniffing: without `X-Content-Type-Options: nosniff`, some legacy browsers may sniff a JSON body as HTML if a user function returns text that looks HTML-ish; in combination with a future regression that echoes `Access-Control-Allow-Origin: *`, this becomes a read primitive.

**Round-3 mitigation.**
`SecurityHeaders` wrap on the entire public listener adds `nosniff` + `Vary: Origin` to every response.
Router-owned routes get an additional `DenyAllCORS` wrap so any browser preflight from another origin is rejected at the middleware layer; this also strips any `Access-Control-*` header an inner handler might inadvertently set.
User-trigger routes deliberately remain unchanged — B4 introduces opt-in `HTTPTriggerSpec.CorsConfig` so SPAs can allowlist specific origins without disabling the deny default.

## Router — Internal Listener (port 8889)

**Who reaches it.**
Cluster-internal pods only: executor, kubewatcher, timer, mqtrigger, canaryconfigmgr, buildermgr (per `charts/fission-all/templates/router/networkpolicy.yaml`).
ClusterIP `svc/router-internal` is never exposed via NodePort/LoadBalancer.
Serves `/fission-function/<ns>/<name>` invocation routes only.

**Attacks applicable today.**
Already HMAC-protected via `hmacauth.ServiceVerifier(... ServiceRouterInternal ...)` since GHSA-3g33-6vg6-27m8.
Browser-cross-origin is structurally improbable — no Service exposes this port outside the cluster.
But: any misconfigured Ingress that forwards `:8889` (e.g., a copy-paste of router.svc.yaml that leaves `spec.type: ClusterIP` unchanged but is then patched via overlay) becomes browser-reachable.
Defense-in-depth matters here because the HMAC verifier passes through when `FISSION_INTERNAL_AUTH_SECRET` is empty (first-deploy mode); a browser preflight in that mode would then proxy to function pods.

**Round-3 mitigation.**
`DenyAllCORS` wrapped outside the HMAC verifier (so preflights are rejected before HMAC even reads the body), with `SecurityHeaders` outermost.
This keeps the listener strictly machine-to-machine.

## Executor (port 8888 on ClusterIP svc)

**Who reaches it.**
Router only, per `charts/fission-all/templates/executor/networkpolicy.yaml` (matchLabels: `application=fission-router, svc=router`).
ClusterIP — not browser-reachable in a default install.
Endpoints: `/v2/getServiceForFunction`, `/v2/tapServices`, `/v2/unTapService`, `/v2/debugInfo`, `/healthz`.

**Attacks applicable today.**
None directly from a browser.
A future regression that exposes executor via Ingress, or a workload running in the install namespace with the router label, would have full executor API access without any CORS gate.

**Round-3 mitigation.**
`DenyAllCORS` + `SecurityHeaders` at the same wrap site as router-internal.
Executor stays machine-to-machine; preflights are rejected.

## Storagesvc (port 8000 on ClusterIP svc)

**Who reaches it.**
buildermgr and function pods per `charts/fission-all/templates/storagesvc/networkpolicy.yaml`.
ClusterIP — not browser-reachable.
Endpoints: `/v1/archive` (POST upload, GET download, DELETE, HEAD).

**Attacks applicable today.**
None directly from a browser.
Storagesvc holds user package archives; a CORS misconfiguration would let any browser-reachable page read archives if the listener were ever exposed.

**Round-3 mitigation.**
`DenyAllCORS` + `SecurityHeaders`.
Defense-in-depth aligned with executor.

## Fetcher (sidecar, no Service)

**Who reaches it.**
Same pod (function or builder pod) only.
There is no Kubernetes Service; the listener binds inside the pod.
Already HMAC-protected.

**Attacks applicable today.**
Browser is unreachable.
But the listener IS reachable from any container that shares the pod network namespace — and per-env pods often run user code alongside fetcher.
A malicious package that loads attacker-controlled JS into a node-env's sandbox could `fetch('http://localhost:8000/')` and reach the fetcher endpoints if HMAC is off (`FISSION_INTERNAL_AUTH_SECRET` empty).

**Round-3 mitigation.**
`DenyAllCORS` doesn't help in-pod-localhost (loopback `Origin` would still match) — its value is making preflights explicit and stripping future stray headers.
`SecurityHeaders` adds `nosniff` to fetcher JSON responses so a user function trying to read `/version` and pivot via MIME confusion fails.

## Builder (sidecar, port 8001, no Service)

Same characteristics as fetcher; same mitigation.

## Webhook server (port 9443 on ClusterIP svc) — OUT OF SCOPE

kube-apiserver is the only caller; the AdmissionWebhookConfiguration's CA pin + apiserver-internal routing make CORS structurally meaningless.
Documented here for completeness so future re-triages don't try to wire `DenyAllCORS` into the controller-runtime webhook server.

## Prometheus metrics (port 8080) — OUT OF SCOPE

`promhttp.Handler` writes Prometheus text format; scrapers are not browsers.
A `DenyAllCORS` wrap would be inert.
Skipped to keep blast radius minimal.

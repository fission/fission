# RFC-0017: Function Developer Debugging Toolkit (CLI)

- Status: Partially implemented ([#3519](https://github.com/fission/fission/pull/3519) + follow-up).
  Phase 1 (`fission function describe`), phase 2 (`fission function test` enrichment), and phase 3 (`logs --request-id/--trace-id` + real streaming `--follow`) are implemented.
  The `describe` invocability headline reads the **RFC-0002 EndpointSlice data-plane** state (the `fission.io/served` label) via the k8s API — chosen over a CLI-reachable executor `/v2/diag/function` endpoint, which would have meant bypassing the executor's HMAC auth or adding CLI-side signing for marginal extra detail.
  Only the cold-start metrics panel (a CLI→Prometheus query path, needs a metric that does not yet exist) remains.
  See "As implemented".
- Tracking issue: —
- Supersedes: —
- Targets: Fission v1.N+ (phased; `describe` over existing data lands first)
- Requires: no Kubernetes floor change; no new third-party dependencies; no server subsystem (this RFC is a CLI aggregation layer over data that already exists or is produced by RFC-0015 and RFC-0016).
- Related: [RFC-0015](0015-invocation-correlation-and-failure-attribution.md) (the request-ID, structured-error body, and `/v2/diag/function` this CLI surfaces), [RFC-0016](0016-otlp-native-logging-pipeline.md) (the `--request-id` log query and streaming this CLI exposes).

## Summary

Diagnosing a failing function today means hopping across five commands — `fission function test`, then `logs`, then `pods`, then `getmeta`, then `package info` — and knowing Kubernetes condition reasons by heart, a 30–120-second ritual per diagnosis.
There is no single view of a function's health, no request-ID in `test` output, and no rendering of *where* a call failed.
This RFC adds a **single pane of glass** — `fission function describe` — and makes the everyday commands self-diagnosing: `fission function test` prints the invocation's request-ID and, on failure, the structured `{component, reason}` from RFC-0015 and the correlated logs from RFC-0016.
It is deliberately thin: it adds no server, consuming the diagnostics endpoint, access records, metrics, and conditions the other RFCs already produce, and it degrades gracefully against an older cluster.

## Motivation

The data needed to debug a function exists but is scattered:

- `fission function test` (`pkg/fission-cli/cmd/function/test.go`) invokes via the router and, on a 4xx/5xx, makes a best-effort grab of pod logs — but it shows only the response body, no request-ID, and no indication of which component failed.
- `fission function getmeta` prints the function's `metav1.Condition`s (via `util.PrintConditions`) — `Ready`, `PackageReady`, `ToolExposed` — but only at major transitions.
- `fission function pods` lists pod readiness; `fission package info` carries the build log in `Package.Status.BuildLog` (escaped); `fission function logs` queries by pod + time.

A developer must run all of them and mentally join the results.
After RFC-0015 and RFC-0016 land, the missing piece is presentation: surface the new request-ID, structured attribution, per-request logs, and invocability in one place, and stop making the developer be the join engine.
This is the Lambda-console-equivalent for the Fission CLI.

## Goals

- One command — `fission function describe <name>` — that answers "what is the state of this function and why is it failing?" from a single invocation.
- Surface correlation everywhere a developer already looks: the request-ID in `test`, and request-ID/trace-ID/streaming in `logs`.
- Make `fission function test` self-diagnosing: on failure, say *where* it failed and show the correlated logs automatically.
- Graceful degradation against a server that predates RFC-0015/0016.

## Non-goals

- Any new server subsystem, endpoint, or CRD — this RFC only consumes existing surfaces.
- A TUI / web dashboard (the CLI is the surface; metrics dashboards remain Grafana's job).
- Replacing `getmeta`/`pods`/`package info` — `describe` aggregates them; they remain as focused commands.

## Design

### 1. `fission function describe <name>`

A new command under `pkg/fission-cli/cmd/function/` (`describe.go`, registered in `command.go`) that aggregates existing sources into one rendered view:

- **Summary** — name, environment, executor type, labels/annotations (from the Function object).
- **Build / package** — `Package.Status.BuildStatus` + conditions (reuse `util.PrintConditions`) and, on failure, the build log (reuse the build-log retrieval helper in `pkg/fission-cli/cmd/package`).
- **Pods** — readiness and status (reuse the `function pods` listing logic).
- **Invocability** — call RFC-0015's `GET /v2/diag/function`: `{invocable, reason, readyEndpoints, busyEndpoints, lastColdStartError}`.
  The headline line answers "can I call this right now, and if not, why?".
- **Recent invocations** — from RFC-0016's access records (or the `fission_function_*` metrics): last N calls with request-ID, status, and latency.
- **Recent logs** — a short tail via the logdb registry.

The output replaces the five-command hop with one screen; each section is independently sourced, so a missing source (older server, no log backend) degrades to "unavailable" rather than failing the command.

### 2. Enrich `fission function test`

- Always print the returned `X-Fission-Request-ID` so the developer can immediately `fission function logs --request-id <id>`.
- On failure, parse the structured `{component, reason, requestId, traceId}` body from RFC-0015 and render a one-line diagnosis — e.g. `✗ failed in executor (specialization_failed) — request abc123` — instead of dumping a raw body.
- Replace the current best-effort pod-logs grab with an automatic `--request-id`-scoped log fetch (RFC-0016), so the relevant lines appear without a second command.
- Share the invoke path with `function run` (RFC-0018) by reusing the single HTTP helper in `test.go`, so behavior is identical across `test`, `run`, and `describe`'s probe.

### 3. Surface request-ID / trace-ID / streaming in `fission function logs`

Thin CLI exposure of RFC-0016's read path: `--request-id`, `--trace-id`, and a real streaming `--follow` (no more one-second polling).
These are additive flags on the existing command.

### 4. Cold-start / latency hints

An optional `describe` panel reads `fission_coldstart_phase_seconds` / `fission_function_cold_starts_total` (RFC-0015) to flag cold-start-heavy functions and show a Lambda-style "REPORT"-equivalent latency summary, so a developer can see whether slowness is cold starts vs function time.

## Phased implementation

1. **`function describe` over existing data** — summary + conditions + pods + build log, using only what exists today (no RFC-0015/0016 dependency).
   Useful immediately.
2. **`test` enrichment** — request-ID echo + structured-error rendering (needs RFC-0015 phases 1–2).
3. **`logs` request-ID/trace-ID/streaming** — surface RFC-0016's read path (needs RFC-0016 phases 1, 3, 4); add the invocability + recent-invocations panels to `describe`.
4. **Cold-start / latency hints** — the metrics panel.

## As implemented

Phase 1 — `fission function describe <name>` — is implemented in `pkg/fission-cli/cmd/function/describe.go` (registered in `command.go`).
It is a pure CLI aggregation over existing API objects, with no server, CRD, or dependency change:

- **Summary** — name, namespace, environment (namespace-qualified when non-default), executor type, package reference, age, and labels, from the `Function` object.
- **Conditions** — the function's status conditions via the shared `util.PrintConditionsTo`.
- **Package** — the referenced `Package`'s build status and conditions; the build log is surfaced **only on a failed build** (the actionable case), keeping the healthy-path view compact.
- **Pods** — the backing pods (same `functionName`/`functionNamespace` label selector as `function pods`), rendered with readiness via `utils.PodContainerReadyStatus`.

Each section is sourced independently and best-effort: a missing/unreadable package or pod list degrades to `<none>` rather than failing the command (a `Function` that cannot be fetched is the only hard error).
Like `kubectl describe`, the command is human-readable only — `getmeta -o json` / `package info -o json` remain the machine-readable surfaces.
Unit tests cover the rendering, the failed-build log, graceful degradation, and the not-found error; an integration test (`TestFunctionDescribe`) runs the real command against a warmed function.

`describe` also renders an **Invocability** headline ("can I call this right now?") synthesized from the data it already has — the `Ready` condition plus the count of fully-ready pods — so it needs no executor endpoint: a Ready function with a warm pod reads `Yes (N warm pod(s))`, a Ready function with none reads `Yes (cold start on first call)`, and a not-Ready function reads `No`.
The deeper executor-state view (`/v2/diag/function`: PoolCache/endpoint state, last cold-start error) remains future work.

Phase 2 — `fission function test` enrichment — is implemented in `pkg/fission-cli/cmd/function/test.go`:

- The per-invocation **request id** (`X-Fission-Request-ID`, RFC-0015) is echoed to **stderr** (keeping stdout clean for the function body) on every call, so the developer can immediately `fission function logs --request-id <id>`.
- On failure, when the router attributed the error (the `X-Fission-Component` header is present), `test` renders a one-line diagnosis — `✗ function "x" failed in executor (specialization_failed) — status 503, request abc` — parsing the structured `{component, reason, message}` body (`pkg/error.InvocationError`); the debug `message` is shown when the server included it.
  Absent the header (a server predating RFC-0015, or a function's own error response), it falls back to the raw body, so it degrades cleanly.
  A correlated-logs hint naming the request id is printed after the existing pod-log/log-db grab.
- **Invoke path** (post GHSA-3g33-6vg6-27m8): `fission function test` targets the router **internal** listener (port 8889, `svc/router-internal`) for `/fission-function/<ns>/<name>`, not the public listener (8888) which no longer serves that path. The request is HMAC-signed with the `ServiceRouterInternal` key when `FISSION_INTERNAL_AUTH_SECRET` is set. When internal auth is enabled, the operator must export `FISSION_INTERNAL_AUTH_SECRET` (the master secret from `kubectl get secret fission-internal-auth -n <ns> -o jsonpath='{.data.secret}' | base64 -d`) before running `fission function test`, or the router returns `401`/`403`. See `docs/internal-auth/00-design.md` for details.

The structured-failure rendering is a pure, table-tested helper (`renderInvocationFailure`); the request-id echo and the structured `logs` filters (`--request-id`/`--trace-id`, RFC-0016) complete the CLI side of phase 3.

Recent-invocations (RFC-0016 access records), a recent-log tail, real streaming `--follow` (RFC-0016 phase 5), and cold-start hints (a CLI→Prometheus path over `fission_function_cold_starts_total`) are deferred — they depend on server-side surfaces this CLI-only RFC does not build.

## Backward compatibility & migration

- `fission function describe` is a new additive command; `test` and `logs` only gain output and flags — existing invocations and scripts are unaffected.
- **Graceful degradation against an older server** is a first-class requirement: `describe`/`test` fall back to the legacy plain-text error when there is no structured body; the invocability panel is skipped when `/v2/diag/function` returns 404; `--request-id` log queries no-op cleanly when access records are not present.
  The toolkit never errors on CLI-vs-server version skew — it shows "unavailable" for the parts the cluster cannot serve.
- No CRD, server, or Helm change; nothing to migrate.

## Test strategy

- **Unit.**
  `describe` rendering from faked condition/pod/diag inputs (table-driven, matching existing `pkg/fission-cli/cmd/function/*_test.go` style); `test` structured-error rendering and the older-server plain-text fallback; flag wiring for the new `logs` flags.
- **Integration** (`test/integration/suites/common`).
  `describe` on a deliberately failed-build function shows the build log and `invocable: false`; `test` on a broken function renders `component: executor` and auto-fetches the correlated logs; `logs --request-id` returns the right lines after an invocation.

## Success metrics

- A developer diagnoses a failing function with **one** command (`describe`) instead of five.
- `fission function test` on a failure names the responsible component and shows the correlated logs without a follow-up command.
- The end-to-end portfolio check (a single failing invocation visible as structured error → request-ID → correlated logs → one `describe` view) passes.

## Open questions / risks

- **Output format.**
  Human-readable by default; a `-o json` mode for `describe` is worth adding for scripting — decide whether to ship it in phase 1.
- **Recent-invocations source.**
  Reading recent calls from metrics is coarse (counters, not a list); the access records (RFC-0016) are the better source but require the log backend.
  `describe` should prefer access records and fall back to a metrics summary.
- **Panel cost.**
  `describe` fans out to several sources (API, diag endpoint, log backend); run them concurrently and time-box each so one slow source does not stall the view.

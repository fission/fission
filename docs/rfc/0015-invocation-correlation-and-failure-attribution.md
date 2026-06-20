# RFC-0015: Invocation Correlation & Failure Attribution

- Status: Implemented (phases 1–4, [#3515](https://github.com/fission/fission/pull/3515)); phase 5 (diagnostics surface) folded into RFC-0017 (planned) (see "As implemented").
- Tracking issue: [#693](https://github.com/fission/fission/issues/693) (return a traceable ID to the caller)
- Supersedes: —
- Targets: Fission v1.N+ (phased; each phase independently shippable)
- Requires: no Kubernetes floor change; no new third-party dependencies (reuses the already-vendored OpenTelemetry SDK and `github.com/google/uuid`); no CRD schema change.
- Related: RFC-0016 (planned) (the logging pipeline consumes the request-ID this RFC introduces), RFC-0017 (planned) (the CLI surfaces the attribution), [RFC-0008](0008-streaming-invocation-path.md) (streaming aborts after the first byte are header-only), [RFC-0002](0002-endpointslice-native-data-plane.md) (the resolver/tap seams the error path runs through).

## Summary

When a Fission function invocation fails today the caller gets an opaque HTTP status and a fixed plain-text body (`error sending request to function`), with no stable identifier and no indication of *where* the failure happened — a Fission component, the user's code, a timeout, or a cold start.
This RFC introduces a stable per-invocation `X-Fission-Request-ID`, a structured JSON error response that attributes each failure to a `component` and a `reason`, end-to-end trace context that reaches the function pod, real cold-start child spans, and an error-biased sampler so failed invocations are always recorded.
It is the keystone of the observability portfolio: every later capability (per-request logs in RFC-0016, the single-pane CLI in RFC-0017) correlates on the request-ID this RFC propagates.
All of it is additive except the error-body format, which is gated behind `ROUTER_STRUCTURED_ERRORS` (default on) with a one-flag escape hatch back to the exact legacy bytes.

## Motivation

The failure path is verifiably lossy.
Concrete anatomy, all against `main`:

- `getProxyErrorHandler` (`pkg/router/functionHandler.go:180-241`) collapses every failure into one of four branches — stream-abort (504), client-close (499), deadline (504), and a `default` that maps the error to a status via `ferror.GetHTTPError` and writes the fixed string `error sending request to function`.
  The body never says which component failed, and the explicit `// TODO: return error message that contains traceable UUID back to user. Issue #693` at line 232 is still open.
- The router already classifies network failures precisely — `pkg/error/network/error.go` exposes `IsDialError`, `IsConnRefusedError`, `IsTimeoutError`, `IsUnsupportedProtoScheme` — but that classification is consumed **only** inside the retry loop in `pkg/router/transport.go` and is **thrown away** once retries are exhausted; the error handler never sees it.
- There is no stable per-invocation token.
  `setFunctionMetadataToHeader` (`pkg/router/requesthHeader.go`) propagates function identity (`X-Fission-Function-Uid/Name/Namespace/ResourceVersion`) but nothing that uniquely identifies one call.
- Trace context stops at the router/executor boundary.
  The router wraps handlers with `otelhttp` (`pkg/router/router.go`, `GetHandlerWithOTEL`) and injects trace IDs into its own logs (`pkg/utils/otel/log.go`, `LoggerWithTraceID`), but the cold-start path emits only span *events* (`otelUtils.SpanTrackEvent`) rather than child spans with status, and `MarkSpecializationFailure` in `pkg/executor/executortype/poolmgr/gpm.go` records an event, not an error.
- With sampling enabled, the trace that would explain a failure usually does not exist — a failed cold start is dropped at the same ratio as a successful warm call.

The result: an operator debugging "why did this 502 happen?" has no request-ID to search, no component attribution to read, and frequently no trace to open.
Every comparable platform (AWS Lambda's `x-amzn-RequestId`, GCF's request path) gives the caller a correlation token; Fission does not.

## Goals

- A stable `X-Fission-Request-ID` per invocation — honored if the caller supplies one, otherwise minted — surfaced in the response headers (success and failure) and in structured error bodies, and propagated router → executor → fetcher → function pod.
- Structured failure attribution: a JSON body carrying `{component, reason, requestId, traceId}`, built by **consuming the existing `pkg/error/network` classifiers** instead of discarding them, and distinguishing executor-RPC failures from function-round-trip failures.
- W3C trace context delivered into the function pod so instrumented user code joins the same trace, plus real cold-start child spans (`reserve`, `fetch`, `specialize`, `ready`) that carry the failure reason on the failing phase.
- Error-biased sampling so a failed invocation is always recorded, with zero new infrastructure required.
- A minimal "is function X invocable right now, and if not, why?" surface for the CLI and operators.

## Non-goals

- Changing the executor RPC body (`/getServiceForFunction` stays a JSON `Function` → address string).
- Replacing OpenTelemetry or the `autoprop` propagator — this RFC extends the existing setup.
- Requiring a tail-sampling Collector (offered as the documented scale-out option, never a dependency).
- Changing HMAC canonicalization — the internal-listener signer signs method + URI + body only, so every new header is free to add and never participates in a signature.
- Per-line correlation of arbitrary user `stdout` (that is RFC-0016's hybrid access-record + env-image work; this RFC delivers the request-ID those records key on).

## Design

### 1. Request / invocation ID (`pkg/utils/correlation`)

A new leaf package `pkg/utils/correlation` holds the header names and the derivation helper so the router, executor, and fetcher can reference them without importing `pkg/router`:

```go
package correlation

const (
    HeaderRequestID = "X-Fission-Request-ID" // stable per-invocation id
    HeaderComponent = "X-Fission-Component"  // echoed on error responses
    HeaderDebug     = "X-Fission-Debug"      // opt-in verbose error bodies
)

// ID returns the correlation id for a request: the inbound header if present,
// else a freshly minted UUID. The trace id is attached separately, never folded in.
func ID(inbound string) string
```

**Recommendation: honor an inbound ID, else mint a fresh `uuid.NewString()`, and attach the trace ID as a *separate* field.**
Deriving the request-ID from the trace ID was considered and rejected: a single client trace that fans out to two functions would yield two invocations sharing one ID.
Minting per invocation keeps the ID 1:1 with a call; the trace ID is still recorded alongside (`traceId` in the body, a span attribute, a log field) so "find the trace for request X" remains a lookup.
When tracing is disabled (no OTLP endpoint), the trace ID is the zero value and is simply omitted — the request-ID still works.

**Generation point.**
A thin middleware wraps *both* mutable routers in `pkg/router/router.go`'s `serve()`:

- On the public listener it sits **inside** the `otelhttp` handler, so the extracted `SpanContext` is already in `ctx`.
- On the internal listener it sits **inside** the HMAC `ServiceVerifier`, so the verifier still signs only body + URI and the ID header is added post-verification.

The middleware reads `X-Fission-Request-ID`; if absent it calls `correlation.ID("")`; it sets the value on the request header (so downstream header setters see it), stores it in the request context, and sets it on the response via a `ResponseWriter` wrapper before the first write.
Because both listeners are covered, every internal caller — timer, kubewatcher, mqtrigger, MCP — also gets a correlation ID on `/fission-function/...`.

**Propagation chain.**

- Router → function pod: extend the header setters in `pkg/router/requesthHeader.go` (called from `functionHandler.handler`) to set `X-Fission-Request-ID`.
- Router → executor RPC: `pkg/executor/client/client.go` sets the header from the context value on `GetServiceForFunction` and `EnsureCapacity` (signature-safe — the signer ignores headers).
- Executor → fetcher: the specialize call in `pkg/executor/executortype/poolmgr/gp_specialize.go` sets the header from context onto the fetcher request.
- Response: set by the middleware; the structured error body (below) repeats it.

This is what closes Issue #693: the structured body carries the request-ID and trace ID — the "traceable UUID back to user" the issue asks for.

### 2. Failure attribution (`pkg/error/invocation.go` + a rewritten error handler)

A new type alongside the existing error helpers:

```go
type Component string

const (
    ComponentRouter   Component = "router"
    ComponentExecutor Component = "executor"
    ComponentFetcher  Component = "fetcher"
    ComponentFunction Component = "function"
    ComponentTimeout  Component = "timeout"
)

type InvocationError struct {
    Component Component `json:"component"`
    Reason    string    `json:"reason"`            // stable, safe taxonomy value
    RequestID string    `json:"requestId"`
    TraceID   string    `json:"traceId,omitempty"`
    Message   string    `json:"message,omitempty"` // raw detail; only when gated
}
```

The architectural move is to stop discarding the round-tripper's classification.
`pkg/router/transport.go` already calls `network.Adapter(err)` and the `Is*` classifiers in its retry loop; on exhausted retries it currently returns a bare `err`.
Instead it returns a small sentinel `routerError{component, reason, err}` from its failure branches, and `getProxyErrorHandler` (`pkg/router/functionHandler.go:180`) uses `errors.As` to read it.
The taxonomy and how each is detected at the router:

| Component | Reason | Detected by |
|---|---|---|
| `timeout` | `function_timeout` | `errors.Is(err, context.DeadlineExceeded)` (existing branch) |
| `timeout` | `stream_idle` / `stream_max_duration` | `errors.Is(context.Cause(ctx), errStreamIdleTimeout/MaxDuration)` (existing branch) |
| `router` | `client_disconnect` | `errors.Is(err, context.Canceled)` → still 499 (not a server failure) |
| `executor` | `specialization_failed` / `capacity_exceeded` / `executor_unavailable` | the resolver/RPC error, wrapped as a `routerError` at the resolver boundary; the 429 path maps to `capacity_exceeded` |
| `function` | `connection_refused` / `dial_error` | round-trip dial errors via `network.Adapter` + `IsConnRefusedError`/`IsDialError` — the function pod is unreachable |
| `function` | `function_error` | a user response with status ≥ 500 (handled in `ModifyResponse`, not the error path) |

Status codes are unchanged — still derived via `ferror.GetHTTPError` — so a client that only reads the status sees no difference.

**Public-safe body, gated verbosity.**
The default body is `{component, reason, requestId, traceId}` only — no raw Go error strings, no internal hostnames.
The `Message` field (the raw `err.Error()`) is included only when the request carries `X-Fission-Debug: true` *and* the router runs with `isDebugEnv` (the existing debug gate already threaded through `functionHandler`), so verbose detail is opt-in and never leaks to anonymous callers.
`Content-Type` becomes `application/json`; the plain-text path is kept only as the marshal-failure fallback and behind the compat flag (see Backward compatibility).

### 3. Trace context into the pod + cold-start spans

**Into the pod.**
The router already proxies through `otelhttp.NewTransport`, which injects `traceparent`/`tracestate`/`baggage` via the global `autoprop` propagator on every outgoing request that carries a span — so the function-pod request is *already* trace-propagated for the normal proxy path.
Two gaps close it fully: the WebSocket-upgrade path forces the raw transport and must inject manually (`otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))`); and we document that an unsampled span still injects a valid `traceparent` (sampled-flag 0), so user code can join regardless of sampling.

**Cold-start child spans.**
The executor RPC context already carries the router's `traceparent` (the executor client uses `otelhttp`).
Replace the four `SpanTrackEvent` markers with real child spans created from a tracer (`otel.Tracer("fission-executor")`):

- `coldstart/reserve` around capacity reservation (`gpm.go` / `pkg/executor/api.go`).
- `coldstart/fetch` around the fetcher specialize call (`gp_specialize.go`).
- `coldstart/specialize` around the env load.
- `coldstart/ready` around the pod-ready/patch wait.

On failure each span gets `RecordError(err)`, `SetStatus(codes.Error, reason)`, and a `coldstart.failure_reason` attribute; `MarkSpecializationFailure` sets the error status on the active cold-start span instead of only emitting an event.
A trace then shows exactly which phase failed, and the `fission.error.component` attribute (also set on the router span) lets a Collector tail-sampling policy key on failures.

### 4. Error-biased sampling (`pkg/utils/otel/errorsampler.go`)

Head sampling alone cannot "always sample errors" — the decision is made at span start, before the outcome is known.
The simplest robust option with **no Collector dependency**: make the root span `RecordOnly` (built but not auto-exported) and register a custom `SpanProcessor` that, in `OnEnd`, exports any span whose status is `Error` plus a configured ratio of the rest.
This is added in `pkg/utils/otel/provider.go`'s `InitProvider` alongside the existing `BatchSpanProcessor`, and is registered only when an OTLP exporter is configured, so it is inert when tracing is off.
For operators already running a Collector we document the cleaner alternative — head-sample at 100%, drop in the Collector with a `tail_sampling` policy keyed on `status.code == ERROR OR fission.error.component != ""` — and ship the `fission.error.component` attribute precisely so that policy is one line.

### 5. Diagnostics surface — folded into RFC-0017

A read-time "is function X invocable, and if not why?" surface (a `GET /v2/diag/function` returning live `{invocable, reason, readyEndpoints, busyEndpoints, lastColdStartError}`, and a `fission fn status` / consolidated `describe` that renders it) is delivered in RFC-0017 (planned) rather than here, for two reasons discovered during implementation:

- A condition-based rollup written from the executor would flap.
  `pkg/executor/util/status.go` deliberately writes only the *success* `Ready` transition and refuses to flip `Ready=False` on the cold-start hot path (transient image-pull / specialize churn would otherwise generate condition flapping that is more noise than signal).
  A `FunctionInvocable=False` write would reintroduce exactly that.
  A read-time endpoint queried on demand avoids writing churny conditions entirely.
- The CLI reaches Kubernetes directly, not the HMAC-gated internal executor port, so the query transport belongs with RFC-0017's CLI tooling.

Durable invocability reasons are already CLI-visible today via the existing `Ready` (executor) and `PackageReady` / `PackageBuildFailed` (buildermgr) conditions on `fission fn get`; RFC-0017 adds the live, consolidated view on top.

## Phased implementation

Each phase compiles and is CI-green on its own; phases 2–5 depend only on phase 1's header constants.

1. **Request-ID plumbing** — add `pkg/utils/correlation`; install the middleware on both listeners; set the request header on the function-pod request and the response header.
   No body change.
2. **Structured error body + attribution** — add `pkg/error/invocation.go`; return `routerError` from `transport.go`'s failure branches consuming `network.Adapter`; rewrite `getProxyErrorHandler` to emit the JSON body behind `ROUTER_STRUCTURED_ERRORS`; wire the `X-Fission-Debug` gate.
   Resolves #693.
3. **Executor/fetcher correlation + cold-start spans** — propagate the request-ID through `executor/client` and the fetcher; replace the cold-start events with child spans + error status.
4. **Error-biased sampling** — add `pkg/utils/otel/errorsampler.go`; register it in `InitProvider`.
5. **Diagnostics** — folded into RFC-0017 (see "Diagnostics surface" above for why).

## As implemented

Phases 1–4 are implemented as designed.
Concrete surface:

- **Phase 1** — `pkg/utils/correlation` (header constants + `ID`/`Middleware`/context helpers); `correlation.Middleware` wired inside the OTEL handler on both router listeners (`pkg/router/router.go`) and, for executor→fetcher correlation, on the executor handler (`pkg/executor/api.go`).
  The id rides to the function pod via the existing reverse proxy.
- **Phase 2** — `pkg/error/invocation.go` (`InvocationError`, `Component`, reason taxonomy); `RoundTrip` wraps executor-origin failures (`pkg/router/transport.go`); `getProxyErrorHandler` emits the JSON body gated by `ROUTER_STRUCTURED_ERRORS` (default on) with the `X-Fission-Debug` detail gate; `fission_invocation_failures_total{component,reason}`.
  Resolves #693.
  While implementing, `classifyFunctionError` was made robust via `errors.Is(err, syscall.ECONNREFUSED)` because `network.IsConnRefusedError` only matches `*url.Error`, which the proxy transport never produces.
- **Phase 3** — request id propagated on the executor RPCs (`pkg/executor/client`) and the fetcher specialize call (`pkg/fetcher/client`); a `coldstart/specialize` child span with error status + `coldstart.failure_reason` (`gp_specialize.go`), and `MarkSpecializationFailure` marks the active span errored (`gpm.go`).
- **Phase 4** — `errorBiasedSampler` + `errorExportProcessor` (`pkg/utils/otel/errorsampler.go`); `InitProvider` now pins the head sampler from `OTEL_TRACES_SAMPLER` (previously ignored — the Helm chart's documented `parentbased_traceidratio`@`0.1` finally takes effect) and force-exports error spans the base dropped.
  The whole mechanism is inert when no OTLP exporter is configured, so installs without tracing are unaffected.

## Backward compatibility & migration

- **Additive surface.**
  The response header, the `traceId`/`component`/`reason` fields, and the cold-start spans are all additive.
  Old clients ignore the header; nothing requires a coordinated upgrade.
- **The one behavior change — error-body format — is gated and reversible.**
  `ROUTER_STRUCTURED_ERRORS` (default `true`) selects the JSON body; setting it to `false` restores the exact legacy plain-text bytes.
  `Accept: application/json` negotiation is honored as well.
  Status codes are never changed.
  A release note documents the default.
- **Rolling upgrades are safe.**
  New headers ride outside the HMAC canonical string, and the internal-listener middleware runs inside the verifier, so signatures still validate.
  A mixed deployment where the executor or fetcher predates this RFC simply drops the request-ID — correlation degrades to router-only and never errors.
  Cold-start spans are additive and inert on an old executor.
- **Tracing disabled.**
  With no OTLP endpoint the no-op tracer yields a zero trace ID, `correlation.ID` falls back to a UUID, `traceId` is omitted, and the error-biased processor is not registered.

## Test strategy

- **Unit.**
  `pkg/utils/correlation` — inbound honored / UUID minted.
  `pkg/router/functionHandler_test.go` — a table driving `getProxyErrorHandler` with synthesized errors (`context.DeadlineExceeded`, a `*net.OpError{Op:"dial"}`, a `connection refused` `*net.OpError`, an `InvocationError` wrapping a 429 `ferror`) asserting the `{component, reason}` JSON and that no raw error leaks without `X-Fission-Debug`; `classifyFunctionError` covered directly.
  `pkg/error/invocation_test.go` — wrapping preserves `GetHTTPError` status via unwrap.
  `pkg/utils/correlation/correlation_test.go` — inbound honored / UUID minted / middleware propagation.
  `pkg/executor/client/client_test.go` — the executor RPCs carry `X-Fission-Request-ID`.
  `pkg/utils/otel/errorsampler_test.go` — an unsampled error span is force-exported; a sampled span is left to the batch processor; the base sampler honors `OTEL_TRACES_SAMPLER`.
- **Integration** (`test/integration/suites/common`, `//go:build integration`, in-process ephemeral servers).
  A `correlation_test.go`: invoke a healthy function and assert the response carries `X-Fission-Request-ID`; invoke a deliberately broken function (missing env) and assert the JSON error body attributes the failure (`component: "executor"`).

## Success metrics

New Prometheus series (low-cardinality labels, matching the existing namespace/name discipline):

- `fission_invocation_failures_total{component, reason}` — the headline attribution counter.
  "Good" = most failures land in `function` (user code), and a spike in `executor/specialization_failed` is an alertable platform problem.
- `fission_coldstart_phase_failures_total{phase}` and `fission_coldstart_phase_seconds{phase}` — pinpoint and time the failing cold-start phase, complementing the existing undifferentiated `fission_function_cold_start_errors_total`.

The portfolio-level proof: a single failed invocation has (1) a request-ID in the response and logs, (2) a sampled (error-biased) trace whose failing span names the component, and (3) a `fission_invocation_failures_total` increment whose `component` matches — all three correlating by request-ID.

## Open questions / risks

- **Request-ID vs trace-ID coupling.**
  Recommendation is to keep them separate (mint per invocation, attach trace ID); revisit only if a strong case for trace-derived IDs emerges.
- **Sampler default ambiguity.**
  `InitProvider` does not currently set an explicit sampler, so the base behavior depends on `OTEL_TRACES_SAMPLER`.
  Phase 4 must pin the base sampler explicitly so the error-biased wrapper is deterministic.
- **Record-on-error hot-path cost.**
  Making root spans `RecordOnly` builds span objects for unsampled traces; this is gated on an exporter being configured, with the Collector tail-sampling path documented for high-RPS deployments.
- **Streaming post-flush failures.**
  Once a 200 and the first byte are sent, the proxy error handler never runs (RFC-0008), so a mid-stream abort cannot carry a structured *body*.
  The request-ID is still in the already-sent response header and the abort is logged — the structured-attribution guarantee is header-only for streaming.
- **Body-format compatibility.**
  Some callers may scrape the plain-text body; the flag + `Accept` negotiation mitigate, but defaulting to JSON is a soft behavior change worth a release note.

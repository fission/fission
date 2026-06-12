# RFC-0008: Streaming Invocation Path

- Status: Proposed
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.N
- Requires: Kubernetes 1.32+ (floor; no bump). No new third-party deps — uses `net/http/httputil` already vendored.

## Summary

Add a per-function, opt-in streaming invocation mode so long-lived responses (SSE token streams,
chunked transfer, WebSocket upgrades) survive the router proxy instead of being killed by the single
total `FunctionTimeout` or corrupted by request replay. The router learns to split that one timeout
into an *idle* timeout and a *max duration*, flush incrementally, and refuse to retry a request once
the first byte has reached the client — all gated behind a new `FunctionSpec.Streaming` field that is
inert and off by default. It also promotes WebSocket to a first-class streaming protocol and retires
the existing out-of-band, Kubernetes-Event-based WebSocket keepalive (fetcher `/wsevent` endpoints +
poolmgr Event informers + `WebsocketFsvc`) in favor of a single router-driven mechanism that works
for every environment.

## Motivation

The router proxies every HTTP-triggered function through a single `httputil.ReverseProxy` built in
`pkg/router/functionHandler.go` (`fh.handler`, around line 482) with a custom `Transport` =
`RetryingRoundTripper`, a `Director`, an `ErrorHandler`, and a `ModifyResponse`. Two properties of
that path make it actively hostile to streaming workloads, which are now the dominant shape of
serverless AI traffic (an LLM token stream, an agent run, a long Server-Sent-Events feed):

- **One total timeout kills the stream.** `RetryingRoundTripper.setContext` (line 413) wraps every
  attempt in `context.WithTimeoutCause(req.Context(), roundTripper.funcTimeout, …)`, where
  `funcTimeout` is derived from the Function's `Spec.FunctionTimeout` (default
  `fv1.DEFAULT_FUNCTION_TIMEOUT = 60`, `pkg/apis/core/v1/const.go`). This is a *wall-clock* deadline
  on the whole exchange, so a chat completion that streams tokens for 90s is cancelled mid-flight at
  60s even though it was making steady progress.
- **Retry replays a request that already started streaming.** `RetryingRoundTripper.RoundTrip` (line
  146) loops up to `maxRetries`, re-dialing a fresh service URL and re-sending the body (kept open via
  `fakeCloseReadCloser`) on transient/dial errors. For a streaming `POST` (e.g. `/v1/chat/completions`)
  that has *already emitted* tokens to the client, re-issuing the request is semantically wrong — the
  client would see a second, duplicated stream concatenated onto the first.
- **No flush opt-in.** Go's `ReverseProxy` auto-flushes only when it detects
  `Content-Type: text/event-stream` *or* `ContentLength == -1`; the proxy here never sets
  `FlushInterval`, so a chunked/`auto` response that doesn't hit Go's heuristic buffers until close.

WebSocket is the revealing case. Fission **does** support WebSocket functions today, but through an
out-of-band machinery that exists precisely *because* the proxy path has no streaming concept:

- The router forwards the `Upgrade` (`pkg/router/util/util.go:207 IsWebsocketRequest`; `ReverseProxy`
  hijacks the connection natively since Go 1.12), but it does **not** keep the backing pod alive — the
  router, which is the only component actually holding the live socket, plays no part in the pod's
  liveness accounting.
- Instead, the **function runtime** must cooperate: the python GEVENT environment's
  `socket_tracker.py` calls the fetcher sidecar's `/wsevent/start` and `/wsevent/end` localhost
  endpoints (`cmd/fetcher/app/server.go:100`), which emit Kubernetes **Events** `WsConnectionStarted`
  / `NoActiveConnections` (`pkg/fetcher/fetcher.go:790,821`). The poolmgr runs two Event-watching
  informers (`gpm.go:163,170` → `WebsocketStartEventChecker` / `NoActiveConnectionEventChecker`,
  `gpm.go:711,742`) that flip a `fscache.WebsocketFsvc sync.Map`
  (`pkg/executor/fscache/functionServiceCache.go:64`), and the idle reaper special-cases that map
  (`pkg/executor/reaper/idle/idle.go:230`) so a pod with a live socket is not reaped.

This works, but it is fragile and narrow: it only exists for the one `WSGI_FRAMEWORK=GEVENT` python
environment, every other language would have to re-implement `socket_tracker.py`, it routes a
data-plane liveness signal through Kubernetes Events and two executor informers, and it is entirely
decoupled from the router that owns the connection. RFC-0008 makes WebSocket a first-class streaming
*protocol* and replaces this machinery with a **router-driven tap** (the router holds the service
tapped for the socket's lifetime — see Design), which works for every environment with no per-runtime
cooperation, no fetcher endpoints, no Events, and no `WebsocketFsvc` map.

None of this is theoretical for the AI work either: the AI gateway / MCP work tracked in RFC-0011
(Functions as MCP Tools) consumes this path directly — an MCP `tools/call` that streams partial
results, or an SSE transport, cannot run on Fission today without raising `FunctionTimeout`
cluster-wide (which then also delays detection of genuinely hung functions). We want streaming to be a
property of the *function*, not a global knob, and we want one streaming mechanism, not an SSE path
and a separate Event-driven WebSocket path.

## Goals

- A per-function, opt-in streaming mode declared on the CRD; default off, fully inert when unset.
- Split the proxy's single deadline into an **idle timeout** (reset on each byte written downstream)
  and a **max duration** (hard ceiling), applied only to streaming functions.
- Enable incremental flushing (`FlushInterval = -1`) for streaming functions so chunks reach the
  client as they are produced.
- **Never replay** a request once a response has started streaming to the client; the round tripper
  must short-circuit its retry loop after the first downstream byte.
- Pass WebSocket `Upgrade` through the internal listener (including its HMAC auth wrapper) without a
  premature idle kill, as a first-class `Protocol: websocket`.
- Keep poolmgr from reaping a pod that is in the middle of a live stream, via a router-driven tap.
- **Consolidate WebSocket onto the streaming path** and deprecate/remove the existing Event-based
  keepalive (fetcher `/wsevent`, poolmgr Event informers, `WebsocketFsvc`, env `socket_tracker.py`) so
  WebSocket works for every environment, not just the `WSGI_FRAMEWORK=GEVENT` python env.
- CLI flags on `fission fn create/update` to set streaming; documented Helm/router env interplay.
- The non-streaming path stays **byte-for-byte identical** — no behavior change for existing functions.

## Non-goals

- No Kubernetes floor bump; nothing here needs an API newer than 1.32.
- No bidirectional/multiplexed streaming framing invented by Fission (gRPC-Web, HTTP/2 server push).
  We forward what the function and `net/http` already speak (chunked, SSE, raw WebSocket).
- No change to the WebSocket *programming model*. The `main(ws, clients)` handler signature, the
  `gevent-ws`/`flask_sockets` runtime, and `WSGI_FRAMEWORK=GEVENT` stay; only the invisible Event-based
  keepalive plumbing is removed. Existing WebSocket functions keep working without source changes.
- No change to the executor *specialization* path or to how service URLs are resolved — only to
  liveness/idle accounting (`UnTapService`).
- No removal or repurposing of `FunctionTimeout`; it remains the request-completion deadline for
  non-streaming functions. Streaming functions deliberately do **not** inherit it as a ceiling (that
  total-wall-clock cap is what streaming escapes); their idle timeout governs and `maxDuration` is
  explicit-only.
- No automatic detection that turns a function streaming without opt-in. Auto-detection of
  `text/event-stream` is a heuristic the round tripper may *use to relax retries* (see Design), but it
  does not flip the CRD field.

## Design

### CRD: `FunctionSpec.Streaming` (`pkg/apis/core/v1/types.go`)

Add one `+optional` pointer field to `FunctionSpec` (sibling of `FunctionTimeout`/`IdleTimeout` at
line ~448), plus a new `StreamingConfig` type. Pointer + `omitempty` guarantees stored objects and
old clients round-trip unchanged (a nil `Streaming` is the existing behavior).

```go
// FunctionSpec, after FunctionTimeout / IdleTimeout:

// Streaming opts this function into the router's streaming invocation path:
// incremental flushing, an idle/max timeout split, and no request replay once
// the response has started. When nil (the default) the function uses the
// classic buffered, retry-on-transient-error proxy path with a single
// FunctionTimeout deadline.
// +optional
Streaming *StreamingConfig `json:"streaming,omitempty"`
```

```go
// StreamingProtocol selects how the router treats the upstream response.
// +kubebuilder:validation:Enum=auto;sse;chunked;websocket
type StreamingProtocol string

const (
    // StreamingAuto flushes immediately and lets the upstream decide the framing
    // (SSE, chunked, or a WebSocket Upgrade); the safe default.
    StreamingAuto      StreamingProtocol = "auto"
    StreamingSSE       StreamingProtocol = "sse"
    StreamingChunked   StreamingProtocol = "chunked"
    StreamingWebSocket StreamingProtocol = "websocket"
)

// StreamingConfig controls the router's streaming behavior for a function.
type StreamingConfig struct {
    // Enabled turns on the streaming path. A non-nil Streaming with Enabled=false
    // is equivalent to nil (classic path) and is allowed so callers can keep the
    // block while toggling.
    // +optional
    // +kubebuilder:default=true
    Enabled bool `json:"enabled"`

    // Protocol hints how the router proxies the response.
    // +optional
    // +kubebuilder:default=auto
    Protocol StreamingProtocol `json:"protocol,omitempty"`

    // IdleTimeoutSeconds is the maximum time the router waits without any bytes
    // flowing from the function before it aborts the stream. Reset on every chunk
    // written downstream. 0 means use the package default (60s).
    // +optional
    // +kubebuilder:validation:Minimum=0
    // +kubebuilder:default=60
    IdleTimeoutSeconds int `json:"idleTimeoutSeconds,omitempty"`

    // MaxDurationSeconds is an optional hard ceiling on total stream lifetime
    // regardless of activity. 0 (the default) means no ceiling — the idle timeout
    // governs. A streaming function does NOT inherit FunctionTimeout as a ceiling;
    // that total-wall-clock cap is exactly what streaming escapes.
    // +optional
    // +kubebuilder:validation:Minimum=0
    MaxDurationSeconds int `json:"maxDurationSeconds,omitempty"`
}
```

After editing `types.go`, regenerate: `make codegen` (clientset/listers/informers under
`pkg/generated/` + deepcopy) and `make generate-crds` (`crds/v1/`). The deepcopy for the new pointer
struct is produced by controller-gen; do not hand-edit `zz_generated_*.go`. HTTPTrigger/Function have
no admission webhook (CEL-only), so the `+kubebuilder:validation` markers above are the entire
validation story for the field — no `pkg/webhook/` change. Add a Go `StreamingConfig.Validate()` in
`pkg/apis/core/v1/validation.go` (idle/max non-negative; `max >= idle` when both non-zero) and call it
from `FunctionSpec.Validate`.

### Why the config lives on `FunctionSpec` (not the HTTPTrigger or Environment)

The natural candidates for the streaming config are `FunctionSpec`, `HTTPTriggerSpec`, and
`EnvironmentSpec`. We put it on `FunctionSpec` for four reasons, the first decisive:

1. **A function is invoked through many paths; only the function is common to all of them.** An
   HTTPTrigger is just *one* caller. The same function is also invoked over the **internal listener**
   (`/fission-function/<ns>/<name>`) by the timer, kubewatcher, MQ-trigger, canary, and — central to
   the AI story — RFC-0011's MCP server. None of those go through an HTTPTrigger. If streaming lived on
   the trigger, an MCP `tools/call` or a timer-driven invocation of a streaming function would silently
   take the buffered path and be cut at `FunctionTimeout` — exactly the workloads this RFC exists for.
   `functionHandler.handler` resolves `fh.function.Spec.Streaming` for **both** listeners, so one
   config covers every invocation path; a trigger field could only cover HTTP-triggered calls.
2. **Streaming is intrinsic to the function's code, not the route.** Whether the handler emits
   SSE/chunked/WebSocket is a property of how the function is *written* and is the same regardless of
   who calls it. The trigger governs *exposure* (path, method, CORS), not response shape.
3. **It belongs next to `FunctionTimeout`/`IdleTimeout`.** The streaming idle/max timeouts are the
   streaming analog of `FunctionTimeout`, which already lives on `FunctionSpec`.
4. **Canary correctness.** An HTTPTrigger with `FunctionWeights` references *two* backend functions; on
   the trigger they would share one streaming config even if only one streams. On the function each
   backend carries its own correct setting.

`EnvironmentSpec` is rejected for the same reason as RFC-0010's `InferenceConfig`: streaming is
per-function, not per-runtime (two functions on one node env can differ).

The one thing `FunctionSpec` cannot express is **the same function exposed via two routes with
different streaming** (e.g. `/chat` streams, `/chat-sync` buffers). This is not foreclosed: the
`resolveProxyPolicy(fn, …)` seam (see below) is exactly where a per-trigger override would slot in —
extend it to `resolveProxyPolicy(fn, trigger, …)` so an optional `HTTPTriggerSpec.Streaming` overrides
the function default. That is additive and backward compatible, so `FunctionSpec` is the right
**default home** (consistent across all invocation paths) without ruling out a future per-route
override. Deferred under YAGNI until a concrete per-route need appears (see Open questions).

### Router: idle/max timeout split + flush (`pkg/router/functionHandler.go`)

Thread the resolved streaming config into the per-request `RetryingRoundTripper`. Extend the struct
(line 87) and the constructor in `fh.handler` (line 474):

```go
RetryingRoundTripper struct {
    logger           logr.Logger
    funcHandler      *functionHandler
    funcTimeout      time.Duration // classic total deadline; max ceiling when streaming
    idleTimeout      time.Duration // 0 ⇒ not streaming
    streaming        bool
    responseStarted  atomic.Bool   // flips true once first byte reaches the client
    closeContextFunc *context.CancelFunc
    serviceURL       *url.URL
    urlFromCache     bool
    totalRetry       int
}
```

In `fh.handler`, resolve the config from `fh.function.Spec.Streaming` and pick timeouts:

```go
var idle time.Duration
streaming := fh.function.Spec.Streaming != nil && fh.function.Spec.Streaming.Enabled
maxDur := time.Duration(fnTimeout) * time.Second // classic: the existing funcTimeout
if streaming {
    sc := fh.function.Spec.Streaming
    idle = time.Duration(orDefault(sc.IdleTimeoutSeconds, DefaultStreamIdleSeconds)) * time.Second
    // Streaming never inherits FunctionTimeout as a ceiling — the idle timeout
    // governs and maxDur is explicit-only (0 = unlimited).
    maxDur = time.Duration(sc.MaxDurationSeconds) * time.Second
}
rrt := &RetryingRoundTripper{
    logger: fh.logger.WithName("roundtripper"), funcHandler: &fh,
    funcTimeout: maxDur, idleTimeout: idle, streaming: streaming,
}
```

`setContext` (line 413) changes so the per-attempt context deadline is the **idle** timeout for
streaming functions (re-armed by the flush hook below) and the **max** deadline otherwise:

```go
func (rt *RetryingRoundTripper) setContext(req *http.Request) *http.Request {
    if rt.closeContextFunc != nil {
        (*rt.closeContextFunc)()
    }
    d := rt.funcTimeout
    if rt.streaming && rt.idleTimeout > 0 {
        d = rt.idleTimeout
    }
    ctx, cancel := context.WithTimeoutCause(req.Context(), d,
        fmt.Errorf("roundtripper %s timeout (%s) exceeded", timeoutKind(rt), d))
    rt.closeContextFunc = &cancel
    return req.WithContext(ctx)
}
```

The hard `maxDur` ceiling for streaming is layered as an *outer* context established in `fh.handler`
(a `context.WithTimeout` on `request.Context()` when `maxDur > 0`), so the per-chunk idle context is a
child of it. The existing `defer rrt.closeContext()` block (line 492) — which must stay in the defer
to avoid the `golang/go#28239` truncation/panic noted in the source comment — is preserved; we add the
outer cancel to the same defer.

Flushing and idle re-arming go on the `ReverseProxy` (line 482):

```go
proxy := &httputil.ReverseProxy{
    Director:     director,
    Transport:    rrt,
    ErrorHandler: fh.getProxyErrorHandler(start, rrt),
    ModifyResponse: func(resp *http.Response) error {
        // mark the stream as started for the no-replay guard (see below) BEFORE
        // returning; ReverseProxy copies the body to the client after this returns.
        if streaming || isAutoStreamResponse(resp) {
            rrt.responseStarted.Store(true)
        }
        go fh.collectFunctionMetric(start, rrt, request, resp)
        return nil
    },
}
if streaming {
    proxy.FlushInterval = -1 // flush every write immediately
}
```

`FlushInterval = -1` makes `ReverseProxy` flush after each `Write`, which is exactly the SSE / chunked
semantics we want and which Go also auto-selects for `text/event-stream`. We set it explicitly so it
applies to `chunked` and `auto` (non-SSE) responses too. The idle-deadline re-arm is achieved by
wrapping the upstream `resp.Body` in `ModifyResponse` with a small `readCloser` that calls a
"saw activity" callback resetting the idle context on each successful `Read`; because
`context.WithTimeout` deadlines are not extensible in place, the wrapper instead swaps in a fresh
`time.AfterFunc`-backed cancel keyed off `rt.idleTimeout` on each read, and `closeContext` cancels the
live timer. (`isAutoStreamResponse` returns true for `Content-Type: text/event-stream` or
`ContentLength == -1`, so an `auto` function that emits SSE without declaring it still gets the
no-replay guard.)

### Router: no replay after first byte

`RetryingRoundTripper.RoundTrip` (line 146) gates its retry loop on `responseStarted`. The flip is set
in `ModifyResponse` (which `ReverseProxy` invokes *before* it starts copying the body downstream), so
by the time any transport-level error could surface during body copy, the guard is already true. Two
edits:

1. At the top of each loop iteration, bail out of retrying if a stream has started:
   ```go
   if rt.responseStarted.Load() {
       // Response already (partially) delivered to the client; replaying the
       // request would duplicate the stream. Surface the error as-is.
       return resp, err
   }
   ```
2. For streaming functions specifically, set `maxRetries` effectively to 1 for the *post-headers*
   phase: the existing cache-miss → executor → dial retries (which happen *before* any byte is sent)
   are still valuable and remain in force; only retries that would re-issue an already-started body are
   suppressed. Because `responseStarted` can only be true after `ModifyResponse`, this distinction
   falls out naturally — pre-response dial errors still retry, post-response errors do not.

This makes streaming `POST`s correct without changing the non-streaming retry behavior at all (for a
classic function `responseStarted` stays false until the buffered response is fully proxied, and the
loop is unchanged).

### WebSocket support (internal listener + HMAC)

WebSocket becomes a first-class `Protocol: websocket` (the existing `Protocol: auto` also handles a
function that upgrades without declaring it). `httputil.ReverseProxy` has handled `Connection:
Upgrade` / `Upgrade: websocket` natively since Go 1.12 — when it sees the upgrade headers it hijacks
the connection and pipes bytes bidirectionally, bypassing `FlushInterval`/`ModifyResponse` — and the
router already detects the upgrade via `pkg/router/util/util.go:207 IsWebsocketRequest`. The streaming
path reuses that detection so a `websocket`/`auto` function gets the no-replay guard, the idle (not
total) deadline, and the router-driven tap below. Three things must hold:

- **Auth pass-through on the internal listener.** Per the router listener split
  (post-GHSA-3g33-6vg6-27m8), function invocations land on the internal listener (`svc/router-internal`,
  port 8889) wrapped by `pkg/auth/hmac.ServiceVerifier`. The verifier must forward the hop-by-hop
  `Upgrade`/`Connection` headers untouched and must not buffer or wrap the `http.ResponseWriter` in a
  way that hides the `http.Hijacker` interface — otherwise the upgrade fails. We add a unit test
  asserting the verifier's wrapped `ResponseWriter` still satisfies `http.Hijacker`, and that signing
  is computed over headers/body only (an `Upgrade` request has no body to replay).
- **Idle, not total, deadline.** For `Protocol: websocket` (or `auto` that upgrades), the per-attempt
  context must use the idle timeout, never a total ceiling, or a long-idle but healthy socket dies.
  Since the hijacked connection escapes `ReverseProxy`'s normal read loop, the round tripper detects
  the `101 Switching Protocols` response in `ModifyResponse`, sets `responseStarted`, and disarms the
  context deadline entirely (relying on TCP keep-alive + the function's own liveness) when
  `MaxDurationSeconds == 0`. With a non-zero `MaxDurationSeconds` the outer ceiling still applies.
- **Router-driven pod keepalive (replaces the Event side-channel).** A hijacked WebSocket holds the
  pod busy for the whole socket lifetime; the router keeps the service tapped for exactly that
  lifetime via the poolmgr keepalive below, so a `websocket`/`auto` stream needs *none* of the
  existing `socket_tracker.py` → `/wsevent` → `WsConnectionStarted`/`NoActiveConnections` Event →
  `WebsocketStartEventChecker` → `WebsocketFsvc` machinery. That machinery is deprecated and removed by
  this RFC — see "Retiring the Event-based WebSocket keepalive".

### Retiring the Event-based WebSocket keepalive

**Why this is generic for every environment.** The keepalive moves to the HTTP/router layer, which is
language-agnostic: the router hijacks the `Upgrade` and keeps the pod tapped for as long as it holds
that proxied connection (heartbeat re-tap; untap on socket close), regardless of what runtime is on the
other end. The function never has to announce its own liveness. This is not just a simplification of
the python path — it is the *first time WebSocket works for any environment*: today python is the only
env that supports WebSocket precisely because it is the only one shipping `socket_tracker.py`. After
this RFC, a node, go, or any HTTP runtime that accepts an upgrade is supported with **zero** keepalive
code. The only env-side WebSocket code that remains is the optional `clients` broadcast set (the
`clients` arg to `main(ws, clients)`), which is an application feature, not keepalive, and is unrelated
to the fetcher.

Once the router-driven tap (next section) keeps a pod alive for a hijacked WebSocket, the entire
Event-driven keepalive is dead weight and is removed across two minor releases (per the `rfc/README.md`
deprecation policy), because it spans two repos:

- **`fission/fission` (this repo), removed in phase 8:** the fetcher endpoints `WsStartHandler` /
  `WsEndHandler` and their routes (`pkg/fetcher/fetcher.go:790,821`, `cmd/fetcher/app/server.go:100`);
  the poolmgr `WebsocketStartEventChecker` / `NoActiveConnectionEventChecker` and their launch sites
  (`gpm.go:163,170,711,742`); the `fscache.WebsocketFsvc` map and its idle-reaper special-case
  (`functionServiceCache.go:64`, `idle/idle.go:230`); the `GetInformerEventChecker` Event-watch helper
  if it has no other caller. The `IsWebsocketRequest` detector is **kept** (the streaming path uses
  it). The `AllowedFunctionsPerContainer: infinite` escape hatch is **kept** — it is an orthogonal
  concurrency setting, not a WebSocket mechanism, though it is no longer *required* for WebSocket pods.
- **`fission/environments` (separate repo), coordinated:** the python `socket_tracker.py`'s
  `/wsevent/start|end` calls and the `monitor()` greenlet that drives them are deleted; the env keeps
  `WSGI_FRAMEWORK=GEVENT` + `gevent-ws` (that is how you *write* a socket app) and the `clients`
  broadcast set, but no longer talks to the fetcher. The `main(ws, clients)` programming model in
  `examples/python/websocket` is unchanged — only the invisible keepalive plumbing goes away. Other
  environments that want WebSocket need only accept the upgrade in their runtime; they require none of
  this plumbing, which is the point of moving keepalive to the router.

Removal is sequenced so a cluster mid-upgrade is never broken: during the deprecation window the
executor honors **both** the new router tap and a still-present `WebsocketFsvc` entry (logical OR in
the idle reaper), and the fetcher `/wsevent` endpoints become no-ops that log a deprecation warning
before they are deleted. A cluster running an old environment image (still calling `/wsevent`) keeps
working against a new control plane, and a new environment image works against an old control plane
(the router tap holds the pod regardless of Events).

### Backpressure / cancellation correctness

The existing defer comment at line 492 is load-bearing: closing the round tripper context too early
truncates the client's body with a spurious "context canceled". The streaming changes preserve that
contract — the idle timer and the outer max-duration cancel are both released only in the same deferred
block, after `proxy.ServeHTTP` returns. Client disconnects propagate correctly because the idle/outer
contexts are children of `request.Context()`, which `net/http` cancels when the client goes away; that
cancellation reaches the upstream `RoundTrip` and tears down the function connection (no leaked
goroutine, no orphaned upstream stream). This is the same parent-context mechanism the current code
already relies on for early client abort (see the comment at `setContext`, line 417).

### Executor / keepalive interplay (poolmgr)

Today, for poolmgr functions, `RoundTrip` registers a deferred `unTapService` (line 250) that fires a
goroutine calling `fh.unTapService` → `executor.UnTapService` *as soon as `RoundTrip` returns*. For a
streaming response `RoundTrip` returns when headers are received, i.e. **while the body is still
streaming**, so the pod can be marked idle and become eligible for idle eviction
(`Spec.IdleTimeout`/`gpm` recycling, `GenericPoolManager.UnTapService` in
`pkg/executor/executortype/poolmgr/gpm.go:234`) mid-stream.

Fix: for streaming functions, move the `unTapService` call from the `RoundTrip` defer to the end of the
stream — i.e. fire it from the body wrapper's `Close()` (when the client finishes reading or
disconnects), not from `RoundTrip` return. Concretely, the deferred `go unTapService(...)` block at
line 250 becomes conditional: when `rt.streaming`, the untap closure is attached to the wrapped
`resp.Body`'s `Close` instead, so the service stays "tapped" (busy) for the full lifetime of the
stream. For long streams that outlive a single tap TTL, the body wrapper additionally re-issues
`TapService` (the existing `fh.tapService`, line 434) on a heartbeat (every `idleTimeout/2`) to keep
the executor's keepalive fresh, mirroring how an in-flight request would normally hold the pod. This
guarantees a pod serving a live token stream is never reaped out from under it.

For a hijacked WebSocket the "body wrapper `Close`" is the socket teardown (client close, function
close, or context cancel), so the same tap-until-close + heartbeat logic holds the pod for the socket
lifetime — this is the router-side replacement for the `WebsocketFsvc` reaper skip, and it is why the
Event machinery can be removed. Because the tap is keyed on the actual proxied connection rather than
on a runtime cooperating via Events, it is correct for every environment and self-clears on disconnect
(no `NoActiveConnections` round-trip needed).

### CLI (`pkg/fission-cli`)

Add streaming flags to `fission fn create` and `fission fn update` (defined in
`pkg/fission-cli/flag/flag.go`, keyed in `pkg/fission-cli/cmd/flagkey`, registered in
`pkg/fission-cli/cmd/function/command.go` alongside the existing `FnExecutionTimeout` / `FnIdleTimeout`
entries):

- `--streaming` (Bool, default false) — set `Spec.Streaming = &fv1.StreamingConfig{Enabled: true, …}`.
- `--streaming-protocol` (String, default `auto`, validated against the Enum).
- `--streaming-idle-timeout` (Int seconds, default 60) → `IdleTimeoutSeconds`.
- `--streaming-max-duration` (Int seconds, default 0) → `MaxDurationSeconds`.

In `create.go` (the `fv1.FunctionSpec{…}` literal at line 244), populate `Streaming` only when
`--streaming` is set, so specs without the flag serialize with no `streaming` key (clean round-trip and
clean spec diffs). `update.go` / `update_container.go` set the field under `input.IsSet(...)` guards,
exactly like the existing `FnIdleTimeout` handling at `update_container.go:117`. `spec.go`'s validation
warnings get a note that `MaxDurationSeconds`, when 0 and `FunctionTimeout > 0`, inherits
`FunctionTimeout`.

### Helm / router env (`charts/fission-all`)

No new required Helm value. Two interactions to document in `values.yaml` comments:

- `ROUTER_ROUND_TRIP_TIMEOUT` (`router.go:215`, the `tsRoundTripperParams.timeout` used as the dial /
  per-attempt *connect* timeout and backoff seed) is unchanged and still applies to the *connection*
  phase of streaming functions; it does **not** cap stream duration. The package default
  `DefaultStreamIdleSeconds` is overridable via a new optional `ROUTER_STREAM_IDLE_TIMEOUT` env
  (parsed in `router.go` next to the other `ROUTER_*` durations) so operators can set a cluster-wide
  idle floor; a function's `IdleTimeoutSeconds` takes precedence when set.
- `ROUTER_ROUND_TRIP_DISABLE_KEEP_ALIVE` (default disables connection reuse) is irrelevant to a single
  long-lived stream but should be noted as orthogonal.

## Alternatives considered

- **Raise `FunctionTimeout` cluster-wide.** The status quo workaround. Rejected: it's a global knob
  that delays detection of genuinely hung non-streaming functions, and it still doesn't fix retry
  replay or buffering. Streaming must be per-function.
- **A separate streaming router subsystem / new `--streamPort` listener.** Rejected: invocation
  routing, caching, trigger resolution, and the internal-listener HMAC already live in `pkg/router`;
  forking a second proxy duplicates all of it and the listener-split security boundary. The change is
  localized to `functionHandler.go`.
- **Auto-detect streaming from `Content-Type` only, no CRD field.** Rejected as the *primary*
  mechanism: detection can't be known before the response headers arrive, so the *request*-side
  decisions (no-replay intent, idle vs. total deadline, keep-pod-tapped) would be made too late or
  guessed. We *do* use `text/event-stream`/`ContentLength==-1` detection as a secondary safety net for
  the no-replay guard, but the timeouts and pod-keepalive are driven by the explicit field.
- **HTTP/2 server-push or gRPC-Web framing in the router.** Rejected (non-goal): large surface, and
  the function runtimes don't speak it; chunked/SSE/WebSocket cover the real workloads.
- **Per-HTTPTrigger (or per-Environment) streaming flag instead of per-Function.** Rejected as the
  *home* for the config — see "Why the config lives on `FunctionSpec`" in Design for the full rationale.
  In short: a function is invoked through many paths (the internal listener used by timer / MQ /
  kubewatcher / canary / RFC-0011 MCP, not just HTTPTriggers), and only the function is common to all
  of them; streaming is intrinsic to the function's code; and a trigger field would split a canary's
  two backends. A per-trigger *override* on top of the function default remains a clean future
  extension via the `resolveProxyPolicy` seam (Open questions), so this choice is not foreclosed.
- **Keep the Event-based WebSocket keepalive and just add SSE/chunked.** Rejected: it would leave two
  parallel streaming mechanisms — a router-driven one for SSE/chunked and a runtime-cooperating,
  Event-driven one for WebSocket — and the per-language `socket_tracker.py` burden that has kept
  WebSocket support effectively python-only. Since the router-driven tap already has to exist for
  SSE/chunked, extending it to hijacked WebSockets is nearly free and lets the whole Event side-channel
  be deleted, which is a net reduction in moving parts (fetcher endpoints, two executor informers, a
  `sync.Map`, a reaper special-case, and a per-env tracker).
- **Generalize the Event signal to all environments instead of removing it.** Rejected: it doubles down
  on routing a data-plane liveness signal through Kubernetes Events and executor informers, and still
  requires every runtime to call the fetcher. The component that owns the live connection (the router)
  already knows the socket is up; deriving liveness from the connection it is holding is simpler and
  has no per-runtime contract.

## Backward compatibility

- `Streaming` is `+optional` and a pointer; nil ⇒ the exact current behavior. Stored Functions and old
  clients round-trip unchanged (CRD additive field).
- The non-streaming code path is byte-identical: `streaming` is false, `FlushInterval` is left at its
  zero value, `responseStarted` only flips after a fully-proxied buffered response, the retry loop and
  the single `funcTimeout` deadline behave exactly as before, and the poolmgr `unTapService` still
  fires on `RoundTrip` return.
- New CLI flags default to off; existing `fission fn create/update` invocations produce identical specs.
- No Helm value is required; `ROUTER_STREAM_IDLE_TIMEOUT` is optional with a built-in default.
- **WebSocket migration is the one deprecation.** The Event-based keepalive is deprecated, not removed
  on day one, and follows the ≥2-minor-release policy. During the window the idle reaper honors the
  router tap **OR** a legacy `WebsocketFsvc` entry, and the fetcher `/wsevent/*` routes remain as
  warn-and-no-op endpoints, so all four skew combinations work: {old, new} control plane × {old, new}
  environment image. Existing WebSocket functions keep running unchanged throughout; the
  `main(ws, clients)` programming model never changes. The cross-repo `fission/environments` change
  (drop `socket_tracker.py`) ships in step but is itself backward compatible (a new env image needs no
  `/wsevent` endpoint, an old one tolerates the no-op). Removal of the dead code (phase 8) lands only
  after the policy window.

## Rollout phases (one PR each, bisectable)

1. **CRD types + codegen (compiles, inert).** Add `StreamingConfig` / `StreamingProtocol` /
   `FunctionSpec.Streaming` to `types.go` with kubebuilder markers; `StreamingConfig.Validate` +
   `FunctionSpec.Validate` call; `make codegen && make generate-crds`. Nothing reads the field yet.
   Validation unit tests.
2. **Router idle/max timeout split + flush.** Thread `Streaming` into `RetryingRoundTripper`; implement
   `setContext` idle-vs-max selection, the outer max-duration ceiling, `FlushInterval = -1`, and the
   body-read idle re-arm. Streaming functions now stream and stop being killed at 60s; classic path
   untouched. Unit tests with a chunk-emitting `httptest` upstream.
3. **No-replay-after-first-byte.** Add `responseStarted` + the `ModifyResponse` flip + the
   `RoundTrip` short-circuit + `isAutoStreamResponse` safety net. Unit test: an upstream that streams N
   bytes then drops the connection is *not* replayed.
4. **Poolmgr keepalive.** Move `unTapService` to stream end (body `Close`) for streaming functions +
   the `idleTimeout/2` re-tap heartbeat. Test against the poolmgr fake that the service stays tapped
   for the stream's lifetime and is untapped exactly once at close.
5. **WebSocket via the router tap.** Reuse `IsWebsocketRequest`; verify/secure `Upgrade` pass-through
   on the internal listener + HMAC verifier (`Hijacker` preserved), `101` handling that disarms the
   deadline, and the body-`Close` = socket-teardown tap so a hijacked socket holds the pod with **no**
   reliance on `WebsocketFsvc`. Unit test the verifier and the tap-held-until-close behavior;
   integration test deferred to phase 7.
6. **CLI.** `--streaming`, `--streaming-protocol`, `--streaming-idle-timeout`,
   `--streaming-max-duration` on create/update; spec round-trip; help text. Optional
   `ROUTER_STREAM_IDLE_TIMEOUT` env + Helm `values.yaml` doc comment.
7. **Docs + integration test.** A streaming echo function plus a chunk-timing client that asserts
   incremental delivery (see below), and a WebSocket echo function proving the pod survives an idle
   socket past `FunctionTimeout` **without** `socket_tracker.py` / `/wsevent`. Update
   `examples/python/websocket` + env docs to present `Streaming` as the recommended way and drop the
   `WSGI_FRAMEWORK` keepalive note. Cross-link RFC-0011.
8. **Deprecate the Event keepalive (begins the ≥2-minor window).** Idle reaper honors `router-tap OR
   WebsocketFsvc`; fetcher `/wsevent/*` become warn-and-no-op; coordinate the `fission/environments`
   PR dropping `socket_tracker.py`'s `/wsevent` calls. No behavior change for existing users.
9. **Remove the dead code (after the window).** Delete `WsStartHandler`/`WsEndHandler` + routes,
   `WebsocketStartEventChecker`/`NoActiveConnectionEventChecker` + launch sites, `fscache.WebsocketFsvc`
   + its reaper special-case, and `GetInformerEventChecker` if unused. Keep `IsWebsocketRequest` and
   `AllowedFunctionsPerContainer: infinite`.

## Verification / test plan

- `make codegen && make generate-crds` clean; `make license-check` (new files need the SPDX header).
- **Unit (testify, `t.Context()`):**
  - `StreamingConfig.Validate` table-driven (idle/max non-negative, `max >= idle`).
  - Idle/max selection in `setContext`: streaming uses idle, classic uses total; verify the outer
    ceiling cancels at `MaxDurationSeconds`.
  - No-replay: an `httptest.Server` that writes a few chunks then RSTs the connection must surface the
    error to the client *without* a second request (assert the handler was hit exactly once via an
    atomic counter); a classic function on a dial error still retries.
  - HMAC verifier preserves `http.Hijacker` and forwards `Upgrade`/`Connection`.
  - Router tap held for a hijacked socket's lifetime: against the poolmgr fake, a WebSocket function's
    service stays tapped while the socket is open and is untapped exactly once on `Close`, with **no**
    `WebsocketFsvc` entry written — proving the Event side-channel is no longer needed.
  - Idle-reaper skew guard (deprecation window): a pod is not reaped when the router tap is held even
    if `WebsocketFsvc` is absent, and (legacy path) still not reaped when only `WebsocketFsvc` is set.
- **envtest:** Function CRUD with `Streaming` set round-trips; the Enum marker rejects an invalid
  `protocol`; nil `Streaming` stays absent on read-back.
- **Integration (`test/integration/suites/common/`, build tag `integration`):** deploy a streaming
  echo function (env handler that writes a line, flushes, sleeps, repeats). Drive it through the
  framework's `Router(t)` client (auto-routes `/fission-function/...` to the internal listener 8889),
  reading the response body incrementally and recording arrival timestamps; assert that inter-chunk
  gaps reflect the function's sleeps (i.e. chunk *i+1* arrives meaningfully after chunk *i*, proving
  the router flushed rather than buffered). A second case sets `--streaming-max-duration` low and
  asserts the stream is cut at the ceiling. `t.Skip` when the runtime image env var is unset, per the
  suite convention.
- **Integration (WebSocket):** the `examples/python/websocket` `main(ws, clients)` echo function,
  invoked over the internal listener, with the **new** env image (no `socket_tracker.py`); assert the
  socket echoes, stays open while idle past `FunctionTimeout`, and the pod is not reaped — confirming
  the router tap replaces the Event keepalive end-to-end. `t.Skip` when the python image env var is
  unset.
- **Manual:** an SSE function whose stream runs past `FunctionTimeout` without being cut; a mixed-skew
  check (old env image still calling `/wsevent` against a new control plane) during the deprecation
  window.

## Open questions

- **Per-trigger streaming override.** The config lives on `FunctionSpec` (see Design for why), which
  cannot express the same function streaming on one route and buffering on another. Should we add an
  optional `HTTPTriggerSpec.Streaming` that overrides the function default, resolved by extending
  `resolveProxyPolicy(fn, …)` to `resolveProxyPolicy(fn, trigger, …)`? Proposed: defer (YAGNI) — no
  concrete per-route use case yet; it is additive and backward compatible whenever one appears, so the
  `FunctionSpec` default does not foreclose it.
- Should `Protocol: auto` ever *disable* flushing for clearly non-streaming responses (e.g. a small
  JSON body) to avoid the tiny overhead of per-write flushes? Proposed: no — `FlushInterval = -1` on a
  short buffered body is negligible, and keeping one code path is simpler.
- Default `IdleTimeoutSeconds`: reuse `DEFAULT_FUNCTION_TIMEOUT` (60) for familiarity, or pick a
  smaller streaming-specific default? Proposed: 60, matching the field's `+kubebuilder:default`.
- Should the idle-timeout re-tap heartbeat (phase 4) be `idleTimeout/2` or tied to the executor's own
  keepalive TTL? Proposed: `idleTimeout/2`, revisited if it proves chatty against the executor.
- Interplay with canary HTTPTriggers (`FunctionReferenceTypeFunctionWeights`): a streamed response
  can't be re-weighted mid-stream. Proposed: the weight is chosen once at `handler` entry (already the
  case), so streaming is consistent with the chosen backend; document that retries-across-backends are
  off for streaming, which is already implied by the no-replay guard.
- WebSocket Event-keepalive removal spans `fission/fission` + `fission/environments`. Proposed: land
  the control-plane deprecation (phase 8) and the env `socket_tracker.py` drop in the same release, but
  gate the dead-code removal (phase 9) on the ≥2-minor window measured from the release that ships the
  router tap — so users on an N-2 env image are never stranded. Confirm no non-WebSocket caller of
  `GetInformerEventChecker` exists before deleting it.
- Should `newdeploy`/`container` WebSocket functions (not just poolmgr) get the same tap-based reaper
  exemption? Today only poolmgr idle-reaps pods; `newdeploy` scales via HPA. Proposed: the router tap
  is poolmgr-specific (it is the only path with `UnTapService`); for `newdeploy` a live socket holds a
  request in flight, which the HPA already accounts for — document, no extra mechanism.
- Cross-reference RFC-0011 (Functions as MCP Tools / AI Gateway): the MCP SSE/streamable-HTTP transport
  is the primary consumer of this path; confirm its handler sets `Streaming.Protocol = sse`/`auto` on
  the generated function so MCP tool calls stream without per-cluster timeout tuning.

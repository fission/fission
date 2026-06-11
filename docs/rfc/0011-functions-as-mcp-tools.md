# RFC-0011: Functions as MCP Tools & AI Gateway

- Status: Partially implemented — Part A (Functions as MCP tools) shipped in [#3483](https://github.com/fission/fission/pull/3483) (merged 2026-06-10); Part B (AI gateway) deferred.
- Tracking issue: TBD
- Supersedes: —
- Targets: Fission v1.N
- Requires: Kubernetes 1.32+ (floor); RFC-0008 streaming invocation path (Streamable HTTP transport reuse). No new k8s features; Part B's embedding/model backends are user-supplied Fission functions or external endpoints.

## Summary

Expose Fission functions as Model Context Protocol (MCP) tools so any LLM agent can discover and
call them over a standard transport, and add an opt-in router AI-gateway middleware chain (semantic
response cache, token/cost metering, model routing/fallback) for functions that front LLMs. Part A
(MCP) is the self-contained, higher-value piece and ships first; Part B (gateway) lands behind
feature flags in later phases. Both are additive and off by default.

## Motivation

A Fission function is already a callable unit: an HTTP endpoint plus CRD metadata (`Function`,
its `Environment`, its `Package`). MCP (`modelcontextprotocol.io`) is the emerging open standard for
exposing tools to LLM agents — an agent connects to an MCP server, lists tools (each with a name,
description, and JSON-Schema input), and calls them. Today a Fission operator who wants their
functions usable by an agent must hand-write and host a separate MCP server that re-declares each
function's schema and proxies calls, duplicating the trigger lifecycle and the router's auth model.
Fission already owns the function inventory, the internal invocation path
(`/fission-function/<ns>/<name>` on the HMAC-gated internal listener, per the post-GHSA-3g33-6vg6-27m8
listener split), and a reconciler pattern for watching CRDs. A first-class MCP subsystem closes that
gap with no new data-plane surface.

Separately, a large and growing class of Fission functions are thin wrappers in front of an LLM
(OpenAI-compatible chat endpoints, self-hosted vLLM, Bedrock proxies). These share three recurring
operational needs that today every team re-implements inside function code: caching semantically
equivalent prompts to cut cost/latency, metering token usage per tenant, and failing over to a
backup model. The router already runs a composable middleware chain (`pkg/router/auth.go` shows the
`next.ServeHTTP` mux-middleware pattern) and already exports Prometheus metrics
(`pkg/router/metrics.go`), so an opt-in AI-gateway middleware is a natural, low-risk addition — but
it is strictly less self-contained than Part A (it needs an embedding backend, a cache-store
decision, and streaming-aware buffering), so it is explicitly scoped after the MCP work.

Why now: RFC-0008 introduces the Streamable HTTP / SSE invocation path that both MCP's Streamable
HTTP transport and the gateway's stream-replay cache depend on. Building MCP and the gateway on top
of that path (rather than inventing a second streaming mechanism) is the reason these belong in one
RFC sequenced after RFC-0008.

## Goals

- Auto-expose opted-in functions as MCP tools over a standard MCP server (`--mcpPort` subsystem in
  `fission-bundle`), with the tool list kept live by watching `Function` CRDs.
- Source each tool's name/description/input-schema from the `Function` CRD (a new optional
  `FunctionSpec.Tool` field), so the tool contract is declarative and round-trips.
- Proxy MCP tool calls to the existing router internal endpoint with the existing HMAC
  `ServiceRouterInternal` signing — no new invocation path, no new data-plane trust boundary.
- Per-namespace / per-tool authorization for agents connecting to the MCP endpoint.
- Add an opt-in, per-trigger AI-gateway middleware (semantic cache, token metering + budget,
  model fallback) composable with the existing router middleware chain, gated behind a feature flag
  and shipped after Part A.
- Keep everything additive and off by default; no Kubernetes floor bump; no change for existing
  users who don't opt in.

## Non-goals

- Fission will **not** become an agent runtime or host the LLM itself — it exposes functions as
  tools and (optionally) proxies/caches LLM traffic; the model lives in a user function or an
  external endpoint.
- No MCP features beyond `tools` in this RFC (no `resources`, `prompts`, or `sampling` capabilities;
  they can follow once the transport and discovery loop are proven).
- No bundled embedding model or vector database. The semantic cache's embedding backend is a
  user-supplied Fission function or external endpoint; the cache store is in-process per replica in
  v1 (a shared store is an open question, see below).
- No Kubernetes floor bump and no new privileged data path; MCP reuses the router internal listener.
- Part B does **not** try to be a general API gateway (rate-limiting, WAF, arbitrary transforms) —
  only the three LLM-specific middlewares named above.

## Design

The two parts are independent at the package level: Part A is a new `pkg/mcp` subsystem; Part B is
new middleware under `pkg/router/`. They share only the new `Tool`/gateway CRD fields' codegen pass
and the RFC-0008 streaming primitives.

### CRD changes (`pkg/apis/core/v1/types.go`)

Part A adds an optional `Tool` field to `FunctionSpec` (verified: `FunctionSpec` is the struct at
`types.go:412`; all additions below are `+optional` and json-`omitempty` so stored objects and old
clients round-trip):

```go
// Tool, when set with ExposeAsMCP=true, advertises this function as a Model
// Context Protocol tool on the fission-bundle --mcpPort server. The MCP server
// watches Function CRDs and hot-updates its tool list from this field.
// +optional
Tool *ToolConfig `json:"tool,omitempty"`
```

```go
// ToolConfig declares how a Function is exposed as an MCP tool.
type ToolConfig struct {
    // ExposeAsMCP gates advertisement. False (default) means the function is
    // never listed as a tool even if the rest of this struct is populated.
    // +optional
    ExposeAsMCP bool `json:"exposeAsMCP,omitempty"`

    // Description is the human/agent-facing tool description shown in the MCP
    // tools/list response. Required when ExposeAsMCP is true.
    // +optional
    Description string `json:"description,omitempty"`

    // InputSchema is the JSON Schema (draft 2020-12) for the tool's arguments,
    // surfaced verbatim as the MCP tool inputSchema. Stored as raw JSON so the
    // CRD does not constrain the schema shape. When empty the tool advertises
    // an open object schema ({"type":"object"}).
    // +optional
    // +kubebuilder:pruning:PreserveUnknownFields
    InputSchema *apiextensionsv1.JSON `json:"inputSchema,omitempty"`

    // ToolName overrides the advertised tool name. Defaults to the Function's
    // metadata.name. Must be a valid MCP tool name (DNS1123-label-ish).
    // +optional
    // +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_-]{1,64}$`
    ToolName string `json:"toolName,omitempty"`
}
```

`InputSchema` uses `k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1.JSON` +
`+kubebuilder:pruning:PreserveUnknownFields` so arbitrary JSON Schema survives apiserver pruning
(the same technique CRDs use for embedded raw config). A Go `ToolConfig.Validate()` is added in
`pkg/apis/core/v1/validation.go` and called from `FunctionSpec.Validate`: it requires `Description`
when `ExposeAsMCP` is true and, if `InputSchema` is set, checks it parses as a JSON object with a
`"type"` key (a cheap structural check; full JSON-Schema meta-validation is left to the agent). CEL
cannot parse arbitrary JSON Schema, so this stays a Go-side check; `Function` has no admission
webhook for this path, matching how other non-CEL-expressible rules are validated in-process.

Part B adds an optional `AIGateway` field to `HTTPTriggerSpec` (verified at `types.go:760`; the
existing `RouteConfig`/`CorsConfig` optional pointers are the precedent):

```go
// AIGateway enables the router's LLM-gateway middleware for this trigger
// (semantic response cache, token/cost metering, model fallback). Nil = the
// middleware is not installed for this trigger. Requires the router to be
// started with AI gateway enabled (Helm aiGateway.enabled); when the feature
// is disabled cluster-wide this field is ignored and a warning condition is
// surfaced.
// +optional
AIGateway *AIGatewayConfig `json:"aiGateway,omitempty"`
```

```go
type AIGatewayConfig struct {
    // SemanticCache, when set, caches LLM responses keyed by embedding
    // similarity of the request prompt.
    // +optional
    SemanticCache *SemanticCacheConfig `json:"semanticCache,omitempty"`

    // Metering, when set, parses OpenAI-compatible usage from responses and
    // emits Prometheus token/cost metrics, with optional per-tenant budgets.
    // +optional
    Metering *MeteringConfig `json:"metering,omitempty"`

    // Fallback, when set, routes to a secondary function/endpoint on
    // error/timeout/budget-exceeded from the primary.
    // +optional
    Fallback *FallbackConfig `json:"fallback,omitempty"`
}

type SemanticCacheConfig struct {
    // EmbeddingBackend is a Fission function reference (namespace/name) or an
    // absolute external URL that returns an embedding vector for a prompt.
    // +kubebuilder:validation:MinLength=1
    EmbeddingBackend string `json:"embeddingBackend"`

    // SimilarityThreshold in [0,1]: a cached entry is a hit when cosine
    // similarity of the incoming prompt's embedding to the entry's embedding
    // is >= this value. Default 0.95.
    // +optional
    // +kubebuilder:validation:Minimum=0
    // +kubebuilder:validation:Maximum=1
    SimilarityThreshold *string `json:"similarityThreshold,omitempty"`

    // TTLSeconds is the cache entry lifetime. Default 300.
    // +optional
    // +kubebuilder:validation:Minimum=1
    TTLSeconds *int `json:"ttlSeconds,omitempty"`

    // MaxEntries caps the per-router-replica LRU for this trigger. Default 1024.
    // +optional
    // +kubebuilder:validation:Minimum=1
    MaxEntries *int `json:"maxEntries,omitempty"`
}

type MeteringConfig struct {
    // Tenant is the label value for per-tenant token metrics and budget keying.
    // Defaults to the trigger namespace.
    // +optional
    Tenant string `json:"tenant,omitempty"`

    // BudgetTokensPerWindow, when > 0, rejects requests with HTTP 429 once the
    // tenant exceeds this many total tokens within WindowSeconds.
    // +optional
    // +kubebuilder:validation:Minimum=0
    BudgetTokensPerWindow int64 `json:"budgetTokensPerWindow,omitempty"`

    // WindowSeconds is the rolling budget window. Default 3600.
    // +optional
    // +kubebuilder:validation:Minimum=1
    WindowSeconds *int `json:"windowSeconds,omitempty"`
}

type FallbackConfig struct {
    // Secondary is the function reference (namespace/name) or absolute URL to
    // fall back to when the primary errors, times out, or is over budget.
    // +kubebuilder:validation:MinLength=1
    Secondary string `json:"secondary"`

    // TimeoutSeconds bounds the primary attempt before failing over. Default 30.
    // +optional
    // +kubebuilder:validation:Minimum=1
    TimeoutSeconds *int `json:"timeoutSeconds,omitempty"`
}
```

`SimilarityThreshold` is a `*string` (decimal-as-string, parsed with `strconv.ParseFloat`) because
CRD floats are discouraged for API stability; the rest are `*int`/`int64` to distinguish unset from
zero. All fields are `+optional`. After editing `types.go`, `make codegen` + `make generate-crds`
regenerate `pkg/generated/`, deepcopy, and `crds/v1/`.

`FunctionStatus` (`types.go:1146`) and `HTTPTriggerStatus` gain a condition surfaced by their
reconcilers: `ToolExposed` (True/False with reason `MCPDisabled` when `aiGateway`/tool is requested
but the subsystem isn't enabled cluster-wide) so misconfiguration is visible via `kubectl` rather
than silently ignored — mirroring the `TimeTriggerConditionScheduled` pattern in
`pkg/timer/reconciler.go`.

### Part A — MCP server subsystem (`pkg/mcp/`)

**Dispatch.** `cmd/fission-bundle/main.go` gains an `mcpPort int` flag and dispatch, mirroring
`--routerPort`/`--executorPort`. Because the MCP server proxies to the router internal listener, it
needs `ROUTER_INTERNAL_URL` exactly as kubewatcher/timer/mqt do; `main.go` already resolves that env
once into `publishURL` (`main.go:268`). We extend that resolved value to the MCP dispatch rather than
re-reading the env in the package (keeping `pkg/mcp` constructors deterministic for unit tests, per
the `MakeWebhookPublisher` convention):

```go
// in CommandLineArgs
mcpPort int
// in setupCommandLineArgs
flag.IntVar(&args.mcpPort, "mcpPort", 0, "Port that the MCP tool server should listen on")
// in startRequestedService, after the executor block and after publishURL is computed
if args.mcpPort != 0 {
    err = mcp.Start(ctx, clientGen, logger, mgr, args.mcpPort, publishURL)
    if err != nil {
        logger.Error(err, "mcp server exited")
    }
    return
}
```

`getServiceNameFromArgs` gains a `Fission-MCP` branch for OTEL service naming.

**Start function.** `pkg/mcp/main.go` mirrors `pkg/timer/main.go::Start` — same signature shape,
same `crd.ClientGeneratorInterface`, same controller-runtime manager + leader election
(`crmanager.NewLeaderElected`). The MCP server is read-only against CRDs and stateless, so multiple
replicas can serve concurrently; leader election is **not** required for correctness here (unlike the
timer), so the manager runs without it and every replica serves the same tool list. The signature:

```go
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger,
    mgr *errgroup.Group, port int, routerInternalURL string) error
```

It (1) builds a cache-backed controller-runtime manager over `Function`, (2) registers a
`FunctionReconciler` that maintains an in-memory `*ToolRegistry`, (3) starts an HTTP server on
`port` serving the MCP Streamable HTTP transport, and (4) runs both under the errgroup.

**Tool registry + reconcile (`pkg/mcp/registry.go`, `pkg/mcp/reconciler.go`).** The reconciler
follows the `TimeTriggerReconciler` shape exactly (cache-backed `client.Client`, `apierrors.IsNotFound`
→ remove, `GenerationChangedPredicate` via `controller.Register`):

```go
type ToolRegistry struct {
    mu    sync.RWMutex
    tools map[types.NamespacedName]Tool // only functions with Tool.ExposeAsMCP=true
}
type Tool struct {
    Name        string          // ToolConfig.ToolName or fn.Name
    Namespace   string
    FnName      string
    Description string
    InputSchema json.RawMessage // ToolConfig.InputSchema or {"type":"object"}
}
```

`Reconcile` gets the `Function`; if `Spec.Tool == nil || !Spec.Tool.ExposeAsMCP` it removes any
registered tool for that key, else it upserts `Tool`. It sets the `ToolExposed` condition on the
`Function` status (best-effort, never gating, via `controller.SetConditions`). Watching `Function`
through the Manager cache gives hot tool-list updates with no polling.

**MCP protocol server (`pkg/mcp/server.go`).** Serves the MCP **Streamable HTTP transport**
(`POST`/`GET` on a single `/mcp` endpoint, server→client messages as SSE) by reusing the RFC-0008
streaming response writer/flush primitives rather than a second SSE implementation. The two methods
that matter for `tools`:

- `tools/list` → enumerate `ToolRegistry`, emit `{name, description, inputSchema}` per tool,
  filtered by the caller's authorized namespaces (see AuthZ).
- `tools/call` → look up the named tool, then proxy to
  `routerInternalURL + /fission-function/<namespace>/<fnName>` with the tool-call arguments as the
  request body. The outbound `http.Client` transport is wrapped with
  `hmacauth.ServiceSigner(master, hmacauth.ServiceRouterInternal, rt, time.Now)` exactly as
  `publisher.MakeWebhookPublisher` does (`pkg/publisher/webhookPublisher.go:108`), where `master` is
  `storagesvcClient.HMACSecretFromEnv()`-style read of `FISSION_INTERNAL_AUTH_SECRET` performed in
  `Start` (not in library constructors). When the function streams (RFC-0008), the proxied response
  is relayed token-by-token into the MCP SSE stream as incremental `tools/call` result chunks; when
  it doesn't, a single result is returned.

We will use an existing Go MCP server library where one is vendored-acceptable (the official
`modelcontextprotocol/go-sdk` server transport) rather than hand-rolling the JSON-RPC envelope; the
dependency is added in the inert first phase so the protocol framing isn't our maintenance burden.
The Fission-specific code is the registry, the reconciler, and the `tools/call` → internal-listener
proxy.

**AuthZ.** Agents authenticate to the MCP endpoint with the same Bearer/JWT scheme the router's
`authMiddleware` already implements (`pkg/router/auth.go`): the MCP server installs an analogous
middleware that validates `Authorization: Bearer <jwt>` against `JWT_SIGNING_KEY`. The JWT carries an
`allowed_namespaces` claim (a list, or `*`); `tools/list` filters to those namespaces and
`tools/call` rejects (`-32600`-style MCP error mapped from 403) tools outside them. When MCP auth is
disabled (no signing key configured, dev mode) the server is pass-through, matching the router's
"empty secret = pass-through" stance — but the Helm default sets a key and `allowed_namespaces`
defaults to the installing namespace, so production is scoped-by-default.

**Helm (`charts/fission-all/`).** New `mcp.enabled: false` value gates a Deployment + ClusterIP
`svc/mcp` (port 8890) running `fission-bundle --mcpPort=8890`, with `ROUTER_INTERNAL_URL`,
`FISSION_INTERNAL_AUTH_SECRET` (from the existing `fission-internal-auth` secret), and `JWT_SIGNING_KEY`
env wired in like the other internal-publisher pods. RBAC: a Role granting read-only
`functions` (`get`/`list`/`watch`) in the watched namespaces plus `functions/status`
(`get`/`update`/`patch`) for the condition writes — strictly read+status, no function mutation. The
service is ClusterIP by default (operators front it with their own ingress/Gateway, per RFC-0007, if
they want external agent access); it never joins the public router listener.

**CLI (`pkg/fission-cli/cmd/function/`).** `fission function create|update` gain
`--expose-as-mcp` (bool), `--tool-description`, `--tool-input-schema` (path to a JSON Schema file),
and `--tool-name`. These populate `FunctionSpec.Tool`. A new `fission function tools` subcommand
lists which functions in the cluster are MCP-exposed (reads the CRDs directly, like the rest of the
CLI). No server round-trip is needed to set the schema — it's declarative on the CRD.

### Part B — Router AI-gateway middleware (`pkg/router/aigateway/`)

Part B is gated behind a Helm `aiGateway.enabled` flag and an `AI_GATEWAY_ENABLED` router env. When
disabled, the `AIGateway` trigger field is ignored (and a `ToolExposed`-style condition warns), so it
is fully inert until an operator opts in — this is why it is sequenced after Part A.

**Middleware wiring.** The router builds its mux in `pkg/router/httpTriggers.go::buildMuxes` (called
on every reconciliation — the documented place where per-mux behavior must be applied). For each
HTTPTrigger whose `Spec.AIGateway != nil` and where the feature is enabled, the per-route handler is
wrapped with the gateway middleware chain *inside* `buildMuxes`, composing with the existing
`authMiddleware`/CORS middlewares via the same `func(next http.Handler) http.Handler` shape as
`pkg/router/auth.go`. Order: metering (outermost, so it sees final status/usage) → budget gate →
semantic cache → fallback → the existing reverse-proxy handler.

**Semantic response cache (`pkg/router/aigateway/cache.go`).** On request: read+buffer the prompt
body, call `SemanticCacheConfig.EmbeddingBackend` (a Fission function via the internal listener with
the same HMAC signer, or an external URL) to get an embedding, and look up the per-replica
`*lru.Cache` keyed by trigger UID. A hit = an existing entry whose stored embedding has cosine
similarity ≥ `SimilarityThreshold` and is within `TTLSeconds`; serve the stored response and bump a
`fission_aigateway_cache_hits_total` counter. A miss proxies to the function, and on success stores
`{embedding, response}`. Streaming (RFC-0008): the middleware tees the streamed response into an
assembling buffer; once the stream completes it stores the assembled body, and a subsequent hit
**replays** the stored body as a stream (re-chunked through the RFC-0008 writer) so cached and live
responses are indistinguishable to the agent. Storage is **in-memory LRU per router replica** in v1:
no cross-replica coordination, bounded by `MaxEntries`, lost on restart — acceptable for a cost/latency
optimization and consistent with the router's stateless design. A shared store (Redis/valkey) is an
explicit open question, not v1.

**Token / cost metering (`pkg/router/aigateway/metering.go`).** Parses OpenAI-compatible
`usage.{prompt_tokens, completion_tokens, total_tokens}` from JSON responses, and for streamed SSE
accumulates from the final `usage` chunk (OpenAI emits it in the terminal `data:` frame when
`stream_options.include_usage` is set; when absent, we estimate from chunk count and log once). New
Prometheus vectors registered in the router registry (mirroring `pkg/router/metrics.go`'s
`init()`/`metrics.Registry.MustRegister`):

```go
aigatewayTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
    Name: "fission_aigateway_tokens_total",
    Help: "LLM tokens accounted by the AI gateway",
}, []string{"function_namespace", "function_name", "tenant", "kind"}) // kind=prompt|completion
```

Budget enforcement keeps a per-tenant rolling counter (`WindowSeconds`); when
`BudgetTokensPerWindow` is exceeded the middleware short-circuits with HTTP 429 before proxying and
increments `fission_aigateway_budget_rejections_total`. Like the cache, the counter is per-replica in
v1 (documented under-count with N replicas; a shared store fixes both at once — same open question).

**Model routing / fallback (`pkg/router/aigateway/fallback.go`).** Wraps the proxy: attempt the
primary (the trigger's own function) with a `TimeoutSeconds` context; on 5xx, timeout, or a
budget-429 it re-dispatches to `FallbackConfig.Secondary` (function ref via internal listener, or
external URL). Emits `fission_aigateway_fallbacks_total{reason}`. Fallback never retries on 4xx other
than the budget gate (client errors aren't transient).

**Helm + RBAC + CLI.** `aiGateway.enabled: false` value sets `AI_GATEWAY_ENABLED` on the router
Deployment. No new Kubernetes RBAC is needed (the router already has the internal-listener client and
calls functions); the embedding/secondary backends are reached over the same internal HMAC path or
plain HTTPS. CLI: `fission httptrigger create|update` gain `--ai-cache-backend`,
`--ai-cache-threshold`, `--ai-cache-ttl`, `--ai-meter-tenant`, `--ai-budget-tokens`,
`--ai-fallback` flags that populate `AIGatewayConfig` (analogous to how `GetRouteConfig` builds
`RouteConfig` in the CLI). All optional; absent flags leave `AIGateway` nil.

## Alternatives considered

- **Ship MCP as a standalone operator/binary outside `fission-bundle`.** Rejected: it would need its
  own RBAC, image, leader election, and CRD client wiring, duplicating what every `Start` function in
  `fission-bundle` already gets from `ClientGenerator`. The multi-headed binary is Fission's
  established pattern; `--mcpPort` is one more head.
- **Generate tool schemas by introspecting function code / OpenAPI at runtime.** Rejected for v1:
  Fission functions are arbitrary code with no enforced schema contract; runtime introspection is
  unreliable and language-specific. A declarative `ToolConfig.InputSchema` on the CRD is explicit,
  reviewable, and round-trips. Code-introspected schemas can be a later opt-in source that populates
  the same field.
- **Put the MCP server inside the router process** (it already has the internal client and the
  function reconcile-ish path). Rejected: the router's job is the hot HTTP data path; an MCP control
  endpoint with its own auth scope and CRD watch is cleaner as a separate subsystem, and keeps the
  router's mux/middleware surface focused.
- **A shared (Redis) semantic cache + budget store from day one.** Rejected for v1: adds a required
  external dependency and a new failure mode to a feature whose whole point is to be a cheap,
  best-effort optimization. Per-replica in-memory is correct-if-approximate and ships faster; the
  shared store is a clean follow-up behind the same config (open question).
- **Bundle an embedding model in the gateway.** Rejected: model choice is the user's; a bundled model
  bloats the router image and pins a model version. The embedding backend is a function/endpoint the
  user already runs.
- **Build a second SSE/streaming stack for MCP and the cache.** Rejected: RFC-0008 already defines
  the streaming writer/flush path; reusing it keeps one streaming implementation and is the stated
  cross-RFC dependency.

## Backward compatibility

- `FunctionSpec.Tool` and `HTTPTriggerSpec.AIGateway` are new `+optional` pointer fields; stored
  objects and old clients round-trip (unset = today's behavior exactly). `make codegen` /
  `make generate-crds` keep generated artifacts in lockstep.
- The MCP subsystem and the AI gateway are both off by default (`mcp.enabled: false`,
  `aiGateway.enabled: false`); clusters that don't enable them see zero behavioral change and no new
  pods, RBAC, or env.
- No public API is removed or changed; no Kubernetes floor bump (the floor stays 1.32 per
  `MinimumKubernetesVersion`). Nothing here deprecates an existing field, so the 2-minor-release
  deprecation policy isn't triggered.
- New CLI flags are additive; existing `function`/`httptrigger` invocations are unchanged.

## Rollout phases (one PR each, bisectable)

1. **CRD types (inert).** Add `ToolConfig` + `FunctionSpec.Tool` and the `AIGateway*` types +
   `HTTPTriggerSpec.AIGateway`; `ToolConfig.Validate`/`AIGatewayConfig.Validate` + unit tests;
   `make codegen` + `make generate-crds`. Compiles, nothing reads the fields yet.
2. **MCP server skeleton (inert).** `pkg/mcp` package with `Start`, the controller-runtime manager,
   the `--mcpPort` flag + dispatch in `main.go`, the MCP library dependency, and a `/mcp` handler
   that serves an **empty** `tools/list`. Server runs but advertises nothing; no proxy yet.
3. **Tool reconcile.** `FunctionReconciler` + `ToolRegistry`; `tools/list` returns opted-in
   functions; `ToolExposed` status condition. Tools are discoverable but not yet callable.
4. **Tool-call proxy + AuthZ.** `tools/call` proxies to the router internal listener with the
   `ServiceRouterInternal` HMAC signer; Bearer/JWT auth middleware with `allowed_namespaces`
   scoping; streaming relay via RFC-0008. Tools become callable.
5. **MCP Helm + CLI.** `mcp.enabled` Deployment/Service/RBAC/env; `--expose-as-mcp` et al. and
   `fission function tools`. Part A is fully usable end-to-end after this PR.
6. **Gateway scaffolding (inert).** `pkg/router/aigateway` package, `AI_GATEWAY_ENABLED` env +
   `aiGateway.enabled` Helm, middleware chain wired in `buildMuxes` behind the flag but with all
   three middlewares as no-ops. Compiles, inert.
7. **Semantic cache.** `cache.go` (embedding backend call, per-replica LRU, hit/miss, stream
   assemble+replay) + metrics + CLI cache flags.
8. **Token metering + budget.** `metering.go` Prometheus vectors, SSE usage parsing, 429 budget gate
   + CLI metering flags.
9. **Model fallback.** `fallback.go` primary→secondary on error/timeout/budget + CLI fallback flag.
10. **Docs + gated integration tests** for both parts (`t.Skip` when the subsystem/feature is
    disabled or backends are unset).

## Verification / test plan

- `make codegen && make generate-crds` clean; `make license-check`; `make code-checks`.
- **Unit (testify, `t.Context()`, table-driven, `t.Parallel()`):**
  - `pkg/mcp`: tool-schema generation from `ToolConfig` (name defaulting, empty-schema fallback,
    `ToolName` override); `ToolRegistry` upsert/remove on reconcile; `tools/call` argument →
    internal-URL request building (assert the HMAC signer wraps the transport, asserted via a
    `httptest.Server` that checks the signature header — not by reading a derived key back).
  - `pkg/apis/core/v1`: `ToolConfig.Validate` (description required when `ExposeAsMCP`; bad input
    schema rejected) and `AIGatewayConfig.Validate` (threshold range, budget/window).
  - `pkg/router/aigateway`: semantic-cache hit vs miss across a similarity boundary (inject a fake
    embedding backend returning fixed vectors; assert hit at ≥ threshold, miss below, TTL expiry,
    LRU eviction at `MaxEntries`); stream assemble-then-replay equivalence; token parsing from a
    non-streamed JSON body and from a streamed SSE `usage` frame; budget gate emits 429 past the
    limit; fallback fires on injected 5xx/timeout but not on 4xx.
- **envtest:** `Function` CRUD with `Tool` round-trips (including `InputSchema` raw JSON survives
  apiserver pruning via `PreserveUnknownFields`); `HTTPTrigger` CRUD with `AIGateway` round-trips;
  the MCP `FunctionReconciler` against an envtest apiserver flips the registry and the `ToolExposed`
  condition on create/update/delete. Prefer fake clientsets for the pure-logic reconcile tests per
  the test-writing guidelines; use envtest only for the round-trip/condition behavior.
- **Integration (`test/integration/suites/common/`, build tag `integration`, `t.Skip` when the
  subsystem image/flag is unset):**
  - Part A: create a function with `--expose-as-mcp`, connect an MCP client to `svc/mcp`, assert
    `tools/list` includes it with the expected schema, call it via `tools/call`, and assert the
    response matches a direct internal-listener invocation. Verify a tool in a non-authorized
    namespace is filtered from `tools/list`.
  - Part B: create an HTTPTrigger with `--ai-cache-*` in front of a deterministic echo function +
    a fake embedding function; issue the same prompt twice and assert the second is a cache hit
    (via `fission_aigateway_cache_hits_total` delta and identical body); assert
    `fission_aigateway_tokens_total` increments when the function returns an OpenAI-shaped `usage`.
- Cross-reference RFC-0008 in the streaming-relay tests (MCP `tools/call` streaming and cache
  stream-replay both build on the RFC-0008 writer).

## Open questions

- **Shared cache/budget store.** v1 is per-router-replica in-memory: caches don't share across
  replicas (lower hit rate) and budgets under-count by up to N×. Proposed follow-up: an optional
  shared backend (Redis/valkey) behind the same `SemanticCacheConfig`/`MeteringConfig`, opt-in.
  Should the budget under-count be made conservative (count locally but enforce at `budget/replicas`)
  in v1, or just documented? Proposed: document, fix with the shared store.
- **MCP transport scope.** Streamable HTTP only (per RFC-0008), or also offer the legacy
  HTTP+SSE/stdio transports some agents still use? Proposed: Streamable HTTP only for v1.
- **Embedding-backend protocol.** Standardize on an OpenAI `/v1/embeddings`-shaped contract for the
  embedding function/endpoint, or define a minimal Fission-native `{text}→{vector}` shape? Proposed:
  accept OpenAI-embeddings shape to reuse existing model servers.
- **Per-tool auth granularity.** v1 scopes by namespace via the JWT `allowed_namespaces` claim. Do we
  need per-tool allowlists (claim listing specific tool names) before GA? Proposed: namespace scope
  for v1, per-tool claim as a follow-up if asked for.
- **`tools/list` cardinality.** A cluster with thousands of exposed functions makes a large
  `tools/list`. Do we need pagination / a label-selector filter on the MCP endpoint? Proposed: add a
  selector query param if it becomes a problem; not v1.

# Fission Security Round 3 — Cross-Origin Defense (CORS + security headers)

> **For agentic workers:** REQUIRED SUB-SKILL — use `superpowers:executing-plans` to work this plan task-by-task.
> Each batch = one (or a small set of) commit(s) on a single long-lived branch.

## Context

Why this plan exists.

The 2026-05-18 SARIF (`output.sarif`, 218 findings) contains no explicit CORS rule, but the user asked to focus on "cross origin scripting" defense across all Fission services.
The closest SARIF cluster is `warning-sink-html` (10 findings, CVSS 8) — flagged-as-FP in round 2 because every handler sets `Content-Type: application/json`, but the scanner keeps re-flagging the same sites each round.
Independently of the scanner, code search confirms Fission today has **zero** CORS handling and **zero** security response headers (`grep -rn "Access-Control-Allow|cors\.|CORS|X-Content-Type-Options|Vary: Origin"` returns no Go matches anywhere under `pkg/` or `cmd/`).
The internal listener split done for GHSA-3g33-6vg6-27m8 closed the path-confusion surface, but did not add browser-cross-origin defense.

Concretely we're closing four gaps:
1. **No defense-in-depth against a future handler regression** that adds `Access-Control-Allow-Origin: *` (today the SARIF FP is correct; tomorrow it may not be).
2. **No CSRF/JSON-hijacking gate** on the router public listener (anyone's browser can hit `/version`, `/_fission/*`, and HTTPTrigger paths).
3. **No `X-Content-Type-Options: nosniff`** on any response, so MIME-sniffing attacks on attacker-controlled function output bypass the JSON content-type the FP triage relies on.
4. **No HTTPTrigger CORS configuration surface** for user functions that legitimately need cross-origin browser callers — users have to bake CORS into every function body.

Intended outcome: a `pkg/utils/httpsecurity` package providing two middlewares (`DenyAllCORS`, `SecurityHeaders`) wired into every Fission HTTP listener, plus an optional per-`HTTPTrigger` CORS allowlist on the router public path so SPAs can opt in without rewriting their function.

User scope decisions (confirmed in plan-mode brainstorm):
- **Internal services** (`executor`, `fetcher`, `builder`, `storagesvc`, `webhook`, `router-internal`) → strict deny.
- **Router public** (`/version`, `/router-healthz`, `/_fission/*`, auth routes) → strict deny on router-owned routes; **per-HTTPTrigger allowlist** for user-function paths defaulting to deny.
- **Security headers** → `X-Content-Type-Options: nosniff` + `Vary: Origin` on every Fission response (defense-in-depth, no new config surface).
- **Implementation** → shared package `pkg/utils/httpsecurity`.
- **Branch strategy** → single long-lived branch with batched commits, one PR at the end. Mirrors round-2 workflow (PR #3361, PR #3364).

## Strategy

1. **Triage is canonical.** Every finding-relevant rule keeps its existing round-2 verdict in `.security-fixes/findings-index.md`; round 3 adds a CORS section but does not re-litigate round-2 classifications.
2. **One shared middleware package, six call sites.** Centralised audit boundary; no duplicated header logic across subsystems.
3. **Browser-safe defaults.** The CRD spec change is additive (new optional `CorsConfig` field on HTTPTrigger). No existing trigger changes behaviour; only triggers that explicitly set the field opt in to CORS responses.
4. **Each batch must compile, lint, and pass tests in isolation.** Verification recipe is fixed at the end of every batch.
5. **No suppressions in source.** No `//nolint`, no scanner-ignore files. The index is authoritative.
6. **Resumability.** A worker reads `.security-fixes/round-3-cors/progress.md` to find the next pending batch.
7. **Pre-PR scrub** of `.security-fixes/round-3-cors/` before opening the PR — matches round-1/round-2 convention.

## Working layout

```
.security-fixes/round-3-cors/
├── README.md                 # entrypoint for round 3
├── plan.md                   # copy of this plan
├── findings-index.md         # CORS section + warning-sink-html re-triage
├── progress.md               # batch ledger
└── threat-model.md           # cross-origin attack surface per listener (one paragraph each)
```

Pre-existing `.security-fixes/{README,findings-index,progress,plan}.md` are the **round-2** artefacts (restored locally after PR #3364 merged per `feedback_restore_workspace_after_scrub`).
Round 3 lives under the `round-3-cors/` subdir to keep both rounds inspectable side-by-side.

Working branch: `security-fixes-cors-2026-05` (cut from `main`).

Scope (locked):
- **In:** `pkg/utils/httpsecurity` (new package), `pkg/router`, `pkg/executor`, `pkg/storagesvc`, `cmd/fetcher`, `cmd/builder`, `pkg/apis/core/v1/types.go` (HTTPTrigger), generated code (`pkg/generated/`, `zz_generated_*.go`, `crds/v1/`), Helm chart values + templates where the CORS allowlist surfaces.
- **Out:** `pkg/webhook` (admission webhooks served via controller-runtime to apiserver only; not browser-reachable, no CORS surface), metrics endpoints (`:8080`, served by `promhttp.Handler`; Prometheus is not a browser), `pkg/timer`/`pkg/kubewatcher`/`pkg/mqtrigger`/`pkg/canaryconfigmgr` (no HTTP listeners), CLI plugin loader.

## Critical files (single-line index)

- `pkg/utils/httpserver/server.go:14` — shared `StartServer(ctx, log, mgr, svc, port, handler)`; every Fission listener funnels through this. Confirms a single wrapping point per subsystem.
- `pkg/utils/httpsecurity/` — NEW package added in B1; holds `DenyAllCORS`, `SecurityHeaders`, `CORSAllowlist`.
- `pkg/router/router.go:108` — public listener handler chain; wrap site for public CORS/headers.
- `pkg/router/router.go:136` — internal listener handler chain (already HMAC-verified); wrap site for strict-deny CORS.
- `pkg/router/httpTriggers.go:178` (`buildMuxes`) — router-owned routes (`/_fission/*`, `/version`, `/router-healthz`) registered here; per-route deny-all wiring lands here.
- `pkg/router/httpTriggers.go:148-161` (`versionHandler`), `pkg/router/auth.go:172` (auth token handler), `pkg/router/functionHandler.go:739` (proxy error path) — `warning-sink-html` cited sites; need re-triage row in round-3 index confirming the round-2 verdict still holds AFTER the CORS/headers wrap.
- `pkg/executor/api.go:271-307` (`GetHandler`, `Serve`) — middleware chain entry point; wrap site.
- `pkg/storagesvc/storagesvc.go:271-301` (`GetHandler`/`Serve`) — wrap site.
- `cmd/fetcher/app/server.go:105-148` — wrap site.
- `cmd/builder/app/server.go:35-60` — wrap site.
- `pkg/apis/core/v1/types.go:682-727` — `HTTPTriggerSpec`; B4 adds a `CorsConfig *HTTPTriggerCorsConfig` field.
- `pkg/apis/core/v1/validation.go` — B4 adds `validateHTTPTriggerCorsConfig` invoked from existing HTTPTrigger validation.
- `charts/fission-all/values.yaml`, `charts/fission-all/templates/router/configmap.yaml` (if present) — B4 surface for global CORS defaults (none today; new file or values keys).
- `output.sarif` — input (gitignored).

## Batches (execution order on the single `security-fixes-cors-2026-05` branch)

| Batch | Theme | Findings closed / surface | Effort |
|---|---|---|---|
| **B0** | `.security-fixes/round-3-cors/` workspace + branch + threat model + finalised index seed | none (triage) | S |
| **B1** | NEW `pkg/utils/httpsecurity` package with `DenyAllCORS`, `SecurityHeaders`, `CORSAllowlist` + comprehensive unit tests | none (infrastructure) | M |
| **B2** | Wire `DenyAllCORS` + `SecurityHeaders` into the **router internal** listener; wire `SecurityHeaders` globally on router **public** listener + `DenyAllCORS` on router-owned routes only | 4 warning-sink-html sites (`version`, `auth`, `functionHandler` error path, `httpTriggers` healthz) re-triaged + neutralised | M |
| **B3** | Wire `DenyAllCORS` + `SecurityHeaders` into `executor`, `storagesvc`, `fetcher` sidecar, `builder` sidecar | 6 warning-sink-html sites re-triaged + neutralised | M |
| **B4** | Add `HTTPTriggerSpec.CorsConfig` CRD field + validation + `make codegen` + `make generate-crds`; wire `CORSAllowlist(spec.CorsConfig)` per-trigger in router public listener route registration | new user-facing surface (opt-in) | L |
| **B5** | Finalise round-3 index + threat model; pre-PR scrub of `.security-fixes/round-3-cors/` | n/a | S |

**Why not parallelise?** B2 depends on B1's package. B3 reuses B2's wrap pattern but the import path is what B1 establishes. B4 mutates generated code and must follow B2 so the router public-listener wiring is in place to consume the new field.

---

## Batch detail

### B0 — Branch + `.security-fixes/round-3-cors/` workspace

**Files:**
- Create: `.security-fixes/round-3-cors/README.md` (round-3 entrypoint; mirrors round-2 README style)
- Create: `.security-fixes/round-3-cors/plan.md` (copy of this file)
- Create: `.security-fixes/round-3-cors/findings-index.md` (CORS section + warning-sink-html re-triage seeds, status `open`)
- Create: `.security-fixes/round-3-cors/progress.md` (B0..B5 ledger, all `pending`)
- Create: `.security-fixes/round-3-cors/threat-model.md` (one paragraph per listener; cross-origin attack surface + mitigation rationale)

- [ ] **Step 1: Cut the branch.** `git fetch origin && git checkout main && git pull --ff-only && git checkout -b security-fixes-cors-2026-05`. Expected: branch exists, `.security-fixes/` (round-2 restore) and `output.sarif` remain untracked.
- [ ] **Step 2: Author the four workspace files** above. The threat-model paragraphs must cover, in this order: router-public, router-internal, executor, storagesvc, fetcher sidecar, builder sidecar, webhook (documented out-of-scope), metrics (documented out-of-scope). Each paragraph states: (a) who reaches it, (b) what cross-origin attacks apply, (c) the round-3 mitigation.
- [ ] **Step 3: Run `make code-checks`.** Expected: PASS (docs only).
- [ ] **Step 4: Commit.** `git add .security-fixes/round-3-cors/ && git commit -m "Bootstrap security-fixes-cors-2026-05 workspace (B0)"`.

---

### B1 — `pkg/utils/httpsecurity` package

**Files:**
- Create: `pkg/utils/httpsecurity/httpsecurity.go`
- Create: `pkg/utils/httpsecurity/httpsecurity_test.go`
- Update: `.security-fixes/round-3-cors/{findings-index.md,progress.md}`

**Public API (final):**

```go
package httpsecurity

// SecurityHeaders adds X-Content-Type-Options: nosniff and Vary: Origin
// to every response. Composable; safe to apply on any listener.
func SecurityHeaders(next http.Handler) http.Handler

// DenyAllCORS rejects browser-driven cross-origin requests:
//   - Strips any Access-Control-* response header the inner handler may have set.
//   - Returns 403 to any CORS preflight (OPTIONS with Origin + Access-Control-Request-Method).
//   - For non-preflight requests with an Origin header from a different host than the
//     request's Host, does NOT echo Origin (the browser SOP then blocks the read).
// Intended for cluster-internal listeners.
func DenyAllCORS(next http.Handler) http.Handler

// AllowlistConfig is the per-route or per-listener CORS allowlist.
type AllowlistConfig struct {
    AllowOrigins       []string      // exact-match origins; or ["*"] for any
    AllowMethods       []string      // GET/POST/...
    AllowHeaders       []string
    ExposeHeaders      []string
    AllowCredentials   bool          // forbids ["*"] origin
    MaxAge             time.Duration
}

// CORSAllowlist returns a middleware that honours cfg. Empty AllowOrigins
// (the zero value) behaves identically to DenyAllCORS, so unconfigured
// HTTPTriggers fall through to deny.
func CORSAllowlist(cfg AllowlistConfig) func(http.Handler) http.Handler
```

**Implementation notes:**
- Header writes must happen before any `WriteHeader` call. Use a small `responseWriterWrapper` that captures the first `WriteHeader` and injects `X-Content-Type-Options` / `Vary` headers before forwarding, so handlers that flush early still get the headers (similar to controller-runtime's middleware pattern).
- `DenyAllCORS` MUST NOT 403 on same-origin OPTIONS (legitimate non-CORS preflight from some clients); only when both `Origin` and `Access-Control-Request-Method` are present.
- `CORSAllowlist` MUST reject the `AllowOrigins: ["*"] + AllowCredentials: true` combination at construction time (panic in constructor — caller bug, not runtime bug).
- No external deps. Use `net/http` and `strings` only. Round-2 user feedback: avoid `github.com/rs/cors` for a trivial deny case.

- [ ] **Step 1: Write `pkg/utils/httpsecurity/httpsecurity.go`** with the API above. Mode the `Vary: Origin` header as an *append* so we don't clobber any `Vary: Accept-Encoding` the otel/gzip middleware sets.
- [ ] **Step 2: Write `pkg/utils/httpsecurity/httpsecurity_test.go`** with table-driven cases:
  - `SecurityHeaders`: nosniff present; Vary contains Origin; existing Vary entries preserved.
  - `DenyAllCORS`: preflight from different Origin → 403; preflight from same host → pass-through; simple cross-origin request → Access-Control headers stripped; same-origin → unchanged; inner handler's stale Access-Control-Allow-Origin removed.
  - `CORSAllowlist`: exact-match origin echoed; mismatched origin → no Allow-Origin header; AllowMethods/AllowHeaders honoured on preflight; `["*"]+credentials` → constructor panics; `MaxAge` rendered as seconds.
- [ ] **Step 3: Run `go test -race -count=1 ./pkg/utils/httpsecurity/... && golangci-lint run ./pkg/utils/httpsecurity/...`.** Expected: PASS.
- [ ] **Step 4: Run `make test-run`** (no other packages depend on this yet, but the gate stays clean). Expected: PASS.
- [ ] **Step 5: Update workspace.** Mark B1 done in `progress.md`; index gets a new "Round-3 additions" section seeded.
- [ ] **Step 6: Commit.** `git add pkg/utils/httpsecurity/ .security-fixes/round-3-cors/ && git commit -m "Add pkg/utils/httpsecurity (CORS deny + security headers) (B1)"`.

---

### B2 — Wire into router

**Files:**
- Modify: `pkg/router/router.go` (lines 108, 136 — public + internal handler chains)
- Modify: `pkg/router/httpTriggers.go` (router-owned routes get per-route `DenyAllCORS`; user-trigger routes do NOT — they'll be handled in B4)
- Modify: `pkg/router/router_test.go`, `pkg/router/httpTriggers_test.go` (extend, not rewrite)
- Update: `.security-fixes/round-3-cors/{findings-index.md,progress.md}`

**Wiring shape (router-public, `router.go:108`):**

```go
// before
publicHandler := otelUtils.GetHandlerWithOTEL(publicMR, "fission-router", otelUtils.UrlsToIgnore("/router-healthz"))

// after — SecurityHeaders outermost so they apply to every response,
// including those generated by the otel middleware itself
publicHandler := httpsecurity.SecurityHeaders(
    otelUtils.GetHandlerWithOTEL(publicMR, "fission-router", otelUtils.UrlsToIgnore("/router-healthz")),
)
```

**Wiring shape (router-internal, `router.go:136`):**

```go
// before
internalHandler := verifier(internalHandlerInner)

// after — DenyAllCORS first (rejects browser-driven preflights before
// HMAC even runs — saves the verifier from buffering preflight bodies),
// then HMAC verifier, then otel-wrapped inner. SecurityHeaders outermost.
internalHandler := httpsecurity.SecurityHeaders(
    httpsecurity.DenyAllCORS(verifier(internalHandlerInner)),
)
```

**Router-owned route wiring (`httpTriggers.go:buildMuxes`):**

Router-owned routes (`/_fission/version`, `/router-healthz`, `/_fission/auth/login` when AuthConfig.IsEnabled) get per-route `DenyAllCORS` applied as a subrouter wrap.
User-trigger routes (registered later in the loop at `pkg/router/httpTriggers.go:201+`) are deliberately untouched — B4 adds opt-in CORS via the new spec field. Until B4 lands, user-trigger routes behave exactly as today (no CORS headers; browsers see SOP block).

- [ ] **Step 1: Read the cited warning-sink-html sites.** `pkg/router/auth.go:172`, `pkg/router/functionHandler.go:739`, `pkg/router/httpTriggers.go:150`. Confirm each writes a known content-type and is reachable on the public listener. Document in the round-3 index that the round-2 FP verdict still applies AFTER `SecurityHeaders` adds `nosniff` (which closes the only realistic exploit chain — MIME confusion).
- [ ] **Step 2: Apply the two `router.go` wraps.** Add `"github.com/fission/fission/pkg/utils/httpsecurity"` to imports.
- [ ] **Step 3: Refactor `buildMuxes` to register router-owned routes on a subrouter** wrapped with `DenyAllCORS`, leaving the per-trigger `for i := range ts.triggers` loop on the parent router untouched. This keeps user-trigger CORS behaviour unchanged today and isolates the wrap to internal routes.
- [ ] **Step 4: Write the failing test.** In `pkg/router/router_test.go`, add `TestRouter_PublicListener_SetsSecurityHeaders` (probes `/router-healthz` and asserts `X-Content-Type-Options: nosniff` + `Vary: Origin` on response). Add `TestRouter_InternalListener_RejectsCrossOriginPreflight` (probes the internal handler with `OPTIONS` + cross-origin headers, asserts 403 without invoking the proxy).
- [ ] **Step 5: Run focused tests.** `go test -race -count=1 ./pkg/router/...`. Expected: PASS.
- [ ] **Step 6: Run `make test-run` + `golangci-lint run ./pkg/router/...`.** Expected: PASS.
- [ ] **Step 7: Update workspace.** Mark the four cited `warning-sink-html` rows in `findings-index.md` as `mitigated-defense-in-depth-B2` (still FP at root, but nosniff closes any future regression). Flip B2 in progress ledger.
- [ ] **Step 8: Commit.** `git add pkg/router/ .security-fixes/round-3-cors/ && git commit -m "Wire httpsecurity into router public + internal listeners (B2)"`.

---

### B3 — Wire into executor, storagesvc, fetcher, builder

**Files:**
- Modify: `pkg/executor/api.go` (around line 305 — `Serve` builds the handler chain)
- Modify: `pkg/storagesvc/storagesvc.go` (around line 300 — equivalent wrap site)
- Modify: `cmd/fetcher/app/server.go` (around line 147 — handler composition)
- Modify: `cmd/builder/app/server.go` (around line 60 — same)
- Modify: each subsystem's `*_test.go` to add a `..._RejectsCrossOriginPreflight` test (extend, not rewrite)
- Update: `.security-fixes/round-3-cors/{findings-index.md,progress.md}`

**Wiring shape (per subsystem):**

```go
// before (executor example, api.go:305)
handler := otelUtils.GetHandlerWithOTEL(executor.GetHandler(), "fission-executor", otelUtils.UrlsToIgnore("/healthz"))

// after
handler := httpsecurity.SecurityHeaders(
    httpsecurity.DenyAllCORS(
        otelUtils.GetHandlerWithOTEL(executor.GetHandler(), "fission-executor", otelUtils.UrlsToIgnore("/healthz")),
    ),
)
```

Same pattern verbatim for storagesvc, fetcher, builder.
All four are cluster-internal-only and already HMAC-protected via `hmacauth.ServiceVerifier`; CORS deny is defense-in-depth in case a future regression exposes them via Ingress.

- [ ] **Step 1: Apply the wrap to all four subsystems.** Add the `httpsecurity` import to each file.
- [ ] **Step 2: Read the 6 remaining cited warning-sink-html sites** (`pkg/builder/builder.go:122,258`, `pkg/executor/api.go:122`, `pkg/fetcher/fetcher.go:204,651`, `pkg/storagesvc/storagesvc.go:95,162`). Confirm the round-2 FP verdict (JSON content-type). Document in round-3 index that `nosniff` neutralises the MIME-sniff sub-case.
- [ ] **Step 3: Write per-subsystem preflight-reject tests.** One test per subsystem (`pkg/executor/api_test.go`, `pkg/storagesvc/storagesvc_test.go`, `cmd/fetcher/app/server_test.go` — create if needed, `cmd/builder/app/server_test.go` — create if needed). Each asserts a cross-origin OPTIONS preflight returns 403 before HMAC even runs.
- [ ] **Step 4: Run focused tests.** `go test -race -count=1 ./pkg/executor/... ./pkg/storagesvc/... ./cmd/fetcher/... ./cmd/builder/...`. Expected: PASS.
- [ ] **Step 5: Run `make test-run` + `golangci-lint run`** scoped to the changed dirs. Expected: PASS.
- [ ] **Step 6: Update workspace.** Mark the 6 warning-sink-html rows as `mitigated-defense-in-depth-B3`. Flip B3 in ledger.
- [ ] **Step 7: Commit.** `git add pkg/executor/api.go pkg/storagesvc/storagesvc.go cmd/fetcher/ cmd/builder/ .security-fixes/round-3-cors/ && git commit -m "Wire httpsecurity into executor, storagesvc, fetcher, builder (B3)"`.

---

### B4 — HTTPTrigger CORS spec + per-trigger allowlist

**Files:**
- Modify: `pkg/apis/core/v1/types.go` (around line 727 — add `CorsConfig *HTTPTriggerCorsConfig` field + new type)
- Modify: `pkg/apis/core/v1/validation.go` (HTTPTrigger validation hook)
- Run: `make codegen` → regenerates `pkg/generated/`, `pkg/apis/core/v1/zz_generated.deepcopy.go`
- Run: `make generate-crds` → updates `crds/v1/fission.io_httptriggers.yaml`
- Modify: `pkg/router/httpTriggers.go` user-trigger loop to apply `httpsecurity.CORSAllowlist(spec.CorsConfig)` per route when the field is set
- Modify: `charts/fission-all/values.yaml` ONLY if a chart toggle is needed (e.g., disabling per-trigger CORS overrides cluster-wide) — see step 4 decision
- Update: `.security-fixes/round-3-cors/{findings-index.md,progress.md}`

**Per CLAUDE.md "things that bite":** After editing `pkg/apis/core/v1/types.go`, you MUST run `make codegen` and `make generate-crds` together; CI fails otherwise. Do NOT hand-edit generated files.

**Per `feedback_no_changelog_values_docs_on_security_pr`:** No `values.yaml` doc-only keys unless the chart actually consumes the value at install time. The CRD field is the user contract; values.yaml gets a key only if step 4 decides we need cluster-wide override.

**New type shape (final):**

```go
// HTTPTriggerCorsConfig configures CORS response headers for browser
// callers of this trigger. When nil, the router emits no Access-Control-*
// headers and the browser's Same-Origin Policy enforces cluster isolation
// from cross-origin pages (current behaviour). When set, the router
// allowlists the configured origins on preflight + actual requests.
//
// Wildcard origins ("*") are forbidden when AllowCredentials is true (the
// browser will refuse the response). The router rejects such configs at
// CRD admission via the validating webhook so the trigger never reconciles
// into a broken state.
HTTPTriggerCorsConfig struct {
    // Exact-match origins (scheme + host + port). Use ["*"] for any origin.
    // +optional
    // +listType=set
    AllowOrigins []string `json:"allowOrigins,omitempty"`

    // HTTP methods this trigger accepts from CORS callers. Empty = trigger's
    // Methods field. +optional
    // +listType=set
    AllowMethods []string `json:"allowMethods,omitempty"`

    // Request headers the browser is allowed to send. +optional
    // +listType=set
    AllowHeaders []string `json:"allowHeaders,omitempty"`

    // Response headers exposed to the browser. +optional
    // +listType=set
    ExposeHeaders []string `json:"exposeHeaders,omitempty"`

    // Allow cookies/Authorization headers from the browser. When true,
    // AllowOrigins MUST NOT contain "*". +optional
    AllowCredentials bool `json:"allowCredentials,omitempty"`

    // Preflight cache lifetime as parsed by time.ParseDuration. +optional
    MaxAge string `json:"maxAge,omitempty"`
}
```

- [ ] **Step 1: Add the new struct + field** in `pkg/apis/core/v1/types.go`. Field goes on `HTTPTriggerSpec` as `CorsConfig *HTTPTriggerCorsConfig \`json:"corsConfig,omitempty"\``.
- [ ] **Step 2: Add validation** in `pkg/apis/core/v1/validation.go`. Reject `AllowOrigins=["*"] + AllowCredentials=true`; reject malformed `MaxAge` (must parse with `time.ParseDuration` and be ≥ 0); reject origins missing scheme. Hook into existing HTTPTrigger validation entry.
- [ ] **Step 3: Regenerate.** Run `make codegen && make generate-crds`. Verify only the expected files changed: `pkg/generated/...`, `pkg/apis/core/v1/zz_generated.deepcopy.go`, `crds/v1/fission.io_httptriggers.yaml`. Per `feedback_user_opens_prs`, ensure no other generated drift sneaks in.
- [ ] **Step 4: Wire `CORSAllowlist` per-trigger** in `pkg/router/httpTriggers.go`. Inside the per-trigger registration loop, if `trigger.Spec.CorsConfig != nil`, wrap the route with `httpsecurity.CORSAllowlist(toAllowlistConfig(trigger.Spec.CorsConfig))`. Provide a small `toAllowlistConfig` adapter (private helper in the router package; converts `*HTTPTriggerCorsConfig` to `httpsecurity.AllowlistConfig`, parsing `MaxAge` via `time.ParseDuration`). Decision for values.yaml: **no values.yaml key**. Per-trigger config is the user contract; cluster-wide override is YAGNI for now.
- [ ] **Step 5: Add test coverage.** Unit tests:
  - `pkg/apis/core/v1/validation_test.go`: rejects `["*"]+credentials`, rejects bad MaxAge, accepts valid configs.
  - `pkg/router/httpTriggers_test.go`: a trigger with `CorsConfig.AllowOrigins=["https://app.example.com"]` echoes that origin on preflight; another trigger without `CorsConfig` returns no Access-Control headers (deny-by-default contract).
- [ ] **Step 6: Run `make test-run` + `make code-checks`.** Expected: PASS.
- [ ] **Step 7: Update workspace.** Mark B4 done in ledger; index gets a paragraph describing the new opt-in surface.
- [ ] **Step 8: Commit.** Two commits if generated diff is bulky:
  ```
  git add pkg/apis/core/v1/types.go pkg/apis/core/v1/validation.go pkg/router/httpTriggers.go pkg/apis/core/v1/validation_test.go pkg/router/httpTriggers_test.go
  git commit -m "HTTPTrigger CorsConfig opt-in CORS allowlist (B4)"
  git add pkg/generated/ pkg/apis/core/v1/zz_generated.deepcopy.go crds/v1/
  git commit -m "Regenerate clientset and CRDs for HTTPTrigger.CorsConfig (B4)"
  ```

---

### B5 — Finalise + pre-PR scrub

**Files:**
- Update: `.security-fixes/round-3-cors/findings-index.md` (final pass)
- Update: `.security-fixes/round-3-cors/progress.md` (all batches done)
- Delete (in pre-PR scrub commit): `.security-fixes/round-3-cors/` workspace folder (round-2 dir at `.security-fixes/` was already pre-scrubbed in PR #3364; leave it untouched)

- [ ] **Step 1: Re-run sarif-summary helper.** `.security-fixes/sarif-summary.sh output.sarif`. Cross-check that no new rule appeared between B0 and B5; warning-sink-html count unchanged because the sites still write JSON (the SARIF tool can't see the new middleware).
- [ ] **Step 2: Final accepted-risk register pass.** Update the round-3 section to state that warning-sink-html is now `mitigated-defense-in-depth` rather than `FP-only` — even though the underlying handler is still safe today, `nosniff` + CORS deny make the path provably-unexploitable to a browser.
- [ ] **Step 3: Cross-check ledger against `git log`.** `git log --oneline main..security-fixes-cors-2026-05 | grep -E '\\((B[0-9])\\)'`. Expected: B0..B4 each present.
- [ ] **Step 4: Commit finalisation.** `git add .security-fixes/round-3-cors/ && git commit -m "Finalise round-3 CORS index (B5)"`.
- [ ] **Step 5: Pre-PR scrub.** `git rm -r .security-fixes/round-3-cors/ && git commit -m "Drop round-3-cors workspace pre-PR (B5 scrub)"`. Verify final diff: `git diff main..security-fixes-cors-2026-05 --name-only | sort` — expected only source/test/chart/generated files.
- [ ] **Step 6: Push the branch.** `git push -u origin security-fixes-cors-2026-05`. Per `feedback_user_opens_prs`, **stop here**: do NOT run `gh pr create`. Surface the branch URL and let the user open the PR in the GitHub UI. If user explicitly overrides, follow `feedback_soften_pr_create_rule`.
- [ ] **Step 7: Post-PR verification loop — see "Post-PR evaluation criteria" below.** This drives CI monitoring + Copilot review iteration until the PR is mergeable.

---

## Post-PR evaluation criteria (gate before declaring the work done)

Mandatory loop after the user opens the PR. Drive it from `gh` CLI; lean on CI as much as possible — re-run CI is preferred to local re-run when CI already covers the same gate, since the CI matrix catches platform-specific failures local `make test-run` cannot.

**Exit condition (all of):**
1. Every required GitHub Actions check on the PR head SHA is **green** (no `failure`, no `cancelled`, no `pending` past the workflow timeout).
2. No outstanding **meaningful** review comments from Copilot or human reviewers. "Meaningful" = a defect, regression risk, or contract violation; nits and style preferences are addressed when easy but are not gating.
3. `git diff main..security-fixes-cors-2026-05 --name-only` contains only source/test/chart/generated files — no `.security-fixes/`, no `output.sarif`.

**Per-iteration recipe (repeat until exit condition met, capped by `feedback_copilot_iteration_cap` at ~3 review rounds before asking the user):**

- [ ] **CI-1: Wait for CI to settle.** Use `gh pr checks <PR#> --watch` (or the `debug-github-ci` skill's `Monitor` poll-loop with 30s interval, 40-min timeout). Do not poll in a sleep loop.
- [ ] **CI-2: Triage failures.**
  - If a check is red, run `gh run view <run-id> --log-failed` and classify per `debug-github-ci`'s playbook: builder/fetcher build pipeline, NetworkPolicy selectors, /packages permissions, kind-ci skaffold profile, real bug.
  - Per `feedback_read_source_before_iterating`, read the failing test source before iterating on a fix.
  - For known-flake patterns (e.g., `TestGoEnv` Phase 2, `TestPackageChecksum` on K8s 1.34 — see CLAUDE.md "things that bite"), re-run via `gh run rerun <run-id> --failed` up to 2 times before treating as real. Do not chase >3 reruns without classifying.
  - For a real bug, fix in a new commit on the same branch; push; CI re-triggers.
- [ ] **CI-3: Request Copilot review.** Run `gh pr edit <PR#> --add-reviewer github-copilot[bot]`. If already requested in an earlier round and there are no new commits, skip.
- [ ] **CI-4: Wait for Copilot to finish reviewing.** Copilot posts review comments as a single batch typically within a few minutes. Use `gh pr view <PR#> --json reviews,comments` to enumerate.
- [ ] **CI-5: Triage Copilot comments.** For each comment:
  - **Meaningful** (defect, contract violation, regression risk, security issue, missing test): fix in a new commit; mark resolved when fixed.
  - **Style/nit** (naming, comment wording, micro-optimization, cosmetic): apply if trivial (<5 lines, no new tests), otherwise reply with rationale and dismiss. Per CLAUDE.md, do not add comments that explain *what* the code does — only *why* non-obvious.
  - **False positive** (Copilot misreads the code): reply with the specific evidence — link to the file:line that disproves the comment — and dismiss.
- [ ] **CI-6: After significant changes (≥1 commit fixing meaningful Copilot feedback), re-request review.** `gh pr edit <PR#> --add-reviewer github-copilot[bot]` again. Wait for the next batch. Significant = a behaviour or contract change; pure formatting/docs is not significant and does NOT trigger a re-request.
- [ ] **CI-7: Iteration cap.** Stop after 3 Copilot review rounds and ask the user before continuing. The cap exists because Copilot's marginal value per round drops sharply; rounds 4+ usually re-surface already-dismissed nits.
- [ ] **CI-8: Final gate.** Run `gh pr checks <PR#>` one last time. All required checks green AND no unresolved meaningful comments → declare done. Hand the PR URL back to the user.

**Anti-patterns to avoid:**
- Don't push fixes that haven't been verified locally first when the failure is reproducible locally — wastes CI cycles. Conversely, when a failure is CI-specific (e.g., `kind-ci` profile patch, runner timing), iterate on CI; don't twist `make test-run` to reproduce.
- Don't `gh pr merge` from automation — leaving the merge decision with the user (per `feedback_user_opens_prs`).
- Don't squash-edit history once Copilot has reviewed a SHA — Copilot loses its anchor and will re-review the whole diff next round.
- Don't disable hooks/`--no-verify` to push past a pre-push gate; per CLAUDE.md, fix the underlying issue.

---

## Verification recipe (run at every batch close)

```bash
make code-checks
make test-run
git status --short                                       # only files in batch scope?
.security-fixes/sarif-summary.sh output.sarif            # spot-check rule deltas
```

If any step fails: read the failing test source first (per `feedback_read_source_before_iterating`), fix the underlying issue, retry once. Do not bypass with `--no-verify`.

End-to-end smoke (after B4 lands, before B5 scrub):

```bash
# Deploy locally via skaffold-kind, then exercise the public listener:
kind create cluster --config kind.yaml
kubectl create ns fission && make create-crds
SKAFFOLD_PROFILE=kind make skaffold-deploy
kubectl port-forward svc/router 8888:80 -n fission &

# Verify security headers on a router-owned route:
curl -i http://localhost:8888/_fission/version | grep -Ei 'x-content-type-options|vary'
# Expect: X-Content-Type-Options: nosniff  AND  Vary: contains Origin

# Verify deny-by-default on a user trigger without CorsConfig:
fission env create --name nodeenv --image fission/node-env
fission fn create --name hello --env nodeenv --code test/integration/testdata/nodejs/hello.js
fission route create --function hello --url /hello
curl -H "Origin: https://attacker.example" -i http://localhost:8888/hello | grep -i access-control
# Expect: no Access-Control-Allow-Origin header

# Verify opt-in CORS allowlist (B4 only):
kubectl patch httptrigger hello -n default --type=merge -p '{"spec":{"corsConfig":{"allowOrigins":["https://app.example.com"],"allowMethods":["GET"]}}}'
curl -X OPTIONS -H "Origin: https://app.example.com" -H "Access-Control-Request-Method: GET" -i http://localhost:8888/hello | grep -i access-control
# Expect: Access-Control-Allow-Origin: https://app.example.com
```

---

## Resumability

A worker resuming this plan should:
1. `git switch security-fixes-cors-2026-05` (or recreate from main if branch was lost).
2. Read `.security-fixes/round-3-cors/progress.md` — the first `pending` row after the last `done` is the next batch.
3. Read `.security-fixes/round-3-cors/findings-index.md` and `threat-model.md` to confirm scope.
4. Confirm batch commits with `git log --oneline main..security-fixes-cors-2026-05 | grep -E '\\((B[0-9])\\)'`.
5. Pick the next pending batch; do NOT start a new batch until the current one is committed and verified.

---

## Out of scope (explicit)

- **`pkg/webhook`** — controller-runtime admission webhooks. kube-apiserver is the only caller; never browser-reachable. The TLS handshake + CA pin enforced by `ValidatingWebhookConfiguration` make CORS structurally meaningless.
- **Prometheus `/metrics`** — `promhttp.Handler` sets text/plain Prometheus content type; Prometheus scrapers are not browsers. Wrapping with `DenyAllCORS` is harmless but pointless.
- **`pkg/timer`, `pkg/kubewatcher`, `pkg/mqtrigger`, `pkg/canaryconfigmgr`** — no HTTP listeners; they are HTTP clients of the router.
- **`pkg/logger`** — symlink reaper, no HTTP server.
- **`pkg/plugin/*`** — CLI plugin loader, no HTTP surface, plus explicit round-2 user exclusion.
- **`test/*`** — test infrastructure.
- **mTLS / per-service TLS termination** — orthogonal hardening; chart guidance already directs operators to terminate TLS at ingress and rely on NetworkPolicy.
- **RFC 0001 application-layer HMAC for user-facing surfaces** — round-2 architectural-deferred; not implemented here.
- **Re-running the scanner** — user runs the scan; we consume `output.sarif`.

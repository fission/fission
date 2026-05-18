# Fission Security Findings Index — Round 3 (Cross-Origin Defense)

**Scan tool:** Harness SAST and SCA 1.0.0
**Scan date:** 2026-05-18
**Source SARIF:** `output.sarif` (gitignored at repo root)
**Totals:** 218 findings — 21 errors / 112 warnings / 105 notes

Round-3 scope: cross-origin defense across all Fission HTTP listeners.
The SARIF contains no explicit CORS rule, so this round is **proactive** rather than scanner-driven.
The closest scanner cluster (`warning-sink-html`, 10 findings, CVSS 8) is re-triaged here under the new defense-in-depth wrap.
For round-1 history see PR #3361; for round-2 history see PR #3364 and the round-2 index at `.security-fixes/findings-index.md`.

---

## Round-3 additions (proactive — no SARIF rule maps directly)

| Concern | Surface | Mitigation | Batch |
|---|---|---|---|
| Cross-origin browser fetches against router-owned routes (`/router-healthz`, `/_fission/version`, auth) | router public listener | `httpsecurity.DenyAllCORS` on a subrouter; `SecurityHeaders` listener-wide | B2 |
| Browser-driven preflights against cluster-internal listeners that lack CORS gates today | router-internal, executor, storagesvc, fetcher, builder | `httpsecurity.DenyAllCORS` + `SecurityHeaders` per subsystem | B2, B3 |
| MIME sniffing on JSON responses | every Fission listener | `X-Content-Type-Options: nosniff` via `SecurityHeaders` | B2, B3 |
| No opt-in CORS for user HTTPTriggers (SPAs must bake CORS into function bodies) | router public, per-trigger | `HTTPTriggerSpec.CorsConfig` + `httpsecurity.CORSAllowlist` per-trigger; deny by default | B4 |
| Cache-poisoning across CORS responses | every listener | `Vary: Origin` via `SecurityHeaders` | B2, B3 |

---

## Re-triage of `warning-sink-html` (10 sites)

All 10 sites were classified as **FP** in round 2 because every cited handler sets `Content-Type: application/json` before writing JSON-marshalled output, so browsers cannot interpret the body as HTML.
Round 3 keeps the FP verdict at the root but adds **defense-in-depth via `SecurityHeaders`** (`nosniff` closes the only realistic MIME-confusion regression path) and **`DenyAllCORS` on the listener** (rejects any future regression that adds `Access-Control-Allow-Origin: *`).

| File:Line | Function | Listener | Round-3 mitigation | Status |
|---|---|---|---|---|
| `pkg/router/httpTriggers.go:150` | `versionHandler` | router public | SecurityHeaders globally + DenyAllCORS on `/_version` route | mitigated-B2 |
| `pkg/router/auth.go:172` | auth token handler | router public | SecurityHeaders globally + DenyAllCORS on `<authUriPath>` route | mitigated-B2 |
| `pkg/router/functionHandler.go:739` | proxy error path | router public | SecurityHeaders globally; user-trigger DenyAllCORS deferred to B4 opt-in | mitigated-B2 |
| `pkg/executor/api.go:122` | `getServiceForFunctionAPI` | executor | B3 SecurityHeaders + DenyAllCORS | open → mitigated-B3 |
| `pkg/storagesvc/storagesvc.go:95` | archive list | storagesvc | B3 SecurityHeaders + DenyAllCORS | open → mitigated-B3 |
| `pkg/storagesvc/storagesvc.go:162` | upload response | storagesvc | B3 SecurityHeaders + DenyAllCORS | open → mitigated-B3 |
| `pkg/fetcher/fetcher.go:204` | version handler | fetcher sidecar | B3 SecurityHeaders + DenyAllCORS | open → mitigated-B3 |
| `pkg/fetcher/fetcher.go:651` | upload response | fetcher sidecar | B3 SecurityHeaders + DenyAllCORS | open → mitigated-B3 |
| `pkg/builder/builder.go:122` | version handler | builder sidecar | B3 SecurityHeaders + DenyAllCORS | open → mitigated-B3 |
| `pkg/builder/builder.go:258` | build response | builder sidecar | B3 SecurityHeaders + DenyAllCORS | open → mitigated-B3 |

After each batch lands, replace `open` with `mitigated-<short-sha>` and add a one-line `Mitigation evidence:` row pointing to the relevant test.

---

## Round-2 carryover (verdicts unchanged)

The round-2 index at `.security-fixes/findings-index.md` is canonical for all non-CORS findings.
Round 3 does NOT re-litigate:

- `command-injection-attacker-controlled` (4) — accepted-risk per round-2 allowlist; scanner re-flag.
- `warning-sink-execute` (6) — 2 dup of cmd-injection + 4 in `pkg/plugin/*` (out of scope).
- `Secret Keyword` (~50 across per-file rule IDs) — FP per round-2 rule-level triage.
- `idor-*` (16 across four sub-rules) — architectural-deferred per round-2 RFC 0001 note; NetworkPolicy mitigation in tree.
- `go-tls-skip-verify` (1) — `test/e2e/framework/framework.go` only; out of scope.
- `sleep-from-attacker-controlled` (18) — FP per round-2; constant `time.Sleep` in CLI poll loops.
- `timing-password-compare` (10) — FP per round-2; ResourceVersion / namespace / container-config diffs, not credential.
- `file-toctou` (70) — round-2 closed 6 mechanical TPs in B1–B3; remaining sites scanner-FP per round-2 per-finding table.
- `log-forging-attacker` (2) — round-2 closed in B2 (`pkg/builder/builder.go:298` sanitizeBuildLogLine).
- `http-to-log` (2) — FP per round-2 (`console/log.go:55` already sanitises).
- `time-file` (4) — FP per round-2; filename suffix only.
- `loose-file-permissions` (2) — round-2 partial close in B4; remaining 2 in test/benchmark out of scope.
- `http-no-ssl` (1) — accepted-risk per round-2 (in-cluster TLS terminated by ingress).
- `unsafe-deserialization-use` (1, new in round-3 SARIF, CVSS 10) — **NOT in round-3 scope**; triage in a future round.
Note its appearance here so a future worker doesn't lose it.
- Per-file Secret-Keyword rule IDs across `pkg/fission-cli/*`, `charts/fission-all/templates/*`, `test/integration/testdata/*` — FP per round-2 rule-level review.
- CKV_GCP_* (Checkov Terraform/GCP rules, 13 findings, CVSS 2-5.5) — **out of scope**; these scan infra-as-code under `test/benchmark/picasso/` and are not Fission control-plane.

---

## Re-scan workflow

When a fresh `output.sarif` arrives:

1. Re-run `.security-fixes/sarif-summary.sh output.sarif`.
2. Compare the per-rule table against this round-3 additions section + the round-2 per-rule index at `.security-fixes/findings-index.md`.
3. Any new ruleId not in either table is real signal — triage that delta.
4. Counts that match are already classified; trust the existing verdict unless a per-finding row says otherwise.
5. CORS findings: if a scanner introduces a CORS-specific rule in a future scan, fold the per-site results into the table above; the round-3 mitigation (httpsecurity wrap on every listener) should already close them.

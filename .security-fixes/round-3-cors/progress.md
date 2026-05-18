# Security Fixes Round 3 — Progress Ledger

Branch: `security-fixes-cors-2026-05`.
See `plan.md` for batch detail and `findings-index.md` for triage.

| Batch | Status   | Commit | Notes |
|-------|----------|--------|-------|
| B0    | done     | 4e0750cc | branch + workspace + threat model |
| B1    | done     | d145493a | `pkg/utils/httpsecurity` package (DenyAllCORS, SecurityHeaders, CORSAllowlist) |
| B2    | done     | 7408ad68 | wire into router public + internal listeners |
| B3    | done     | (pending) | wire into executor, storagesvc, fetcher, builder |
| B4    | pending  |        | HTTPTrigger CorsConfig CRD field + per-trigger allowlist + codegen |
| B5    | pending  |        | finalise index + pre-PR scrub |

Post-PR loop (after user opens the PR) lives in the plan under "Post-PR evaluation criteria" — CI green gate, Copilot review request, meaningful-comment triage, capped at ~3 review rounds.

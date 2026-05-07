# Security Fixes Round 2 — Progress Ledger

Branch: `security-fixes-2026-05`.
See `plan.md` for batch detail and `findings-index.md` for triage.

| Batch | Status   | Commit | Notes |
|-------|----------|--------|-------|
| B0    | done     | 274c6561 | branch + workspace + fresh index |
| B1    | done     | e68db701 | pkg/utils TOCTOU sweep (utils.go, zip.go) |
| B2    | done     | 17ee533e | pkg/fetcher TOCTOU + pkg/builder log-forging |
| B3    | done     | TBD    | misc TOCTOU sweep — all 21 sites scanner-FP, classification documented |
| B4    | pending  |        | file mode tightening (spec/init.go, logger.go) |
| B5    | pending  |        | index finalisation + pre-PR scrub |

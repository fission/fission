# Security Fixes Workspace — Round 3 (Cross-Origin Defense)

This folder holds the working artefacts for the 2026-05-18 cross-origin defense round.

- `plan.md` — full plan with per-batch tasks.
Start here.
- `findings-index.md` — round-3 CORS section + re-triage of `warning-sink-html` sites under the new defense-in-depth wrap.
- `progress.md` — batch ledger.
Read this to find what's next.
- `threat-model.md` — per-listener cross-origin attack surface and round-3 mitigation rationale.

Source SARIF (`output.sarif`) is gitignored.
Re-run the scanner to refresh it.
This folder is scrubbed in the final batch before the PR opens — reviewers see only source-code changes.

Round 2 artefacts live alongside this folder under `.security-fixes/` (restored locally after PR #3364 merged).
The round-2 `findings-index.md` and `plan.md` are unchanged; round 3 does not re-litigate round-2 verdicts.

Working branch: `security-fixes-cors-2026-05` (cut from `main` 2026-05-18).

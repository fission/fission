# Security Fixes Workspace — Round 2

This folder holds the working artefacts for the 2026-05-07 SAST remediation.

- `plan.md` — full plan with per-batch tasks.
Start here.
- `findings-index.md` — canonical triage of every finding in `output.sarif`.
- `progress.md` — batch ledger.
Read this to find what's next.
- `sarif-summary.sh` — query helper.

Source SARIF (`output.sarif`) is gitignored.
Re-run the scanner to refresh it.
This folder is scrubbed in the final batch before the PR opens — reviewers see only source-code changes.

# Pipeline — stage contract & repeated-execution model

Five stages.
Each is a standalone script under `scripts/`, driven by `scrub.sh`.
Every stage is keyed by `--repo owner/name` and reads its config via the resolution order in `config.example.toml` (explicit `--config` > `config.<owner>__<repo>.toml` > `config.example.toml`).

## Workdir

All derived artifacts live in `~/.cache/issue-pr-scrub/<owner>__<repo>/` (override via `[paths].workdir`).
Nothing is written into the target repo, so no `.gitignore` edits are needed and the same machine can scrub many repos.

```
<workdir>/
  state.json        timestamps + counts per stage
  threads.jsonl     normalized issues/PRs (extract)
  triage.jsonl      disposition/tier/labels/rationale per OPEN thread (triage)
  apply-plan.jsonl  machine-actionable rows, auto + review (report)
  report.md         human review, grouped (report)
  triage.csv        spreadsheet view (report)
  stale-review.csv  stale items sorted by signal, blank `keep` column (report)
  keepers.txt       YOU create this: numbers to force-keep (triage skips them)
  approved.jsonl    YOU create this: curated review-tier rows to apply
  ledger.jsonl      append-only record of applied gh actions (apply)
```

## Stage 1 — sync (`scrub.sh sync`)

Wraps gitcrawl. Reads GitHub, writes only local SQLite.

- **First run / full backfill:** `--full` → `gitcrawl sync owner/repo --state all` (metadata-only).
  Pulls every issue + PR. Comment/reaction counts come from the thread object itself, so the rule engine never needs comment hydration. Add `--with-comments` only if you want comment bodies in the FTS index — that's the slow path; metadata-only is minutes for a multi-year repo, honors rate limits, and resumes cleanly.
- **Incremental (default):** `gitcrawl sync owner/repo`.
  Fetches open items plus a recently-closed sweep, so local open-state doesn't rot between runs.

Idempotent: re-running updates changed rows in place (gitcrawl upserts by `(repo, kind, number)`).

## Stage 2 — extract (`extract.py`)

Pure read of the gitcrawl SQLite mirror → `threads.jsonl`, one normalized row per thread:
`number, kind (issue|pr), state, title, body_excerpt, body_len, author, url, labels[], is_draft, created_at, updated_at, closed_at, merged_at, closed_local, comment_count, reactions (both read from the GitHub object — no comment hydration needed), references[] (parsed #123 / pull/123), cluster_id, cluster_canonical (best-effort, usually null in keyword mode)`.

No network. Safe to re-run any time after a sync.

## Stage 3 — triage (`triage.py`)

Deterministic rule engine over `threads.jsonl`, config-driven. Triages **open, not-locally-closed** threads only. Builds duplicate groups locally (same-kind title-token Jaccard ≥ `dup_title_jaccard`, default 0.7; cross-references upgrade confidence) — no embeddings. Emits `triage.jsonl`: `disposition, tier, add_labels[], comment_template, context, rationale, type, areas`.

See `triage-rules.md` for the full rule list. Pure; re-run after tuning config without re-syncing.

## Stage 4 — report (`report.py`)

Transforms `triage.jsonl` into review artifacts:
- `report.md` — grouped by tier (auto → review → keep → skip) then disposition, with links + rationale + suggested labels. **This is what a human reads.**
- `triage.csv` — flat spreadsheet for sorting/filtering.
- `apply-plan.jsonl` — actionable rows (auto + review tiers) with `action` (close|label), `close_reason`, labels, comment template, context, and `updated_at_at_extract` (for the staleness guard).

Pure. No GitHub writes.

## Stage 5 — apply (`apply.py`) — the only writer

```
apply.py --repo R --auto                  # dry-run, tier=auto rows
apply.py --repo R --auto --execute        # perform them (gated, capped)
apply.py --repo R --from approved.jsonl --execute   # human-approved review items
```

Per row, in order: ledger-dedup → live re-fetch (staleness + protected guard) → cap check → build gh commands → (dry-run: print | execute: run + ledger + optional `gitcrawl close-thread`).
See `write-actions.md` for command shapes and the full gate list.

## Repeated-execution model (cadence)

The whole point is cheap re-runs:
- `scrub.sh run --repo R` (no `--full`) on a schedule → fresh report from an incremental sync.
- `triage.jsonl`/`report.md` are pure re-derivations, so tuning `config.*.toml` and re-running `triage`+`report` costs nothing (no re-sync).
- The **ledger** makes `apply` safe to re-run: already-applied actions are skipped, so you can apply in capped batches across multiple invocations until the auto set is drained.
- Locally closing handled items (`gitcrawl close-thread`, done automatically on a successful GitHub close) drops them from future `triage` runs, so the report shrinks to genuinely-new work each cycle.

To run as an actual cadence, wrap `scrub.sh run` in cron/CI and review the report; keep `apply --execute` a human-in-the-loop step.

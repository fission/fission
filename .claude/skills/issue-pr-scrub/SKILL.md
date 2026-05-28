---
name: issue-pr-scrub
description: Use when scrubbing or triaging a GitHub repo's open issue/PR backlog — closing stale/duplicate/already-shipped/EOL items and categorizing the rest (type, area, priority) like a product manager. Use for one-off backlog cleanups or a recurring triage cadence. Backed by gitcrawl (local SQLite mirror, no API-quota burn). Portable across OSS repos via --repo.
---

# issue-pr-scrub

A repeatable, low-risk pipeline to scrub a long-neglected GitHub backlog. It mirrors all issues/PRs into local SQLite with **gitcrawl**, applies a deterministic rule engine to decide a *disposition* per thread (close-stale / close-duplicate / close-implemented / close-eol / pr-archive / needs-info / keep+categorize), produces a reviewable report, and only then — gated and capped — applies the approved actions to GitHub via `gh`.

**Core principle: decide locally and cheaply (repeatable, no writes); apply to GitHub separately, gated, and capped.** gitcrawl's own `close-thread`/`close-cluster` are LOCAL-only (they hide items from future runs, they never touch GitHub). All real closing/labeling/commenting goes through `gh` in the `apply` stage.

## When to use

- "Scrub / triage / clean up the open issues (and PRs)", especially a backlog years deep.
- Recurring triage cadence (weekly/monthly) — the pipeline is idempotent and ledger-deduped.
- Finding duplicates, stale items, and things already fixed in a newer release.
- Categorizing the living backlog (type/area/priority) for product planning.

Not for: reading a single issue (`gh issue view`), or anything needing GitHub *write* beyond close/label/comment (milestones, transfers — do by hand).

## Prerequisites

`bash scripts/scrub.sh doctor` checks them. You need: **gitcrawl** (`brew install openclaw/tap/gitcrawl` then `gitcrawl init`), **gh** (authenticated), **python3 3.11+**, **sqlite3**. No OpenAI key needed — this skill runs keyword/FTS-only.

## The pipeline

Five idempotent stages, each reading/writing a per-repo workdir (`~/.cache/issue-pr-scrub/<owner>__<repo>/`). Run them via `scripts/scrub.sh`:

| Stage | Command | Writes GitHub? | Output |
| --- | --- | --- | --- |
| sync | `scrub.sh sync --repo R [--full]` | no (reads GitHub) | populates gitcrawl SQLite |
| extract | `scrub.sh extract --repo R` | no | `threads.jsonl` |
| triage | `scrub.sh triage --repo R` | no | `triage.jsonl` |
| report | `scrub.sh report --repo R` | no | `report.md`, `triage.csv`, `apply-plan.jsonl` |
| apply | `scrub.sh apply --repo R --auto|--from F [--execute]` | **yes (gated)** | `ledger.jsonl` |

Plus `scrub.sh protect --repo R [--execute]` — adds `keep-open` to the numbers in `keepers.txt` (gated; dry-run default).

`scrub.sh run --repo R [--full]` chains sync→extract→triage→report (never writes). Use `--full` on the first run (full backfill); omit it after (incremental, with closed-sweep).

**Read the detailed stage contract in `resources/pipeline.md` before running.**

## Typical workflow

```bash
cd .claude/skills/issue-pr-scrub/scripts
bash scrub.sh doctor                                   # prerequisites
bash scrub.sh labels --repo fission/fission            # what labels are missing?
bash scrub.sh run --repo fission/fission --full        # backfill + triage report (no writes)
# open ~/.cache/issue-pr-scrub/fission__fission/report.md and review
bash scrub.sh apply  --repo fission/fission --auto     # DRY-RUN: prints what it would do
bash scrub.sh apply  --repo fission/fission --auto --execute   # gated auto-tier closes
```

For `review`-tier items: copy the ones you approve from `apply-plan.jsonl` into `approved.jsonl`, then `apply.py --from approved.jsonl --execute`.

### Re-reviewing the stale pile (don't bulk-close blindly)

`close-stale` is purely age-based and is usually the biggest bucket — treat it as "needs eyes," not "safe to close." Every row is enriched with type/area/priority/engagement, and `report.py` emits **`stale-review.csv`** sorted by signal (highest reactions+comments, feature/bug first) so keepers float to the top.

```bash
# 1) skim stale-review.csv; put numbers worth keeping into keepers.txt
#    (one per line, '# notes' allowed)
# 2) re-run — keepers become skip and drop out of the apply-plan (instant, no re-sync)
bash scrub.sh triage --repo R && bash scrub.sh report --repo R
# 3) optional: make the protection visible upstream
bash scrub.sh protect --repo R --execute       # adds keep-open to keepers.txt items
# 4) close only what's left
bash scrub.sh apply --repo R --auto --execute
```

## Decisions baked in (this deployment)

- **Tiered auto + gated** writes. Only `tier=auto` (deterministic, low-risk) is eligible for `--auto`; everything judgmental is `tier=review` and needs explicit human approval via `--from`. Nothing is ever closed without `--execute`.
- **Keyword/FTS only** — no OpenAI/embeddings. Duplicate grouping uses deterministic `#ref` cross-references + title-token overlap (in `triage.py`), NOT gitcrawl vector clusters.
- **Extends existing repo labels.** Taxonomy + the rule→label map live in `resources/labels.md` and `scripts/config.example.toml`. Proposed new labels (`priority/*`, `stale`, `keep-open`) are *proposed*, created only via `labels --create-missing --execute`.

The rule engine, taxonomy, and write playbook are documented in:
- `resources/triage-rules.md` — every disposition, its heuristic, its tier
- `resources/labels.md` — full label taxonomy + mapping + proposed extensions
- `resources/write-actions.md` — gh write playbook, comment templates, safety gates
- `resources/gitcrawl-reference.md` — the gitcrawl commands + SQLite schema we rely on

## Safety gates (apply stage) — non-negotiable

Enforced in `apply.py`, every run:
1. **Dry-run by default.** No `--execute` ⇒ it only prints the gh commands.
2. **Per-run cap** (`apply.max_per_run`, default 25) + pacing between writes.
3. **Tier gate.** `--auto` touches only `tier=auto`; `review` items require `--from <curated>`.
4. **Staleness guard.** Re-fetches each thread's live state immediately before acting; skips if it was closed or *touched since extract* — re-run the pipeline to pick up changes.
5. **Protected re-check.** Skips anything now carrying a protected label.
6. **Ledger dedup.** A `(number, action)` recorded `ok` is never repeated.

### Rationalization table — STOP if you catch yourself thinking…

| Rationalization | Reality |
| --- | --- |
| "The backlog is huge, just bulk-close everything stale." | The cap and tier gate exist precisely for huge backlogs. Run in batches; let the human see the first batch's effect. |
| "These `review` items are obviously closable, I'll auto them." | `review` means a judgment call the rules couldn't make safely. Curate into `approved.jsonl`; don't promote them to `--auto`. |
| "Skip the dry-run, I trust the rules." | Dry-run is one command and surfaces mis-tuned thresholds before they post comments on dozens of issues. Always dry-run first. |
| "The staleness guard is annoying, disable it." | It's the only thing preventing you from closing an issue someone just commented on. Never bypass; re-run the pipeline instead. |
| "Just create the proposed labels, they're obviously fine." | Labels are repo-wide and visible to everyone. Confirm with a maintainer; `labels --create-missing` requires `--execute` for this reason. |
| "I'll open a PR with the triage changes." | This skill does not open PRs. It closes/labels/comments via gh only, and only what was approved. |

## Portability

Everything is parameterized by `--repo` and a per-repo `config.<owner>__<repo>.toml` (copy `config.example.toml`). The `[areas]`/`[types]`/`[versions]` tables are the only repo-specific tuning. Copy the whole `issue-pr-scrub/` folder into any OSS project's `.claude/skills/` and point it at a new repo.

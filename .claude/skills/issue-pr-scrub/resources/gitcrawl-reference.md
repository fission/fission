# gitcrawl reference — what this skill relies on

gitcrawl (https://gitcrawl.sh, `openclaw/gitcrawl`) mirrors a repo's issues/PRs into local SQLite so triage runs offline without burning GitHub API search quota. This skill uses a small, stable slice of it.

## Install / setup

```bash
brew install openclaw/tap/gitcrawl
gitcrawl init           # creates ~/.config/gitcrawl/{config.toml,gitcrawl.db,...}
gitcrawl doctor --json  # confirms github_token_present (uses gh auth token), version
```

Requires Go-built binary + a GitHub token (resolved from `GITHUB_TOKEN` or `gh auth token`). **No OpenAI key needed** for this skill — we do not embed or cluster.

## Commands we call

| Command | Why | Writes? |
| --- | --- | --- |
| `gitcrawl sync R --state all` | first-run full backfill (metadata-only by default) | local SQLite only |
| `gitcrawl sync R --state all --include-comments` | full backfill with comment bodies (opt-in via `scrub.sh sync --with-comments`) | local SQLite only |
| `gitcrawl sync R` | incremental refresh (open + closed sweep) | local SQLite only |
| `gitcrawl close-thread R --number <n> --reason ...` | hide a handled item from future runs (LOCAL) | local SQLite only |

We deliberately **do not** use `gitcrawl cluster`/`clusters`: clustering builds a graph over the OpenAI vector store, which is empty in keyword-only mode, so it yields nothing useful. Duplicate detection is done in `triage.py` instead.

`gitcrawl close-thread` / `close-cluster` are **local-only governance** — they set `closed_at_local` in SQLite and never call GitHub. Real closes are done by `apply.py` via `gh`.

## SQLite schema we read (gitcrawl 0.5.0)

DB at `~/.config/gitcrawl/gitcrawl.db` (override `GITCRAWL_DB_PATH`). Relevant tables:

**`repositories`** — `id, owner, name, full_name (unique), …`. We look up `repo_id` by `full_name = owner/repo`.

**`threads`** — one row per issue/PR. Columns we use:
`number, kind ('issue'|'pull_request'), state, title, body, author_login, author_type, html_url, labels_json (JSON array of GitHub label objects), is_draft, created_at_gh, updated_at_gh, closed_at_gh, merged_at_gh, closed_at_local (local-close marker), raw_json (full GitHub object — we read reactions.total_count)`.
Unique on `(repo_id, kind, number)`; indexed on `(repo_id, number)`, `(repo_id, state, closed_at_local)`, `(repo_id, updated_at)`.

**`comments`** — `thread_id, comment_type, author_login, is_bot, body, created_at_gh`. We count non-bot comments per thread for engagement/priority.

**`documents` / `documents_fts`** — canonical indexed text + FTS5 virtual table. Available for keyword search; the rule engine works off `threads`/`comments` directly so it doesn't strictly need FTS.

Best-effort (usually empty without vectors): `clusters` (`representative_thread_id, member_count, closed_at_local`), `cluster_members` (`cluster_id, thread_id, score_to_representative`), `cluster_runs`.

## Notes / gotchas

- `labels_json` is the raw GitHub array, e.g. `[{"name":"area-ops",...}]` — parse `.name`. Empty is `[]`.
- `kind` is `pull_request` in SQLite; we normalize to `pr` in `threads.jsonl`.
- Timestamps are RFC3339 strings (`…Z`); `common.days_since` handles parsing.
- A full backfill of a multi-year repo takes minutes and paginates the GitHub API; it honors `Retry-After`/rate-limit headers and resumes. Run it once with `--full`, then incremental.
- gitcrawl version pinned in this skill's testing: **0.5.0**. If the schema shifts in a future release, `extract.py`'s SELECT is the only thing to adjust.

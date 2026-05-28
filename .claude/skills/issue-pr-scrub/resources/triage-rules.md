# Triage rules — the rule engine

`triage.py` classifies each **open, not-locally-closed** thread with the first matching rule (order matters). Every threshold is in `config.*.toml` under `[triage]`, `[versions]`, `[protected]`. Output per thread: a `disposition`, a `tier`, labels to add, a comment template key, a context dict, and a human rationale.

## Tiers

| Tier | Meaning | How it's applied |
| --- | --- | --- |
| `auto` | Deterministic + low-risk. | Eligible for `apply.py --auto` (still gated by dry-run/cap/guards). |
| `review` | Needs human judgment the rules can't make safely. | Listed in `report.md`; apply only via `--from approved.jsonl`. |
| `keep` | Active/relevant; categorize only. | Label suggestions only; never closes. |
| `skip` | Protected. | Never auto-acted. |

## Rule order

**-1. Keepers guard → `skip`.**
Any number listed in `<workdir>/keepers.txt` (one per line, `#` comments allowed) is force-kept with disposition `keep`, tier `skip`, rationale "manually kept". This wins over every other rule and takes effect immediately — no re-sync. It's how you rescue stale items you've reviewed and want to protect. Optionally push the `keep-open` label upstream with `scrub.sh protect` so the protection is visible on GitHub and survives a DB rebuild.

**0. Protected guard → `keep` / `skip`.**
If the thread carries any `[protected].labels` (`keep-open`, `help wanted`, `good first issue`, `proposal`, `hold-off-merging`, `in progress`, `work-in-progress`, `ready-to-merge`, `area-security`, `research`), it is untouchable. Rationale records which label protected it.

**1. close-duplicate.**
Non-canonical member of a locally-detected duplicate group. Group detection (in `build_dup_groups`): union-find over (a) deterministic same-repo cross-references `#123`/`pull/123`/`issues/123` of 2+ digits, and (b) same-kind title-token Jaccard ≥ `dup_title_jaccard` (default 0.7; 4+ char tokens, stopwords removed). Canonical = lowest-numbered member that is open upstream AND not locally closed.
- evidence is computed **per member**: a member auto-closes only if it has a direct cross-reference edge to another group member; members joined by title overlap alone stay `review`.

**2. close-implemented → `review`.**
Body matches `(fixed|resolved|closed|implemented) (in|by|via) #NNN`. Conservative: a human must confirm the referenced PR actually merged (keyword mode can't verify merge state reliably).

**3. close-eol → `auto`.**
Body/title mentions a version at/below a floor in `[versions]` (`fission_floor`, `k8s_floor`) **and** inactive ≥ `stale_days`. The regexes capture a numeric version; `below_floor` compares tuples.

**4. PR lifecycle (kind == pr).**
- inactive ≥ `pr_abandoned_days` (default 730) → `pr-archive` / `auto`
- draft and inactive ≥ `pr_stale_days` (default 365) → `pr-archive` / `review`

**5. close-stale (issues) → `auto`.**
Open issue, inactive ≥ `stale_days` (default 540 ≈ 18mo), `comment_count ≤ stale_comment_max`.
If `stale_two_step = true` and not yet labeled `stale`: emits `mark-stale` (label + warning comment, no close) — a later run closes it once it's already labeled stale and still idle.

**6. needs-info → `review`.**
Issue inferred as `bug`, no reproduction (body shorter than `no_repro_body_chars` or lacking any `repro_markers`), inactive ≥ `needs_info_days`. Adds `needs-reproduction` + `need-user-input`.

**7. keep + categorize → `keep`.**
Everything else. Adds `needs-triage` plus inferred **type** (`[types]` keyword map → bug/feature-request/enhancement/documentation/question), up to 3 **areas** (`[areas]` keyword map, ranked by hit count over title+body-prefix), and a **priority** (`[priority]`: critical keywords, else engagement score = comments + reactions bucketed high/medium/low).

## Enrichment on every row

`type`, `areas`, `priority`, `engagement` (= reactions + comments), `reactions`, and `comment_count` are computed for **every** triaged thread — including stale/eol/pr-archive rows — so the close piles are reviewable, not opaque. `report.py` uses them to build `stale-review.csv` (sorted by signal) and to annotate the report.

## Stale re-review loop

`close-stale` is purely age-based and intentionally the largest pile. To re-review before closing:
1. Run the pipeline; open `stale-review.csv` (sorted: highest engagement + feature/bug first).
2. Decide keepers; put their numbers in `<workdir>/keepers.txt`.
3. Re-run `triage` + `report` (instant — no re-sync). Keepers are now `skip` and out of the apply-plan.
4. (Optional) `scrub.sh protect` to add `keep-open` upstream.
5. `apply --auto --execute` closes the remaining stale items in capped batches.

## Tuning

- Rules are pure re-derivations: edit `config.*.toml`, re-run `scrub.sh triage` + `report` — no re-sync.
- Too many false stale-closes? Raise `stale_days` / lower `stale_comment_max`.
- Duplicate groups too greedy? Raise `dup_title_jaccard` in `[triage]` (default 0.7), or act only on `ref`-evidence members and leave `title`-only members for manual review.
- Area labels noisy? Tighten `[areas]` keywords or lower the cap (in `infer_areas`).
- The engine never *removes* labels and never edits closed threads.

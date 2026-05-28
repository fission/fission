# Write actions — the gh playbook & safety gates

`apply.py` is the stage that applies **triage plan actions** (close/label/comment) to GitHub. It maps each approved `apply-plan.jsonl` row to `gh` commands. Dry-run prints them; `--execute` runs them.

Two other commands also write to GitHub and are likewise gated behind `--execute`: `protect.py` (adds the `keep-open` label to your keepers) and `labels_sync.py --create-missing` (creates the proposed labels). Everything else in the pipeline is read-only.

## Command shapes

For an issue (`gh issue ...`) or PR (`gh pr ...`):

```bash
# label-only (disposition=keep is never applied; only review/auto labels here)
gh issue edit  <n> --repo R --add-label "lab1,lab2"

# close (label first if any, then comment, then close)
gh issue comment <n> --repo R --body "<rendered template>"
gh issue close   <n> --repo R --reason not_planned   # or completed (issues only)
gh pr    close   <n> --repo R                          # PRs have no --reason
```

Close reasons by disposition: `completed` for close-implemented; `not_planned` for close-duplicate / close-eol / close-stale / pr-archive.

After a successful close, `apply.py` runs `gitcrawl close-thread R --number <n>` locally (unless `--no-local-close`) so the item drops out of the next triage run.

## Comment templates

Defaults live in `apply.py` (`TEMPLATES`); override per-repo via a `[templates]` table in config. Placeholders are filled from each row's `context` (`{canonical}`, `{detail}`, `{age_days}`). Keep them courteous and reversible — every one invites reopening.

| Key | Used by | Gist |
| --- | --- | --- |
| `duplicate` | close-duplicate | "Closing as a duplicate of #{canonical}… happy to reopen." |
| `implemented` | close-implemented | "Looks resolved in a more recent release… reopen if it persists." |
| `eol` | close-eol | "Targets {detail}, past our supported window, idle {age_days}d…" |
| `stale` | close-stale | "Inactive {age_days}d; scrubbing the backlog… reopen if still relevant." |
| `stale_warn` | mark-stale | "Inactive {age_days}d; marking stale, will close later unless there's activity." |
| `pr_archive` | pr-archive | "Archiving for inactivity (`archive-pr`)… rebase & reopen to continue." |
| `needs_info` | needs-info | "Can't reproduce — please add repro steps, versions, logs." |

## Safety gates (enforced every run)

1. **Dry-run default.** Without `--execute`, `apply.py` prints `would: gh …` and makes no changes.
2. **Selection is mutually exclusive and explicit.** Exactly one of `--auto` (tier=auto rows from `apply-plan.jsonl`) or `--from FILE.jsonl` (your curated review rows). No "apply everything".
3. **Per-run write cap.** `[apply].max_per_run` (default 25), overridable with `--max`, counts individual GitHub mutations (label/comment/close each count), checked at row boundaries so a row is never left half-applied. Stops cleanly; re-run to continue (per-step ledger skips done steps).
4. **Pacing.** `[apply].sleep_seconds` between writes — gentle on the API and on watchers' notifications.
5. **Staleness guard.** Immediately before acting, re-fetch live `state`/`updatedAt`/`labels`. Skip if: already closed upstream, or `updatedAt` differs from extract time (someone touched it). Re-run the pipeline to refresh.
6. **Protected re-check.** Skip if the live labels now include a protected label.
7. **Ledger dedup.** `ledger.jsonl` records every action with `ok`/`error`; an `ok` `(number, action)` is never repeated.
8. **Auth.** `--execute` requires a clean `gh auth status`.

## Curating review-tier items

`review` items never run under `--auto`. To act on them:

```bash
WD=~/.cache/issue-pr-scrub/fission__fission
# inspect, then keep only the rows you approve:
jq -c 'select(.tier=="review" and .disposition=="close-duplicate")' "$WD/apply-plan.jsonl" > "$WD/approved.jsonl"
# edit approved.jsonl by hand to drop any you're unsure about, then:
bash scrub.sh apply --repo fission/fission --from "$WD/approved.jsonl" --execute
```

The same gates (cap, staleness, protected, ledger) apply to `--from`.

## What this stage will NOT do

- Open or merge PRs.
- Edit milestones, assignees, projects, or transfer issues.
- Remove labels.
- Touch closed threads.
- Create labels (that's `labels --create-missing`, a separate gated command).

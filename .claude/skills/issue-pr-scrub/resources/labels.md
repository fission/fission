# Labels — taxonomy, mapping, and proposed extensions

The skill **extends the repo's existing labels** rather than inventing a parallel scheme. `scrub.sh labels --repo R` reconciles the config's referenced labels against the live repo and reports what's missing. The `[labels]`/`[types]`/`[areas]`/`[priority]` tables in `config.example.toml` are the editable mapping.

## Fission's current labels (captured 2026-05; verify with `gh label list`)

**Type / nature**
`bug`, `enhancement`, `feature-request`, `question`, `documentation`, `proposal`, `research`, `record-replay`

**Area** (all `0052cc` blue unless noted)
`area-function`, `area-api`, `area-dev-workflow`, `area-routing`, `area-composition`, `area-events`, `area-ops`, `area-install`, `area-observability`, `area-ux`, `area-performance`, `area-extensibility`, `area-builder`, `area-environment`, `area-executor`, `area-multitenancy`, `area-security`, `area-kubernetes`, `area-test`, `area-fission-cli`, `area-storagesvc`, `area-timetrigger`
Executor sub-areas: `executor-poolmgr`, `executor-newdeploy`, `executor-container`
Other component tags: `keda`, `podspec`

**Lifecycle / triage**
`triage`, `needs-triage`, `needs-reproduction`, `need-user-input`, `duplicate`, `wontfix`, `invalid`, `spam`, `in progress`, `work-in-progress`, `waiting-for-review`, `ready-to-merge`, `hold-off-merging`, `upstream-dependency`, `archive-pr` ("We close PR for timebeing and revisit again in future")

**Size**
`size/S`, `size/M`, `size/L`, `size/XL`

**PR automation / CI**
`go`, `docker`, `helm`, `github_actions`, `dependencies`, `no-changelog`, `skip-ci`, `run-old-ci`, `sweep`, `fission-ci/cd/release`

**Community**
`help wanted`, `good first issue`, `hacktoberfest`, `hacktoberfest-accepted`

## How the rule engine uses them

| Disposition | Label(s) added (`[labels]`) | gh close reason |
| --- | --- | --- |
| close-duplicate | `duplicate` | not_planned |
| close-implemented | _(none)_ | completed |
| close-eol | `wontfix` | not_planned |
| close-stale / mark-stale | `stale` * | not_planned (close) |
| needs-info | `needs-reproduction`, `need-user-input` | _(no close)_ |
| pr-archive | `archive-pr` | not_planned |
| keep | `needs-triage` + type + areas + priority * | _(no close)_ |

`*` = references a **proposed** label that does not exist yet (see below).

**Type inference** maps keyword sets to existing type labels (`bug`, `feature-request`, `enhancement`, `documentation`, `question`).
**Area inference** maps subsystem keywords to `area-*` (+ executor sub-areas / `keda`), capped at the 3 strongest per thread.
**Protected labels** (`[protected]`) make a thread untouchable: `keep-open`, `help wanted`, `good first issue`, `proposal`, `hold-off-merging`, `in progress`, `work-in-progress`, `ready-to-merge`, `area-security`, `research`.

## Proposed new labels (do NOT exist yet)

The engine references these; they're created only via `scrub.sh labels --create-missing --execute` (after maintainer sign-off — labels are repo-wide and visible to everyone).

| Label | Color | Purpose |
| --- | --- | --- |
| `stale` | `ededed` | No activity for a long time; close candidate |
| `keep-open` | `0e8a16` | Protects an issue from stale automation |
| `priority/critical` | `b60205` | Drop everything |
| `priority/high` | `d93f0b` | Address soon |
| `priority/medium` | `fbca04` | Normal |
| `priority/low` | `0e8a16` | Nice to have |
| `eol-version` | `bfd4f2` | Targets an unsupported/EOL version (optional) |

Rationale for additions: Fission has **no priority scheme** and **no stale label** today, both of which a backlog scrub needs. If you'd rather not add `priority/*`, set the four `*_label` keys in `[priority]` to existing labels (or empty) and they'll be skipped.

## Other repos

Run `scrub.sh labels --repo other/repo` first. It lists referenced-but-missing labels so you can either create them or retune `config.<owner>__<repo>.toml` to the repo's existing scheme. The `[areas]`/`[types]` keyword maps are the main thing to localize.

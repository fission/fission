#!/usr/bin/env python3
"""Stage 5 — apply: execute approved triage actions on GitHub via `gh`.

THE ONLY STAGE THAT WRITES TO GITHUB. Dry-run by default — it prints the exact
gh commands it WOULD run and exits. You must pass --execute to perform writes.

Selection (exactly one required):
  --auto              act on tier=auto rows from apply-plan.jsonl (gated set)
  --from FILE.jsonl   act on a curated file of approved rows (for review items)

Safety gates (all enforced here, every run):
  * dry-run default; --execute required for any mutation
  * --max N per-run cap (config apply.max_per_run; default 25)
  * gentle pacing between writes (config apply.sleep_seconds)
  * ledger dedup: never re-applies a (number, action) already recorded ok
  * staleness guard: re-fetches each thread's current state/updatedAt/labels
    immediately before acting; SKIPS if it was closed or touched since extract
  * protected-label re-check at apply time
  * requires a clean `gh auth status`
On a successful GitHub close, optionally runs `gitcrawl close-thread` locally
so the item drops out of the next run.
"""

from __future__ import annotations

import time

import common

TEMPLATES = {
    "duplicate": (
        "Closing as a duplicate of #{canonical}. Please follow that issue for "
        "updates; happy to reopen if you think this is meaningfully different."
    ),
    "implemented": (
        "This looks resolved in a more recent release. Closing as completed — "
        "please reopen with details if you still hit it on a current version."
    ),
    "eol": (
        "This targets {detail}, which is past our supported window, and has been "
        "inactive for {age_days} days. Closing as not planned — please reopen "
        "against a currently supported version if it still reproduces."
    ),
    "stale": (
        "This has been inactive for {age_days} days. We're scrubbing the backlog "
        "and closing stale items to keep it actionable. If this is still relevant, "
        "please reopen or comment and we'll pick it back up."
    ),
    "stale_warn": (
        "This has been inactive for {age_days} days and is being marked stale. "
        "It will be closed in a future scrub if there's no further activity. "
        "Comment to keep it open."
    ),
    "pr_archive": (
        "Archiving this PR for now due to inactivity (see the `archive-pr` label). "
        "We revisit archived PRs periodically — please rebase and reopen if you'd "
        "like to continue."
    ),
    "needs_info": (
        "We can't reproduce this from the information provided. Could you add "
        "reproduction steps, your Fission and Kubernetes versions, and relevant "
        "logs? Marking as needing more info."
    ),
}


def render(template_key: str, cfg: dict, context: dict) -> str | None:
    if not template_key:
        return None
    tmpl = cfg.get("templates", {}).get(template_key) or TEMPLATES.get(template_key)
    if not tmpl:
        return None
    safe = {"canonical": "?", "detail": "the targeted version", "age_days": "?"}
    safe.update({k: v for k, v in context.items() if v is not None})
    try:
        return tmpl.format(**safe)
    except (KeyError, IndexError):
        return tmpl


def gh_kind(kind: str) -> str:
    return "pr" if kind == "pr" else "issue"


def current_state(slug: str, kind: str, number: int) -> dict | None:
    """Re-fetch live state for the staleness/protection guard."""
    fields = "state,updatedAt,labels"
    try:
        data = common.gh_json(
            ["pr" if kind == "pr" else "issue", "view", str(number),
             "--repo", slug, "--json", fields]
        )
    except Exception as exc:  # noqa: BLE001 - surface gh failure, skip the item
        common.info(f"  #{number}: gh view failed ({exc}); skipping")
        return None
    return {
        "state": (data.get("state") or "").lower(),
        "updated_at": data.get("updatedAt"),
        "labels": [lb.get("name") for lb in data.get("labels", []) if lb.get("name")],
    }


def main() -> None:
    ap = common.base_argparser(__doc__)
    sel = ap.add_mutually_exclusive_group(required=True)
    sel.add_argument("--auto", action="store_true", help="tier=auto rows from apply-plan.jsonl")
    sel.add_argument("--from", dest="from_file", help="curated approved rows (.jsonl)")
    ap.add_argument("--execute", action="store_true", help="actually write (default: dry-run)")
    ap.add_argument("--max", type=int, default=None, help="override per-run cap")
    ap.add_argument("--no-local-close", action="store_true", help="skip gitcrawl close-thread")
    args = ap.parse_args()

    ctx = common.bootstrap(args)
    wd, slug, cfg = ctx["wd"], ctx["slug"], ctx["cfg"]
    apply_cfg = cfg.get("apply", {})
    cap = args.max if args.max is not None else apply_cfg.get("max_per_run", 25)
    sleep_s = apply_cfg.get("sleep_seconds", 2.0)
    local_close = apply_cfg.get("local_close", True) and not args.no_local_close
    protected = set(cfg.get("protected", {}).get("labels", []))

    src = (wd / "apply-plan.jsonl") if args.auto else common.expand_path(args.from_file)
    rows = list(common.read_jsonl(src))
    if args.auto:
        rows = [r for r in rows if r.get("tier") == "auto"]
    if not rows:
        common.info(f"no actionable rows in {src}")
        return

    mode = "EXECUTE" if args.execute else "DRY-RUN"
    common.info(f"[{mode}] {slug}: {len(rows)} candidate rows (cap {cap})")
    if args.execute:
        common.require_gh_auth()

    # Ledger dedup is PER STEP (label/comment/close), not per row: a close that
    # fails after the comment succeeded must not replay the (non-idempotent)
    # comment on the next run.
    done_steps = {(int(e["number"]), e.get("step"))
                  for e in common.read_jsonl(common.ledger_path(wd))
                  if e.get("status") == "ok"}
    applied = 0
    for r in rows:
        if applied >= cap:
            common.info(f"reached per-run cap ({cap}); stop. Re-run to continue.")
            break
        number, kind, action = int(r["number"]), r["kind"], r["action"]
        steps = build_commands(slug, kind, action, number, r, cfg)
        pending = [(s, c) for (s, c) in steps if (number, s) not in done_steps]
        if not pending:
            continue

        live = current_state(slug, kind, number)
        if live is None:
            continue
        if action == "close" and live["state"] != "open":
            common.info(f"  #{number}: already {live['state']} upstream; skip")
            continue
        if set(live["labels"]) & protected:
            common.info(f"  #{number}: now protected ({set(live['labels']) & protected}); skip")
            continue
        if live["updated_at"] and r.get("updated_at_at_extract") and \
                live["updated_at"] != r["updated_at_at_extract"]:
            common.info(f"  #{number}: touched since extract ({live['updated_at']}); skip — re-run pipeline")
            continue

        for step, cmd in pending:
            common.info(("  RUN: " if args.execute else "  would: ") + " ".join(_shellish(cmd)))

        if not args.execute:
            applied += 1
            continue

        # Run pending steps in order; stop at the first failure so a later run
        # resumes from exactly the step that failed.
        row_ok = True
        closed = False
        for step, cmd in pending:
            cp = common.run(cmd, check=False)
            status = "ok" if cp.returncode == 0 else "error"
            common.ledger_append(wd, {
                "number": number, "step": step, "action": action,
                "disposition": r["disposition"], "status": status,
                "error": "" if status == "ok" else (cp.stderr or cp.stdout or "").strip()[:300],
            })
            if status == "ok":
                done_steps.add((number, step))
                if step == "close":
                    closed = True
            else:
                common.info(f"  #{number}: step '{step}' FAILED — {(cp.stderr or '').strip()[:200]}")
                row_ok = False
                break

        if closed and local_close and common.have("gitcrawl"):
            common.run(
                ["gitcrawl", "close-thread", slug, "--number", str(number),
                 "--reason", r.get("disposition", "scrub")],
                check=False,
            )
        if row_ok:
            applied += 1
        time.sleep(sleep_s)

    common.info(f"[{mode}] done. {applied} {'applied' if args.execute else 'planned'}.")


def build_commands(slug, kind, action, number, row, cfg) -> list[tuple[str, list[str]]]:
    """Return ordered (step, argv) pairs for one row; step is the ledger key."""
    gk = gh_kind(kind)
    steps: list[tuple[str, list[str]]] = []
    labels = row.get("add_labels") or []
    if labels:
        steps.append(("label", ["gh", gk, "edit", str(number), "--repo", slug,
                                "--add-label", ",".join(labels)]))
    # Post the comment for ANY disposition that carries a template, not just
    # closes — mark-stale and needs-info are label-plus-comment actions.
    body = render(row.get("comment_template"), cfg, row.get("context", {}))
    if body:
        steps.append(("comment", ["gh", gk, "comment", str(number), "--repo", slug, "--body", body]))
    if action == "close":
        close_cmd = ["gh", gk, "close", str(number), "--repo", slug]
        reason = row.get("close_reason")
        if gk == "issue" and reason:
            close_cmd += ["--reason", reason]
        steps.append(("close", close_cmd))
    return steps


def _shellish(argv: list[str]) -> list[str]:
    out = []
    for a in argv:
        out.append(f'"{a}"' if " " in a else a)
    return out


if __name__ == "__main__":
    main()

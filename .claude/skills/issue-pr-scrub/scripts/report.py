#!/usr/bin/env python3
"""Stage 4 — report: triage.jsonl -> report.md + triage.csv + apply-plan.jsonl.

- report.md        human review, grouped by tier then disposition, with links.
- triage.csv       spreadsheet view of every triaged thread.
- apply-plan.jsonl  machine-actionable rows (close/label) for apply.py.

Pure re-derivation, safe to re-run. No writes to GitHub.
"""

from __future__ import annotations

import csv

import common

# Closing dispositions vs label-only.
CLOSE_DISPOSITIONS = {
    "close-duplicate", "close-implemented", "close-eol", "close-stale", "pr-archive",
}
# gh issue close --reason accepts {completed | not planned | duplicate}
# (note the space — this is the CLI value, not the API's not_planned).
CLOSE_REASON = {
    "close-duplicate": "duplicate",
    "close-implemented": "completed",
    "close-eol": "not planned",
    "close-stale": "not planned",
    "pr-archive": "not planned",
    "mark-stale": None,
    "needs-info": None,
    "keep": None,
}

TIER_ORDER = ["auto", "review", "keep", "skip"]


def plan_rows(triage: list[dict]):
    """One apply-plan row per actionable thread (auto + review tiers)."""
    for r in triage:
        if r["tier"] in ("keep", "skip"):
            continue
        action = "close" if r["disposition"] in CLOSE_DISPOSITIONS else "label"
        yield {
            "number": r["number"],
            "kind": r["kind"],
            "action": action,
            "disposition": r["disposition"],
            "tier": r["tier"],
            "close_reason": CLOSE_REASON.get(r["disposition"]),
            "add_labels": r["add_labels"],
            "comment_template": r["comment_template"],
            "context": r.get("context", {}),
            "rationale": r["rationale"],
            "url": r["url"],
            "title": r["title"],
            "updated_at_at_extract": r.get("updated_at"),
        }


# Re-review ordering: surface the stale items most likely to be keepers first
# (high engagement, and feature/bug over question/uncategorized).
TYPE_RANK = {"feature-request": 0, "enhancement": 1, "bug": 2,
             "documentation": 3, "question": 4, None: 5}


def write_stale_review(path, triage: list[dict]) -> int:
    rows = [r for r in triage if r["disposition"] in ("close-stale", "mark-stale")]
    rows.sort(key=lambda r: (-r.get("engagement", 0),
                             TYPE_RANK.get(r.get("type"), 5),
                             r.get("age_days", 0)))
    cols = ["keep", "number", "engagement", "reactions", "comments", "type",
            "priority", "areas", "age_days", "title", "url"]
    with path.open("w", newline="", encoding="utf-8") as fh:
        w = csv.writer(fh)
        w.writerow(cols)
        for r in rows:
            w.writerow([
                "", r["number"], r.get("engagement", 0), r.get("reactions", 0),
                r.get("comment_count", 0), r.get("type"), r.get("priority"),
                "|".join(r.get("areas", [])), int(r.get("age_days") or 0),
                r["title"], r["url"],
            ])
    return len(rows)


def write_csv(path, triage: list[dict]):
    cols = ["number", "kind", "disposition", "tier", "type", "age_days",
            "add_labels", "areas", "rationale", "url"]
    with path.open("w", newline="", encoding="utf-8") as fh:
        w = csv.writer(fh)
        w.writerow(cols)
        for r in triage:
            w.writerow([
                r["number"], r["kind"], r["disposition"], r["tier"], r.get("type"),
                r.get("age_days"), "|".join(r["add_labels"]), "|".join(r.get("areas", [])),
                r["rationale"], r["url"],
            ])


def write_md(path, slug: str, triage: list[dict]):
    by_tier: dict[str, dict[str, list[dict]]] = {}
    for r in triage:
        by_tier.setdefault(r["tier"], {}).setdefault(r["disposition"], []).append(r)

    lines = [
        f"# Backlog scrub report — {slug}",
        "",
        f"Generated {common.now_iso()} · {len(triage)} open threads triaged.",
        "",
        "## Summary",
        "",
        "| Tier | Disposition | Count |",
        "| --- | --- | --- |",
    ]
    for tier in TIER_ORDER:
        for disp in sorted(by_tier.get(tier, {})):
            lines.append(f"| {tier} | {disp} | {len(by_tier[tier][disp])} |")
    apply_order = (
        "**Apply order:** review `auto` items, then run "
        + "`apply.py --auto --execute`. Curate `review` items into "
        + "`approved.jsonl`, then `apply.py --from approved.jsonl --execute`. "
        + "`keep` items are label suggestions only."
    )
    lines += ["", apply_order, ""]

    titles = {
        "auto": "## Auto tier — gated, eligible for `apply.py --auto`",
        "review": "## Review tier — needs your approval",
        "keep": "## Keep — categorized for the living backlog (label suggestions)",
        "skip": "## Skipped — protected by label",
    }
    for tier in TIER_ORDER:
        if tier not in by_tier:
            continue
        lines += ["", titles[tier], ""]
        for disp in sorted(by_tier[tier]):
            rows = sorted(by_tier[tier][disp], key=lambda r: r["number"])
            lines += [f"### {disp} ({len(rows)})", ""]
            for r in rows:
                labels = ", ".join(f"`{lbl}`" for lbl in r["add_labels"]) or "—"
                meta = " · ".join(filter(None, [
                    r.get("type"),
                    r.get("priority"),
                    f"👍{r['reactions']}/💬{r['comment_count']}" if r.get("engagement") else None,
                ]))
                meta = f" [{meta}]" if meta else ""
                lines.append(
                    f"- [#{r['number']}]({r['url']}) {r['kind']} · "
                    f"{int(r.get('age_days') or 0)}d idle{meta} — {r['title']}  \n"
                    f"  add: {labels} · _{r['rationale']}_"
                )
            lines.append("")
    path.write_text("\n".join(lines), encoding="utf-8")


def main() -> None:
    ap = common.base_argparser("build report.md/csv/apply-plan from triage.jsonl")
    args = ap.parse_args()
    ctx = common.bootstrap(args)
    wd = ctx["wd"]
    triage = list(common.read_jsonl(wd / "triage.jsonl"))
    if not triage:
        common.die("no triage.jsonl — run the triage stage first")

    write_md(wd / "report.md", ctx["slug"], triage)
    write_csv(wd / "triage.csv", triage)
    n_stale = write_stale_review(wd / "stale-review.csv", triage)
    n_plan = common.write_jsonl(wd / "apply-plan.jsonl", plan_rows(triage))

    auto = sum(1 for r in triage if r["tier"] == "auto")
    review = sum(1 for r in triage if r["tier"] == "review")
    common.info(
        f"report.md, triage.csv written; apply-plan.jsonl has {n_plan} actionable "
        f"rows ({auto} auto, {review} review)."
    )
    if n_stale:
        common.info(
            f"  stale-review.csv: {n_stale} stale items, sorted by signal — mark keepers,"
            f" add their numbers to {wd / 'keepers.txt'}, then re-run triage+report."
        )
    common.info(f"  open {wd / 'report.md'}")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Stage 2 — extract: gitcrawl SQLite mirror -> normalized threads.jsonl.

Reads the gitcrawl.db tables (`threads`, `comments`, plus best-effort
`clusters`/`cluster_members`) and emits one normalized JSON row per issue/PR.
Pure re-derivation from the local mirror — no network, safe to re-run.

Dup detection note: gitcrawl clustering needs OpenAI vectors. In keyword-only
mode there are usually none, so we DON'T depend on gitcrawl clusters here; we
emit parsed cross-references (#123 / pull/123) and let triage.py group dups
locally. If durable clusters happen to exist, we attach them as a bonus.
"""

from __future__ import annotations

import json
import re
import sqlite3

import common

# Same-repo issue/PR references. 2+ digits (single digits are too ambiguous).
REF_HASH = re.compile(r"(?<![\w/])#(\d{2,})\b")
REF_PATH = re.compile(r"\b(?:issues|pull)/(\d{2,})\b")
REF_URL = re.compile(r"github\.com/[^/\s]+/[^/\s]+/(?:issues|pull)/(\d{2,})")

BODY_EXCERPT_CHARS = 4000


def parse_labels(labels_json: str) -> list[str]:
    try:
        arr = json.loads(labels_json or "[]")
    except json.JSONDecodeError:
        return []
    out = []
    for item in arr:
        if isinstance(item, dict) and item.get("name"):
            out.append(item["name"])
        elif isinstance(item, str):
            out.append(item)
    return out


def parse_refs(text: str, self_number: int) -> list[int]:
    if not text:
        return []
    found: set[int] = set()
    for rx in (REF_HASH, REF_PATH, REF_URL):
        for m in rx.finditer(text):
            n = int(m.group(1))
            if n != self_number:
                found.add(n)
    return sorted(found)


def raw_counts(raw_json: str) -> tuple[int, int]:
    """(comment_count, reactions_total) straight from the GitHub object.

    Avoids needing the slow --include-comments backfill: the issue/PR object
    already carries `comments` and `reactions.total_count`.
    """
    try:
        obj = json.loads(raw_json or "{}")
    except json.JSONDecodeError:
        return 0, 0
    comments = int(obj.get("comments", 0) or 0)
    r = obj.get("reactions")
    reactions = int(r.get("total_count", 0) or 0) if isinstance(r, dict) else 0
    return comments, reactions


def repo_id(con: sqlite3.Connection, slug: str) -> int:
    row = con.execute(
        "select id from repositories where full_name = ?", (slug,)
    ).fetchone()
    if row is None:
        common.die(
            f"repo {slug} not present in gitcrawl.db — run the sync stage first"
        )
    return row[0]


def best_effort_clusters(con: sqlite3.Connection, rid: int) -> dict[int, dict]:
    """Map thread number -> {cluster_id, canonical_number} from latest run.

    Returns {} when no vector-backed clusters exist (the keyword-only norm).
    """
    try:
        run = con.execute(
            "select id from cluster_runs where repo_id = ? order by id desc limit 1",
            (rid,),
        ).fetchone()
        if not run:
            return {}
        run_id = run[0]
        rows = con.execute(
            """
            select c.id, rep.number as canonical, t.number as member
            from clusters c
            join cluster_members m on m.cluster_id = c.id
            join threads t on t.id = m.thread_id
            left join threads rep on rep.id = c.representative_thread_id
            where c.cluster_run_id = ? and c.member_count > 1
            """,
            (run_id,),
        ).fetchall()
    except sqlite3.OperationalError:
        return {}
    out: dict[int, dict] = {}
    for cid, canonical, member in rows:
        out[member] = {"cluster_id": cid, "canonical_number": canonical}
    return out


def main() -> None:
    ap = common.base_argparser("extract gitcrawl mirror into threads.jsonl")
    args = ap.parse_args()
    ctx = common.bootstrap(args)
    db = common.gitcrawl_db(ctx["cfg"])
    if not db.exists():
        common.die(f"gitcrawl db not found at {db} — run the sync stage first")

    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    rid = repo_id(con, ctx["slug"])
    clusters = best_effort_clusters(con, rid)

    rows = con.execute(
        """
        select t.number, t.kind, t.state, t.title, t.body, t.author_login,
               t.author_type, t.html_url, t.labels_json, t.is_draft,
               t.created_at_gh, t.updated_at_gh, t.closed_at_gh, t.merged_at_gh,
               t.closed_at_local, t.raw_json
        from threads t
        where t.repo_id = ?
        order by t.number
        """,
        (rid,),
    ).fetchall()

    def emit():
        for r in rows:
            body = r["body"] or ""
            number = r["number"]
            labels = parse_labels(r["labels_json"])
            refs = parse_refs(r["title"] + "\n" + body, number)
            comment_count, reactions = raw_counts(r["raw_json"])
            cl = clusters.get(number, {})
            yield {
                "number": number,
                "kind": "pr" if r["kind"] == "pull_request" else "issue",
                "state": r["state"],
                "title": r["title"],
                "body_excerpt": body[:BODY_EXCERPT_CHARS],
                "body_len": len(body),
                "author": r["author_login"],
                "author_type": r["author_type"],
                "url": r["html_url"],
                "labels": labels,
                "is_draft": bool(r["is_draft"]),
                "created_at": r["created_at_gh"],
                "updated_at": r["updated_at_gh"],
                "closed_at": r["closed_at_gh"],
                "merged_at": r["merged_at_gh"],
                "closed_local": bool(r["closed_at_local"]),
                "comment_count": comment_count,
                "reactions": reactions,
                "references": refs,
                "cluster_id": cl.get("cluster_id"),
                "cluster_canonical": cl.get("canonical_number"),
            }

    out = ctx["wd"] / "threads.jsonl"
    n = common.write_jsonl(out, emit())
    con.close()

    state = common.load_state(ctx["wd"])
    state["extracted_at"] = common.now_iso()
    state["thread_count"] = n
    common.save_state(ctx["wd"], state)
    common.info(f"extracted {n} threads -> {out}")


if __name__ == "__main__":
    main()

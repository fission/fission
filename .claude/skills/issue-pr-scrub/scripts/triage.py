#!/usr/bin/env python3
"""Stage 3 — triage: rule engine over threads.jsonl -> triage.jsonl.

Deterministic and config-driven. Ordered, first-match-wins rules assign each
OPEN thread a disposition, a confidence tier, labels to add, a comment
template, and a human-readable rationale. Pure re-derivation, safe to re-run.

Tiers:
  auto   - deterministic + low-risk; eligible for gated auto-apply
  review - needs human judgment; listed in the report for approval
  keep   - categorize only, no close
  skip   - protected, never auto-acted

Closed / locally-closed threads are skipped (nothing to do).

Local dup detection (keyword-only mode): we build duplicate groups from
deterministic GitHub cross-references (#123) plus normalized title-token
overlap. No embeddings / OpenAI required.
"""

from __future__ import annotations

import re
from collections import defaultdict

import common

STOPWORDS = {
    "the", "a", "an", "to", "of", "in", "on", "for", "and", "or", "is", "are",
    "with", "when", "not", "no", "fission", "issue", "bug", "error", "support",
    "add", "use", "using", "via", "feature", "request", "should", "does",
}
TOKEN_RE = re.compile(r"[a-z0-9]{4,}")


def norm_tokens(title: str) -> set[str]:
    return {t for t in TOKEN_RE.findall(title.lower()) if t not in STOPWORDS}


def load_keepers(wd) -> set[int]:
    """Numbers the maintainer wants to protect from closing.

    One issue/PR number per line in <workdir>/keepers.txt; '#' starts a comment,
    and an inline '# note' after a number is allowed. Force-keeps instantly with
    no re-sync (decoupled from the upstream keep-open label).
    """
    path = wd / "keepers.txt"
    if not path.exists():
        return set()
    out: set[int] = set()
    for line in path.read_text().splitlines():
        line = line.split("#", 1)[0].strip()
        if line.isdigit():
            out.add(int(line))
    return out


def version_tuple(s: str) -> tuple[int, ...]:
    return tuple(int(p) for p in s.split(".") if p.isdigit())


def below_floor(found: str, floor: str) -> bool:
    return version_tuple(found) < version_tuple(floor)


# --------------------------------------------------------------------------- #
# duplicate grouping                                                          #
# --------------------------------------------------------------------------- #
def build_dup_groups(threads: list[dict], jaccard_floor: float = 0.7) -> dict[int, dict]:
    """Return {number: {"canonical": n, "evidence": "ref"|"title", "members":[...]}}.

    A group's canonical is its oldest still-relevant thread (lowest number).
    Only OPEN, non-protected members get a dup disposition later; the canonical
    is whichever member is open with the lowest number (fallback: lowest number).
    """
    by_num = {t["number"]: t for t in threads}
    parent: dict[int, int] = {}

    def find(x: int) -> int:
        parent.setdefault(x, x)
        while parent[x] != x:
            parent[x] = parent[parent[x]]
            x = parent[x]
        return x

    def union(a: int, b: int) -> None:
        ra, rb = find(a), find(b)
        if ra != rb:
            parent[max(ra, rb)] = min(ra, rb)

    # A cross-reference (#123) is NOT duplicate evidence on its own — "PR fixes
    # #123" or "see also #123" links related work, not duplicates. So duplicates
    # require SAME-KIND title-token overlap; a same-kind reference between two
    # already-similar threads only upgrades the evidence (title -> ref => auto).
    # Cross-kind (issue<->PR) pairs are never treated as duplicates.

    # same-kind reference pairs, for evidence upgrade only
    ref_pairs: set[frozenset[int]] = set()
    for t in threads:
        for ref in t["references"]:
            other = by_num.get(ref)
            if other and other["kind"] == t["kind"]:
                ref_pairs.add(frozenset((t["number"], ref)))

    toks = {t["number"]: norm_tokens(t["title"]) for t in threads}
    nums = [t["number"] for t in threads if len(toks[t["number"]]) >= 4]
    bucket: dict[str, list[int]] = defaultdict(list)
    for n in nums:
        for tok in toks[n]:
            bucket[tok].append(n)
    evidence: dict[frozenset[int], str] = {}
    seen_pairs: set[frozenset[int]] = set()
    for members in bucket.values():
        if len(members) > 40:  # ignore ultra-common tokens
            continue
        for i in range(len(members)):
            for j in range(i + 1, len(members)):
                a, b = members[i], members[j]
                key = frozenset((a, b))
                if key in seen_pairs:
                    continue
                seen_pairs.add(key)
                if by_num[a]["kind"] != by_num[b]["kind"]:
                    continue
                ta, tb = toks[a], toks[b]
                jac = len(ta & tb) / len(ta | tb)
                if jac >= jaccard_floor:
                    union(a, b)
                    evidence[key] = "ref" if key in ref_pairs else "title"

    # collect groups
    groups: dict[int, list[int]] = defaultdict(list)
    for t in threads:
        groups[find(t["number"])].append(t["number"])

    # members each thread has a "ref" edge to (for per-member evidence)
    ref_neighbors: dict[int, set[int]] = defaultdict(set)
    for pair, kind_ev in evidence.items():
        if kind_ev == "ref":
            a, b = tuple(pair)
            ref_neighbors[a].add(b)
            ref_neighbors[b].add(a)

    result: dict[int, dict] = {}
    for members in groups.values():
        if len(members) < 2:
            continue
        members.sort()
        mset = set(members)
        # Canonical must be a thread that is actually visible: open upstream AND
        # not locally hidden, else we'd close items as duplicates of something
        # intentionally removed from future runs.
        visible = [m for m in members
                   if by_num[m]["state"] == "open" and not by_num[m]["closed_local"]]
        canonical = (visible or members)[0]
        for m in members:
            if m == canonical:
                continue
            # Per-member evidence: auto only if THIS member has a direct ref edge
            # to another group member; title-only members stay review-tier.
            ev = "ref" if ref_neighbors[m] & mset else "title"
            result[m] = {"canonical": canonical, "evidence": ev, "members": members}
    return result


# --------------------------------------------------------------------------- #
# rule engine                                                                 #
# --------------------------------------------------------------------------- #
class Engine:
    def __init__(self, cfg: dict, keepers: set[int] | None = None):
        self.cfg = cfg
        self.keepers = keepers or set()
        self.protected = set(cfg.get("protected", {}).get("labels", []))
        tri = cfg.get("triage", {})
        self.stale_days = tri.get("stale_days", 540)
        self.stale_comment_max = tri.get("stale_comment_max", 8)
        self.stale_two_step = tri.get("stale_two_step", False)
        self.pr_stale_days = tri.get("pr_stale_days", 365)
        self.pr_abandoned_days = tri.get("pr_abandoned_days", 730)
        self.needs_info_days = tri.get("needs_info_days", 365)
        self.no_repro_chars = tri.get("no_repro_body_chars", 120)
        self.repro_markers = [m.lower() for m in tri.get("repro_markers", [])]
        self.lbl = cfg.get("labels", {})
        v = cfg.get("versions", {})
        self.fission_re = re.compile(v["fission_version_re"], re.I) if v.get("fission_version_re") else None
        self.fission_floor = v.get("fission_floor")
        self.k8s_re = re.compile(v["k8s_version_re"], re.I) if v.get("k8s_version_re") else None
        self.k8s_floor = v.get("k8s_floor")
        self.types = cfg.get("types", {})
        self.areas = cfg.get("areas", {})
        self.prio = cfg.get("priority", {})

    # -- inference helpers -------------------------------------------------- #
    def infer_type(self, text: str) -> str | None:
        for label, kws in self.types.items():
            if any(k in text for k in kws):
                return label
        return None

    def infer_areas(self, scope: str, limit: int = 3) -> list[str]:
        # Score each area by how often its keywords appear in the scoped text
        # (title + body prefix), then keep the strongest few. Avoids slapping
        # 9 area labels on one long issue.
        scored: dict[str, int] = {}
        order: list[str] = []
        for kw, labels in self.areas.items():
            hits = scope.count(kw)
            if hits:
                for lb in labels:
                    if lb not in scored:
                        order.append(lb)
                    scored[lb] = scored.get(lb, 0) + hits
        ranked = sorted(order, key=lambda lb: (-scored[lb], order.index(lb)))
        return ranked[:limit]

    def infer_priority(self, t: dict, text: str) -> str:
        if any(k in text for k in self.prio.get("critical_keywords", [])):
            return self.prio.get("critical_label", "priority/critical")
        score = t["comment_count"] + t["reactions"]
        if score >= self.prio.get("high", 15):
            return self.prio.get("high_label", "priority/high")
        if score >= self.prio.get("medium", 5):
            return self.prio.get("medium_label", "priority/medium")
        return self.prio.get("low_label", "priority/low")

    def eol_hit(self, text: str) -> str | None:
        for rx, floor, name in (
            (self.fission_re, self.fission_floor, "Fission"),
            (self.k8s_re, self.k8s_floor, "Kubernetes"),
        ):
            if not rx or not floor:
                continue
            for m in rx.finditer(text):
                ver = m.group(1)
                if below_floor(ver, floor):
                    return f"{name} {ver} (below supported {floor})"
        return None

    def has_repro(self, t: dict) -> bool:
        if t["body_len"] >= self.no_repro_chars:
            body = t["body_excerpt"].lower()
            if any(m in body for m in self.repro_markers):
                return True
        return False

    # -- main classification ------------------------------------------------ #
    def classify(self, t: dict, dup: dict | None) -> dict:
        labels = set(t["labels"])
        text = (t["title"] + "\n" + t["body_excerpt"]).lower()
        # Type/area inference uses a tight scope (title weighted + body prefix)
        # so a long body doesn't accrete every label.
        scope = (t["title"].lower() + " ") * 3 + t["body_excerpt"][:400].lower()
        age = common.days_since(t["updated_at"]) or 0.0
        is_pr = t["kind"] == "pr"

        type_label = self.infer_type(scope)
        area_labels = self.infer_areas(scope)
        priority_label = self.infer_priority(t, text)
        engagement = t["comment_count"] + t["reactions"]

        def out(disp, tier, add, template, rationale, context=None):
            return {
                "number": t["number"],
                "kind": t["kind"],
                "title": t["title"],
                "url": t["url"],
                "state": t["state"],
                "updated_at": t["updated_at"],
                "disposition": disp,
                "tier": tier,
                "add_labels": sorted(set(add)),
                "comment_template": template,
                "rationale": rationale,
                "context": context or {},
                "age_days": round(age, 0),
                "type": type_label,
                "areas": area_labels,
                # Enrichment present on EVERY row so the stale pile is reviewable.
                "priority": priority_label,
                "engagement": engagement,
                "reactions": t["reactions"],
                "comment_count": t["comment_count"],
            }

        # -1) manually kept (keepers.txt) wins over everything
        if t["number"] in self.keepers:
            return out("keep", "skip", [], None, "manually kept (keepers.txt)")

        # 0) protected
        if labels & self.protected:
            hit = ", ".join(sorted(labels & self.protected))
            return out("keep", "skip", [], None, f"protected by label(s): {hit}")

        # 1) duplicate
        if dup:
            tier = "auto" if dup["evidence"] == "ref" else "review"
            return out(
                "close-duplicate", tier, self.lbl.get("duplicate", ["duplicate"]),
                "duplicate",
                f"duplicate of #{dup['canonical']} "
                f"({'cross-reference' if dup['evidence'] == 'ref' else 'title overlap'} evidence; "
                f"group {sorted(dup['members'])})",
                context={"canonical": dup["canonical"], "evidence": dup["evidence"]},
            )

        # 2) implemented (PR linkage in body): conservative -> review
        if re.search(r"\b(fixed|resolved|closed|implemented) (?:in|by|via) #?\d{2,}", text):
            return out(
                "close-implemented", "review", [], "implemented",
                "body claims it was fixed/implemented in a referenced PR — verify the PR merged",
            )

        # 3) EOL version + stale
        eol = self.eol_hit(text)
        if eol and age >= self.stale_days:
            return out(
                "close-eol", "auto", self.lbl.get("eol", ["wontfix"]), "eol",
                f"targets {eol} and inactive {int(age)}d",
                context={"detail": eol, "age_days": int(age)},
            )

        # 4) PR-specific lifecycle
        if is_pr:
            if age >= self.pr_abandoned_days:
                return out(
                    "pr-archive", "auto", self.lbl.get("pr_archive", ["archive-pr"]),
                    "pr_archive", f"PR inactive {int(age)}d (>{self.pr_abandoned_days}d, abandoned)",
                    context={"age_days": int(age)},
                )
            if age >= self.pr_stale_days and t["is_draft"]:
                return out(
                    "pr-archive", "review", self.lbl.get("pr_archive", ["archive-pr"]),
                    "pr_archive", f"draft PR inactive {int(age)}d",
                )

        # 5) stale issue
        if not is_pr and age >= self.stale_days and t["comment_count"] <= self.stale_comment_max:
            if self.stale_two_step and "stale" not in labels:
                return out(
                    "mark-stale", "auto", self.lbl.get("stale", ["stale"]), "stale_warn",
                    f"inactive {int(age)}d — label stale + warn; close on a later run",
                    context={"age_days": int(age)},
                )
            return out(
                "close-stale", "auto", self.lbl.get("stale", ["stale"]), "stale",
                f"inactive {int(age)}d, {t['comment_count']} comments",
                context={"age_days": int(age)},
            )

        # 6) needs-info (bug, no repro, aging)
        if not is_pr and type_label == "bug" and not self.has_repro(t) and age >= self.needs_info_days:
            return out(
                "needs-info", "review", self.lbl.get("needs_info", ["needs-reproduction"]),
                "needs_info", f"bug without reproduction steps, inactive {int(age)}d",
            )

        # 7) keep + categorize
        add = list(self.lbl.get("needs_triage", ["needs-triage"]))
        if type_label and type_label not in labels:
            add.append(type_label)
        for a in area_labels:
            if a not in labels:
                add.append(a)
        if priority_label and priority_label not in labels:
            add.append(priority_label)
        # don't relabel things already triaged with a type+area
        rationale = "active/recent — categorize for the living backlog"
        return out("keep", "keep", add, None, rationale)


def main() -> None:
    ap = common.base_argparser("triage threads.jsonl -> triage.jsonl")
    args = ap.parse_args()
    ctx = common.bootstrap(args)
    wd = ctx["wd"]
    threads = list(common.read_jsonl(wd / "threads.jsonl"))
    if not threads:
        common.die("no threads.jsonl — run the extract stage first")

    # Only triage things that are actionable: open and not already locally closed.
    open_threads = [t for t in threads if t["state"] == "open" and not t["closed_local"]]
    jac = ctx["cfg"].get("triage", {}).get("dup_title_jaccard", 0.7)
    dup_map = build_dup_groups(threads, jaccard_floor=jac)  # over all to find canonicals
    keepers = load_keepers(wd)
    if keepers:
        common.info(f"keepers.txt: {len(keepers)} numbers force-kept")
    engine = Engine(ctx["cfg"], keepers=keepers)

    results = [engine.classify(t, dup_map.get(t["number"])) for t in open_threads]
    n = common.write_jsonl(wd / "triage.jsonl", results)

    state = common.load_state(wd)
    state["triaged_at"] = common.now_iso()
    state["open_count"] = len(open_threads)
    common.save_state(wd, state)

    # quick stderr histogram
    hist: dict[str, int] = {}
    for r in results:
        key = f"{r['disposition']}/{r['tier']}"
        hist[key] = hist.get(key, 0) + 1
    common.info(f"triaged {n} open threads -> {wd / 'triage.jsonl'}")
    for k in sorted(hist):
        common.info(f"  {k:28s} {hist[k]}")


if __name__ == "__main__":
    main()

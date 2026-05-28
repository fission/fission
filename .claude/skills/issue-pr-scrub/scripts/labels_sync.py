#!/usr/bin/env python3
"""labels-sync — reconcile the repo's GitHub labels with the skill taxonomy.

Read-only by default: prints the repo's current labels and which labels the
triage rules reference but don't yet exist (so you know what to create). With
--create-missing --execute it creates the missing proposed labels via `gh`.

This is what makes the skill portable: point it at any repo and it tells you
exactly which labels the config assumes but the repo lacks.
"""

from __future__ import annotations

import common

# Proposed extensions to a typical OSS label set (created only on request).
# name -> (hex color without '#', description)
PROPOSED = {
    "stale": ("ededed", "No activity for a long time; candidate for closing"),
    "keep-open": ("0e8a16", "Protected from stale automation"),
    "priority/critical": ("b60205", "Drop everything"),
    "priority/high": ("d93f0b", "Should be addressed soon"),
    "priority/medium": ("fbca04", "Normal priority"),
    "priority/low": ("0e8a16", "Nice to have"),
    "eol-version": ("bfd4f2", "Targets an unsupported/EOL version"),
}


def referenced_labels(cfg: dict) -> set[str]:
    out: set[str] = set()
    for vals in cfg.get("labels", {}).values():
        out.update(vals)
    out.update(cfg.get("types", {}).keys())
    for vals in cfg.get("areas", {}).values():
        out.update(vals)
    prio = cfg.get("priority", {})
    for key in ("critical_label", "high_label", "medium_label", "low_label"):
        if prio.get(key):
            out.add(prio[key])
    out.update(cfg.get("protected", {}).get("labels", []))
    return out


def main() -> None:
    ap = common.base_argparser("reconcile repo labels with the triage taxonomy")
    ap.add_argument("--create-missing", action="store_true",
                    help="create missing PROPOSED labels")
    ap.add_argument("--execute", action="store_true", help="actually create (default dry-run)")
    args = ap.parse_args()
    ctx = common.bootstrap(args)
    slug, cfg = ctx["slug"], ctx["cfg"]

    existing = {lb["name"] for lb in common.gh_json(
        ["label", "list", "--repo", slug, "--limit", "300", "--json", "name"]
    )}
    referenced = referenced_labels(cfg)
    missing = sorted(referenced - existing)

    common.info(f"{slug}: {len(existing)} labels exist; config references {len(referenced)}.")
    if not missing:
        common.info("all referenced labels already exist.")
        return

    common.info(f"\nreferenced but MISSING ({len(missing)}):")
    for name in missing:
        tag = "  [proposed]" if name in PROPOSED else "  [unknown — typo? or create manually]"
        common.info(f"  {name}{tag}")

    if not args.create_missing:
        common.info("\nrun with --create-missing --execute to create the [proposed] ones.")
        return

    creatable = [n for n in missing if n in PROPOSED]
    common.info(f"\n{'creating' if args.execute else 'would create'} {len(creatable)} proposed labels:")
    if args.execute:
        common.require_gh_auth()
    for name in creatable:
        color, desc = PROPOSED[name]
        cmd = ["gh", "label", "create", name, "--repo", slug,
               "--color", color, "--description", desc, "--force"]
        common.info(("  RUN: " if args.execute else "  would: ") + " ".join(cmd))
        if args.execute:
            cp = common.run(cmd, check=False)
            if cp.returncode != 0:
                common.info(f"    failed: {(cp.stderr or '').strip()[:200]}")


if __name__ == "__main__":
    main()

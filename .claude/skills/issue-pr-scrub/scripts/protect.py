#!/usr/bin/env python3
"""protect — push the `keep-open` label upstream for your keepers.

`keepers.txt` already force-keeps items locally (triage skips them with no
re-sync). This optional step also applies the `keep-open` label on GitHub so
the protection is visible to everyone and survives a fresh DB rebuild.

Dry-run by default; --execute required. Reads numbers from <workdir>/keepers.txt
unless --from is given.
"""

from __future__ import annotations

import common

LABEL = "keep-open"


def read_numbers(path) -> list[int]:
    out: list[int] = []
    for line in path.read_text().splitlines():
        line = line.split("#", 1)[0].strip()
        if line.isdigit():
            out.append(int(line))
    return out


def main() -> None:
    ap = common.base_argparser(__doc__)
    ap.add_argument("--from", dest="from_file", default=None,
                    help="file of numbers (default: <workdir>/keepers.txt)")
    ap.add_argument("--label", default=LABEL, help=f"label to add (default {LABEL})")
    ap.add_argument("--execute", action="store_true", help="actually label (default dry-run)")
    args = ap.parse_args()
    ctx = common.bootstrap(args)
    slug, wd = ctx["slug"], ctx["wd"]

    src = common.expand_path(args.from_file) if args.from_file else (wd / "keepers.txt")
    if not src.exists():
        common.die(f"no keepers file at {src} — add issue/PR numbers, one per line")
    numbers = read_numbers(src)
    if not numbers:
        common.info(f"{src} has no numbers")
        return

    mode = "EXECUTE" if args.execute else "DRY-RUN"
    common.info(f"[{mode}] {slug}: labelling {len(numbers)} keepers with `{args.label}`")
    if args.execute:
        common.require_gh_auth()
    for n in numbers:
        cmd = ["gh", "issue", "edit", str(n), "--repo", slug, "--add-label", args.label]
        common.info(("  RUN: " if args.execute else "  would: ") + " ".join(cmd))
        if args.execute:
            cp = common.run(cmd, check=False)
            if cp.returncode != 0:
                common.info(f"    #{n} failed: {(cp.stderr or '').strip()[:200]}")
    common.info(f"[{mode}] done.")


if __name__ == "__main__":
    main()

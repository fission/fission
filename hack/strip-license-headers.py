#!/usr/bin/env python3
"""Strip the legacy Apache-2.0 boilerplate block from source files.

One-time migration helper for the SPDX license-header switch, kept in-tree
for reference and re-runs. Idempotent: a file that does not begin with the
legacy Apache block is left byte-for-byte unchanged.

Usage:
    python3 hack/strip-license-headers.py FILE [FILE ...]
"""
from __future__ import annotations

import sys

# A leading comment block is removed only if it contains one of these.
MARKERS = (
    "Licensed under the Apache License",
    "limitations under the License",
)

HASH_SUFFIXES = (".sh", ".py", ".mk")


def is_hash_file(path: str) -> bool:
    name = path.rsplit("/", 1)[-1]
    if name == "Makefile" or name.startswith("Dockerfile"):
        return True
    return any(name.endswith(s) for s in HASH_SUFFIXES)


def strip_block_go(text: str) -> str:
    """Remove a leading /* ... */ block that holds the Apache boilerplate."""
    if not text.startswith("/*"):
        return text
    end = text.find("*/")
    if end == -1:
        return text
    block = text[: end + 2]
    if not any(m in block for m in MARKERS):
        return text
    return text[end + 2 :].lstrip("\n")


def strip_block_hash(text: str) -> str:
    """Remove the leading #-comment Apache block, preserving any shebang."""
    lines = text.splitlines(keepends=True)
    head: list[str] = []
    i = 0
    if lines and lines[0].startswith("#!"):
        head.append(lines[0])
        i = 1
    while i < len(lines) and lines[i].strip() == "":
        i += 1
    start = i
    while i < len(lines) and lines[i].lstrip().startswith("#"):
        i += 1
    block = "".join(lines[start:i])
    if not any(m in block for m in MARKERS):
        return text
    tail = lines[i:]
    while tail and tail[0].strip() == "":
        tail.pop(0)
    return "".join(head) + "".join(tail)


def strip_file(path: str) -> bool:
    with open(path, "r", encoding="utf-8") as fh:
        original = fh.read()
    stripped = strip_block_hash(original) if is_hash_file(path) else strip_block_go(original)
    if stripped == original:
        return False
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(stripped)
    return True


def main(argv: list[str]) -> int:
    changed = 0
    for path in argv:
        try:
            if strip_file(path):
                changed += 1
        except (OSError, UnicodeDecodeError) as exc:
            print(f"skip {path}: {exc}", file=sys.stderr)
    print(f"stripped {changed} file(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))

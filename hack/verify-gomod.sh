#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

# Verifies go.mod keeps direct and indirect requirements in separate blocks:
# exactly one direct `require (...)` block, one indirect (`// indirect`) block,
# no block mixing the two, and no single-line `require` directives.
# `go mod tidy` does NOT enforce this layout, so this guard does.
# Convention: .claude/resources/go-mod-conventions.md

set -euo pipefail

GOMOD="${1:-go.mod}"

awk '
  # Single-line require (e.g. `require foo v1.2.3`) — must live in a block.
  /^require[ \t]+[^(]/ {
    printf "  line %d: single-line require — consolidate into a block: %s\n", NR, $0
    bad = 1
    next
  }
  /^require[ \t]*\(/ { inblk = 1; start = NR; direct = 0; indirect = 0; next }
  inblk && /^\)/ {
    inblk = 0
    if (direct > 0 && indirect > 0) {
      printf "  line %d: require block mixes %d direct + %d indirect entries\n", start, direct, indirect
      bad = 1
    } else if (direct > 0) {
      directblocks++
    } else if (indirect > 0) {
      indirectblocks++
    }
    next
  }
  inblk && /\/\/ indirect/ { indirect++; next }
  inblk && /[^ \t]/ && $0 !~ /^[ \t]*\/\// { direct++; next }
  END {
    if (directblocks > 1)   { printf "  found %d direct require blocks — collapse to one\n", directblocks; bad = 1 }
    if (indirectblocks > 1) { printf "  found %d indirect require blocks — collapse to one\n", indirectblocks; bad = 1 }
    if (bad) {
      print "go.mod block layout is not canonical (see .claude/resources/go-mod-conventions.md):" > "/dev/stderr"
      exit 1
    }
  }
' "$GOMOD" || {
  echo "FAIL: $GOMOD violates the direct/indirect block convention." >&2
  exit 1
}

echo "OK: $GOMOD keeps direct and indirect requirements in separate blocks."

#!/bin/bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0
#
# Runs the antislop analyzers (github.com/sanketsudake/antislop) over the
# module. antislop rejects code that destroys or fabricates type evidence:
# `any` in signatures, fields and containers, narrowing back out of `any`,
# reflect, monkey patching, structural names, and untyped decoding. Every rule
# is on and every finding is an error; suppress a finding only by fixing it or
# by an explicit flag below, never silently.
#
# The binary is resolved in this order:
#   1. $ANTISLOP           — explicit path to a built binary
#   2. antislop on $PATH
#   3. go run …@$ANTISLOP_VERSION (needs access to the module; the repository
#      is private today, so this path only works with GOPRIVATE + git auth)
#
# Extra arguments are passed through, e.g. `hack/antislop.sh -test=false ./pkg/router/...`.
# Not wired into CI yet: see docs/superpowers/plans/2026-08-17-antislop-cleanup.md
# (Phase 0) — CI needs a public, tagged module or the golangci-lint module
# plugin, and the baseline must burn down first.

set -euo pipefail

ANTISLOP_VERSION="${ANTISLOP_VERSION:-latest}"

if [ -n "${ANTISLOP:-}" ]; then
	bin="$ANTISLOP"
elif command -v antislop >/dev/null 2>&1; then
	bin="$(command -v antislop)"
else
	bin=(go run "github.com/sanketsudake/antislop/cmd/antislop@${ANTISLOP_VERSION}")
fi

if [ "$#" -eq 0 ]; then
	set -- ./...
fi

# Flags that record deliberate policy for this repository. Add a comment for
# every deviation from the analyzer defaults.
#
# nostructuralnames is off: its only default term is "shape", and in this
# repository "route shape" is RFC-0013 vocabulary — the set of HTTPTrigger
# fields whose change forces a mux rebuild — that is exposed as the metric
# label value shape_changed / shape_change and in HTTPTrigger condition
# messages. Renaming the identifiers alone would split the vocabulary and
# renaming the labels is a user-visible metrics change. The analyzer has no
# per-package exemption (-nostructuralnames.terms replaces the whole list).
POLICY_FLAGS=(-nostructuralnames=false)

exec "${bin[@]}" "${POLICY_FLAGS[@]}" "$@"

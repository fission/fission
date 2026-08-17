#!/bin/bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0
#
# Runs the antislop analyzers (github.com/sanketsudake/antislop) over the
# module. antislop rejects code that destroys or fabricates type evidence:
# `any` in signatures, fields and containers, narrowing back out of `any`,
# reflect, monkey patching, structural names, and untyped decoding.
#
# The binary is resolved in this order:
#   1. $ANTISLOP           — explicit path to a built binary
#   2. antislop on $PATH
#   3. go run …@$ANTISLOP_VERSION (needs access to the module; the repository
#      is private today, so this path only works with GOPRIVATE + git auth)
#
# Usage:
#   hack/antislop.sh                 gate the tree against the baseline
#   hack/antislop.sh --list          print every finding, baselined or not
#   hack/antislop.sh --update        rewrite the baseline from the current tree
#   hack/antislop.sh ./pkg/router/…  report on specific packages (no gating)
#
# Gating is against hack/antislop-baseline.txt, which records the ACCEPTED
# findings as "<count> <file> <analyzer>" — deliberately without line numbers,
# so unrelated edits above a finding do not churn it. The gate fails when a
# file/analyzer pair appears that the baseline does not list, or when a
# baselined pair grows. A pair that shrinks passes.
#
# The cost of dropping line numbers is that a net-zero swap within one
# file/analyzer pair — delete one finding, introduce another — is not caught.
# Diff stability is worth more than catching that case; the alternative churns
# the baseline on every edit above a finding.
#
# A baseline exists because the standalone binary has no per-package scoping
# and honours no //nolint directive, so the only alternatives would be to
# disable an analyzer for the whole module or to leave the tree ungated.
# Read hack/antislop-baseline.txt before adding to it: every entry there is a
# claim that `any` is the honest type at that spot, not a to-do.

set -euo pipefail

cd "$(dirname "$0")/.."

ANTISLOP_VERSION="${ANTISLOP_VERSION:-latest}"
BASELINE="hack/antislop-baseline.txt"

if [ -n "${ANTISLOP:-}" ]; then
	bin=("$ANTISLOP")
elif command -v antislop >/dev/null 2>&1; then
	bin=("$(command -v antislop)")
else
	bin=(go run "github.com/sanketsudake/antislop/cmd/antislop@${ANTISLOP_VERSION}")
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

# summarize turns raw findings into the baseline's "<count> <file> <analyzer>"
# form, sorted so the file is stable across runs.
summarize() {
	sed -E 's#^\./##; s#^'"$PWD"'/##; s#:[0-9]+:[0-9]+: ([a-z]+):.*#\t\1#' |
		sort | uniq -c | awk '{print $1, $2, $3}' | sort -k2,2 -k3,3
}

# run invokes the analyzer. Exit codes follow go/analysis singlechecker:
# 0 = clean, 3 = diagnostics reported (the normal case here). Anything else
# means it did not run at all — 1 for a package load error, 2 for a bad flag,
# 127 for a missing binary. Those must never be mistaken for a clean tree, so
# they abort loudly instead of yielding empty output the gate would read as
# "no findings".
run() {
	local out status
	out="$("${bin[@]}" "${POLICY_FLAGS[@]}" "$@" 2>&1)" && status=0 || status=$?
	case "$status" in
	0 | 3) ;;
	*)
		echo "antislop: analyzer did not run (exit $status):" >&2
		echo "$out" >&2
		exit 2
		;;
	esac
	printf '%s\n' "$out"
}

case "${1:-}" in
--list)
	run ./...
	;;
--update)
	{
		echo "# antislop findings accepted for this repository."
		echo "#"
		echo "# Format: <count> <file> <analyzer>. No line numbers on purpose — an"
		echo "# edit above a finding must not churn this file. Regenerate with"
		echo "# 'hack/antislop.sh --update'; the gate is 'hack/antislop.sh'."
		echo "#"
		echo "# Everything listed here is pkg/workflow, and all of it is one claim:"
		echo "# the workflow engine addresses ARBITRARY USER JSON with JSONPath"
		echo "# (RFC-0022, Step Functions parity), so there is no named type to"
		echo "# decode into — the schema belongs to the user, not to us. Wrapping"
		echo "# the tree was designed and rejected: a json.RawMessage-backed"
		echo "# Document re-decodes the whole document per Choice condition, and a"
		echo "# generic Value[any] launders the empty interface through a type"
		echo "# parameter without adding any evidence. Each narrowing site here is"
		echo "# a comma-ok or a type switch whose default branch is correct."
		echo "#"
		echo "# The engine-owned shapes that DO have a fixed schema are typed:"
		echo "# see catchError / branchErrorCause in pkg/workflow/fold.go. The two"
		echo "# noknownwidening entries are those structs being handed to"
		echo "# expr.Path.SetResult, which takes any because the tree it writes"
		echo "# into is user JSON."
		echo "#"
		echo "# Adding a line here is a design claim. Justify it in review."
		run ./... | summarize
	} >"$BASELINE"
	echo "wrote $BASELINE"
	;;
"")
	current="$(run ./... | summarize)"

	# One awk pass: load the baseline, then fail on any pair that is absent
	# from it or exceeds its count. A pair that SHRANK is fine.
	if ! awk '
		NR==FNR {
			if ($0 !~ /^#/ && NF == 3) allowed[$2 SUBSEP $3] = $1
			next
		}
		NF == 3 {
			a = ($2 SUBSEP $3) in allowed ? allowed[$2 SUBSEP $3] : 0
			if ($1 > a) {
				printf "antislop: %s: %s: %s finding(s), baseline allows %s\n", $2, $3, $1, a
				bad = 1
			}
		}
		END { exit bad ? 1 : 0 }
	' "$BASELINE" <(printf '%s\n' "$current"); then
		echo
		echo "New antislop findings. Fix them, or — if 'any' is genuinely the honest"
		echo "type there — run 'hack/antislop.sh --update' and justify the new"
		echo "baseline entries in review."
		exit 1
	fi
	echo "antislop: no findings beyond the accepted baseline ($BASELINE)"
	;;
*)
	run "$@"
	;;
esac

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
# baselined pair grows. It never passes silently on a NEW finding anywhere.
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
		sort | uniq -c | awk '{printf "%s %s %s\n", $1, $2, $3}' | sort -k2,2 -k3,3
}

run() {
	# antislop exits non-zero when it reports findings; that is the normal case
	# here, so the exit status is checked by the caller via the output instead.
	"${bin[@]}" "${POLICY_FLAGS[@]}" "$@" 2>&1 || true
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
	baseline="$(grep -v '^#' "$BASELINE" | grep -v '^[[:space:]]*$' || true)"

	# Report any pair whose count exceeds the baseline (absent == 0).
	failed=0
	while read -r count file analyzer; do
		[ -z "${file:-}" ] && continue
		allowed="$(awk -v f="$file" -v a="$analyzer" '$2==f && $3==a {print $1}' <<<"$baseline")"
		allowed="${allowed:-0}"
		if [ "$count" -gt "$allowed" ]; then
			echo "antislop: $file: $analyzer: $count finding(s), baseline allows $allowed"
			failed=1
		fi
	done <<<"$current"

	if [ "$failed" -ne 0 ]; then
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

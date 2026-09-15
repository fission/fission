#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

# Model-check the RFC-0021/0022 protocol specs with TLC.
#
# The "green" configs must pass; each negative config must FAIL with an
# invariant violation — every negative model documents why a guard exists
# (queue-unguarded → the lease-epoch settle guard; eventlogsub-blindwrite → the
# version-CAS cursor commit; quota-nonatomic → the atomic quota counter;
# aliasgc-norecheck → the delete-time alias re-check).
# See docs/rfc/specs/README.md.

set -euo pipefail

TLA2TOOLS_VERSION="${TLA2TOOLS_VERSION:-1.7.4}"
# SHA256 of the tla2tools.jar attached to the v1.7.4 GitHub release — the
# latest STABLE (non-prerelease) tlaplus release. Its asset has been immutable
# since 2024-08-08. Do NOT move this to v1.8.0: that tag is marked prerelease
# and the tlaplus project re-uploads its assets on every master push (the jar
# manifest carries a build date), so a v1.8.0 pin drifts daily and the job
# fails at this checksum step on unrelated PRs (#3685, #3723, then again the
# same afternoon). The pin stays so an UNEXPECTED artifact still fails loudly
# rather than silently running arbitrary downloaded code.
# Verified before pinning: manifest Main-class tlc2.TLC, Implementation-Title
# "TLA+ Tools", Implementation-Vendor "Microsoft Corp.", Implementation-Version
# "2.0 2024-08-08", X-Git-ShortRevision 5a47802, tlc2/TLC.class present,
# 2274532 bytes, downloaded from the official tlaplus/tlaplus v1.7.4 release
# URL; every green and negative config in this script passes on it.
TLA2TOOLS_SHA256="${TLA2TOOLS_SHA256:-936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SPECS_DIR="${REPO_ROOT}/docs/rfc/specs"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "${WORK_DIR}"' EXIT
JAR="${WORK_DIR}/tla2tools.jar"

echo "Downloading tla2tools ${TLA2TOOLS_VERSION}..."
curl -fsSL --retry 3 --retry-delay 2 --retry-all-errors -o "${JAR}" \
  "https://github.com/tlaplus/tlaplus/releases/download/v${TLA2TOOLS_VERSION}/tla2tools.jar"

echo "Verifying checksum..."
if command -v sha256sum >/dev/null 2>&1; then
  echo "${TLA2TOOLS_SHA256}  ${JAR}" | sha256sum -c -
else
  actual="$(shasum -a 256 "${JAR}" | awk '{print $1}')"
  if [[ "${actual}" != "${TLA2TOOLS_SHA256}" ]]; then
    echo "checksum mismatch: got ${actual}, want ${TLA2TOOLS_SHA256}" >&2
    exit 1
  fi
fi

# Copy the specs into the work dir and run TLC there, so its output (states/,
# error-trace *_TTrace_* files) never lands in the tracked docs/rfc/specs/.
cp "${SPECS_DIR}"/*.tla "${SPECS_DIR}"/*.cfg "${WORK_DIR}/"

# tlc runs one config from the isolated work dir; returns non-zero on an
# invariant violation or error.
tlc() {
  ( cd "${WORK_DIR}" && java -XX:+UseParallelGC -jar "${JAR}" -deadlock -config "$1" "$2" )
}

fail=0

for cfg in queue.cfg workflowfold.cfg workflowbranch.cfg eventlogsub.cfg quota.cfg aliasgc.cfg; do
  spec="$(basename "${cfg}" .cfg).tla"
  echo "=== TLC (must pass): ${cfg} ==="
  if tlc "${cfg}" "${spec}"; then
    echo "PASS: ${cfg}"
  else
    echo "FAIL: ${cfg} reported an error but was expected to pass" >&2
    fail=1
  fi
done

# Negative models MUST fail with an invariant violation: each documents why a
# guard exists (queue-unguarded → the lease-epoch settle guard;
# eventlogsub-blindwrite → the version-CAS cursor commit). "cfg:spec" pairs
# because a negative config shares its base spec's .tla.
for pair in "queue-unguarded.cfg:queue.tla" "eventlogsub-blindwrite.cfg:eventlogsub.tla" "quota-nonatomic.cfg:quota.tla" "aliasgc-norecheck.cfg:aliasgc.tla"; do
  cfg="${pair%%:*}"
  spec="${pair##*:}"
  echo "=== TLC (must FAIL): ${cfg} ==="
  neg_out="${WORK_DIR}/${cfg}.out"
  if tlc "${cfg}" "${spec}" >"${neg_out}" 2>&1; then
    echo "FAIL: ${cfg} passed but MUST fail (its guard is not being exercised)" >&2
    cat "${neg_out}" >&2
    fail=1
  elif grep -q "is violated" "${neg_out}"; then
    echo "PASS: ${cfg} failed as expected:"
    grep -E "Invariant .* is violated" "${neg_out}" || true
  else
    echo "FAIL: ${cfg} errored for a non-invariant reason (parse/tooling), not the expected violation" >&2
    cat "${neg_out}" >&2
    fail=1
  fi
done

if [[ "${fail}" -ne 0 ]]; then
  echo "TLC model check FAILED" >&2
  exit 1
fi
echo "TLC model check OK: all green configs pass, the negative configs fail as designed."

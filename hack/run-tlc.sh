#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

# Model-check the RFC-0021/0022 protocol specs with TLC.
#
# The two "green" configs must pass; the negative config (queue-unguarded.cfg,
# EpochGuard = FALSE) must FAIL with an invariant violation — it documents why
# the queue driver's settles are guarded on the lease epoch, not the message id.
# See docs/rfc/specs/README.md.

set -euo pipefail

TLA2TOOLS_VERSION="${TLA2TOOLS_VERSION:-1.8.0}"
# SHA256 of the tla2tools.jar attached to the v1.8.0 GitHub release. NOTE: the
# tlaplus project periodically REBUILDS and re-uploads this release asset (the jar
# manifest carries a build date), so its SHA drifts over time. A checksum mismatch
# here therefore usually means an upstream rebuild, not corruption or tampering —
# re-verify the jar is genuine tla2tools (manifest Main-class tlc2.TLC, Microsoft
# vendor) and bump this pin. The pin stays so an UNEXPECTED artifact still fails
# loudly rather than silently running arbitrary downloaded code.
# Last bumped 2026-07-19 for the upstream rebuild dated 2026-07-18 (verified:
# manifest Main-class tlc2.TLC, tlc2/TLC.class present, from the official
# tlaplus/tlaplus v1.8.0 release).
TLA2TOOLS_SHA256="${TLA2TOOLS_SHA256:-cc4803dce2a8ffaf0f5920a9dc39df4b5ee34ab4cb53fb58ac557277a7e516b3}"

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

for cfg in queue.cfg workflowfold.cfg workflowbranch.cfg eventlogsub.cfg; do
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
for pair in "queue-unguarded.cfg:queue.tla" "eventlogsub-blindwrite.cfg:eventlogsub.tla"; do
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

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
TLA2TOOLS_SHA256="${TLA2TOOLS_SHA256:-33de7da9ce1b7fffb9d1c184021178dbb051747be48504e65c584c423721a32e}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SPECS_DIR="${REPO_ROOT}/docs/rfc/specs"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "${WORK_DIR}"' EXIT
JAR="${WORK_DIR}/tla2tools.jar"

echo "Downloading tla2tools ${TLA2TOOLS_VERSION}..."
curl -fsSL -o "${JAR}" \
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

# tlc runs one config; returns non-zero on an invariant violation or error.
tlc() {
  java -XX:+UseParallelGC -jar "${JAR}" -deadlock -config "${SPECS_DIR}/$1" "${SPECS_DIR}/$2"
}

fail=0

for cfg in queue.cfg workflowfold.cfg; do
  spec="$(basename "${cfg}" .cfg).tla"
  # queue-unguarded and queue share queue.tla; the loop only runs the base specs.
  [[ "${cfg}" == "queue.cfg" ]] && spec="queue.tla"
  echo "=== TLC (must pass): ${cfg} ==="
  if tlc "${cfg}" "${spec}"; then
    echo "PASS: ${cfg}"
  else
    echo "FAIL: ${cfg} reported an error but was expected to pass" >&2
    fail=1
  fi
done

echo "=== TLC (must FAIL): queue-unguarded.cfg ==="
neg_out="${WORK_DIR}/neg.out"
if tlc queue-unguarded.cfg queue.tla >"${neg_out}" 2>&1; then
  echo "FAIL: queue-unguarded.cfg passed but MUST fail (the epoch guard is not being exercised)" >&2
  cat "${neg_out}" >&2
  fail=1
elif grep -q "is violated" "${neg_out}"; then
  echo "PASS: queue-unguarded.cfg failed as expected:"
  grep -E "Invariant .* is violated" "${neg_out}" || true
else
  echo "FAIL: queue-unguarded.cfg errored for a non-invariant reason (parse/tooling), not the expected violation" >&2
  cat "${neg_out}" >&2
  fail=1
fi

if [[ "${fail}" -ne 0 ]]; then
  echo "TLC model check FAILED" >&2
  exit 1
fi
echo "TLC model check OK: both green configs pass, the negative config fails as designed."

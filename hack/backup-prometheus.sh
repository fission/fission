#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0


set -o errexit
set -o nounset
set -o pipefail

if [[ -n "${TRACE-}" ]]; then
    set -o xtrace
fi
NAMESPACE=${1:-monitoring}
RELEASE=${2:-prometheus}
BACKUP_DIR=${3:-/tmp/prometheus}
LABELS="-l app=kube-prometheus-stack-${RELEASE}"

sts_name=$(kubectl -n "$NAMESPACE" get sts "$LABELS" -o custom-columns=:.metadata.name | grep ^"$RELEASE")
echo "Statefulset name: $sts_name"

mkdir -p "$BACKUP_DIR"/prometheus
# Copy snapshot to local
kubectl -n "$NAMESPACE" cp "$sts_name"-0:/prometheus/ "$BACKUP_DIR"/prometheus

mkdir -p "$BACKUP_DIR"/etc/prometheus
kubectl -n "$NAMESPACE" cp "$sts_name"-0:/etc/prometheus "$BACKUP_DIR"/etc/prometheus

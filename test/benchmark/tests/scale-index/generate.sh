#!/bin/bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

# Router endpoint-index scale generator (RFC-0002 scale verification).
#
# Creates N synthetic headless Services + EndpointSlices shaped exactly like
# the executor-created per-function objects (same labels the router's filtered
# informer selects on), WITHOUT running any pods. This isolates the
# router-side scale story — informer cache, index memory, admission latency —
# from pod scheduling limits: a laptop can hold thousands of slices but only
# ~100 pods.
#
# The slices are inert for routing (no triggers reference the fake functions);
# they only exercise the watch path and the index.
#
#   ./generate.sh create 1000   # build fn-scale-0..999 services+slices
#   ./generate.sh churn 200     # rewrite 200 random slices (endpoint flips)
#   ./generate.sh delete        # remove everything (label-selected)
#
# Objects land in namespace $NS (default: default), batched through one
# kubectl apply per 500 docs.

set -euo pipefail

NS="${NS:-default}"
ACTION="${1:?usage: $0 <create|churn|delete> [count]}"
COUNT="${2:-1000}"
SCALE_LABEL="fission-scale-test=true"

emit_pair() {
    local i="$1" ip_a ip_b
    ip_a="10.255.$(( i / 250 )).$(( (i % 250) + 1 ))"
    ip_b="10.254.$(( i / 250 )).$(( (i % 250) + 1 ))"
    cat <<EOF
---
apiVersion: v1
kind: Service
metadata:
  name: fn-scale-${i}
  namespace: ${NS}
  labels:
    fission-scale-test: "true"
    fission.io/managed-by: fission
    functionName: scale-fn-${i}
    functionNamespace: ${NS}
spec:
  clusterIP: None
  selector:
    functionName: scale-fn-${i}
  ports:
  - port: 8888
    targetPort: 8888
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: fn-scale-${i}-1
  namespace: ${NS}
  labels:
    fission-scale-test: "true"
    fission.io/managed-by: fission
    functionName: scale-fn-${i}
    functionNamespace: ${NS}
    kubernetes.io/service-name: fn-scale-${i}
addressType: IPv4
ports:
- port: 8888
  protocol: TCP
endpoints:
- addresses: ["${ip_a}"]
  conditions: { ready: true }
  targetRef: { kind: Pod, name: scale-pod-${i}-a, namespace: ${NS}, uid: "00000000-0000-4000-8000-$(printf '%012d' "$i")" }
- addresses: ["${ip_b}"]
  conditions: { ready: true }
  targetRef: { kind: Pod, name: scale-pod-${i}-b, namespace: ${NS}, uid: "00000000-0000-4000-9000-$(printf '%012d' "$i")" }
EOF
}

case "$ACTION" in
create)
    batch=/tmp/scale-batch.yaml
    : > "$batch"
    n=0
    for i in $(seq 0 $((COUNT - 1))); do
        emit_pair "$i" >> "$batch"
        n=$((n+1))
        if [ $((n % 250)) -eq 0 ]; then
            kubectl apply -f "$batch" --server-side --force-conflicts > /dev/null
            echo "applied $n/$COUNT"
            : > "$batch"
        fi
    done
    [ -s "$batch" ] && kubectl apply -f "$batch" --server-side --force-conflicts > /dev/null
    echo "created $COUNT services + slices"
    ;;
churn)
    # Rewrite COUNT random slices (within TOTAL created, default 1000): flip
    # one endpoint's readiness so the router rebuilds those entries
    # (quarantine-clear + COW swap path under storm).
    TOTAL="${TOTAL:-1000}"
    for _ in $(seq 1 "$COUNT"); do
        i=$((RANDOM % TOTAL))
        kubectl -n "$NS" patch endpointslice "fn-scale-${i}-1" --type=json \
            -p "[{\"op\":\"replace\",\"path\":\"/endpoints/0/conditions/ready\",\"value\":$( ((RANDOM % 2)) && echo true || echo false )}]" >/dev/null 2>&1 || true
    done
    echo "churned ~$COUNT slices"
    ;;
delete)
    kubectl -n "$NS" delete endpointslices,services -l "$SCALE_LABEL" --wait=false
    echo "deletion issued (label-selected)"
    ;;
*) echo "usage: $0 <create|churn|delete> [count]" >&2; exit 1 ;;
esac

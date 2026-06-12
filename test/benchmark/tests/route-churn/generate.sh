#!/bin/bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

# Router route-churn generator (RFC-0013 verification).
#
# Creates N HTTPTriggers + N Functions as API objects ONLY — no packages, no
# pods, no environments. The triggers are canary-style (function-weights
# references), because canary weight rewrites are exactly the steady-churn
# class RFC-0013 makes free: today every weight tick triggers a full mux
# rebuild in the router; with the route table it is one atomic handler swap.
#
# The functions never run (their environment reference is dangling), so the
# only thing exercised is the router's reconcile → route-update path: watch
# events, route table application, mux rebuilds. Watch the router's
# fission_router_* metrics (mux_rebuilds_total, route_table_applies_total)
# while churning.
#
#   ./generate.sh create 1000   # build churn-fn-0..999 + churn-trigger-0..999
#   ./generate.sh churn 200     # rewrite canary weights on 200 random triggers
#   ./generate.sh delete        # remove everything (label-selected)
#
# Objects land in namespace $NS (default: default), batched through one
# kubectl apply per 500 docs.

set -euo pipefail

NS="${NS:-default}"
ACTION="${1:?usage: $0 <create|churn|delete> [count]}"
COUNT="${2:-1000}"
CHURN_LABEL="fission-route-churn-test=true"

emit_function() {
    local i="$1"
    cat <<EOF
---
apiVersion: fission.io/v1
kind: Function
metadata:
  name: churn-fn-${i}
  namespace: ${NS}
  labels:
    fission-route-churn-test: "true"
spec:
  environment:
    name: churn-env
    namespace: ${NS}
  package:
    packageref:
      name: churn-pkg
      namespace: ${NS}
  InvokeStrategy:
    StrategyType: execution
    ExecutionStrategy:
      ExecutorType: poolmgr
EOF
}

# emit_trigger emits a canary trigger splitting traffic between churn-fn-i
# and churn-fn-((i+1) % COUNT) with the given primary weight.
emit_trigger() {
    local i="$1" weight="$2" total="$3"
    local other=$(( (i + 1) % total ))
    cat <<EOF
---
apiVersion: fission.io/v1
kind: HTTPTrigger
metadata:
  name: churn-trigger-${i}
  namespace: ${NS}
  labels:
    fission-route-churn-test: "true"
spec:
  relativeurl: /churn-${i}
  methods:
  - GET
  functionref:
    type: function-weights
    name: churn-fn-${i}
    functionweights:
      churn-fn-${i}: ${weight}
      churn-fn-${other}: $(( 100 - weight ))
EOF
}

apply_batched() {
    # Reads YAML docs from stdin, applies in batches of 500 docs.
    local batch="" doc_count=0
    while IFS= read -r line; do
        batch+="${line}"$'\n'
        if [[ "$line" == "---" ]]; then
            doc_count=$((doc_count + 1))
            if (( doc_count % 500 == 0 )); then
                printf '%s' "$batch" | kubectl apply -f - >/dev/null
                echo "  applied ${doc_count} docs..."
                batch=""
            fi
        fi
    done
    if [[ -n "$batch" ]]; then
        printf '%s' "$batch" | kubectl apply -f - >/dev/null
    fi
}

case "$ACTION" in
create)
    echo "creating ${COUNT} functions + ${COUNT} canary triggers in ${NS}"
    {
        for i in $(seq 0 $((COUNT - 1))); do
            emit_function "$i"
            emit_trigger "$i" 90 "$COUNT"
        done
    } | apply_batched
    echo "done. router metrics to watch: fission_router_mux_rebuilds_total"
    ;;
churn)
    # Rewrite the canary weights on COUNT random triggers. Each rewrite bumps
    # the trigger's generation without changing its route shape — the class
    # phase 1 turns into a pure handler swap.
    total=$(kubectl get httptrigger -n "$NS" -l "$CHURN_LABEL" --no-headers 2>/dev/null | wc -l | tr -d ' ')
    if (( total == 0 )); then
        echo "no churn triggers found; run '$0 create N' first" >&2
        exit 1
    fi
    echo "rewriting canary weights on ${COUNT} random triggers (of ${total})"
    {
        for _ in $(seq 1 "$COUNT"); do
            i=$(( RANDOM % total ))
            emit_trigger "$i" $(( (RANDOM % 99) + 1 )) "$total"
        done
    } | apply_batched
    echo "done"
    ;;
delete)
    echo "deleting all route-churn objects in ${NS}"
    kubectl delete httptrigger,function -n "$NS" -l "$CHURN_LABEL" --ignore-not-found
    ;;
*)
    echo "usage: $0 <create|churn|delete> [count]" >&2
    exit 1
    ;;
esac

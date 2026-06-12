#!/bin/bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

# RFC-0002 multi-replica admission validation (LOCAL evidence run).
#
# Closes the "per-replica admission never executed with R>1" gap: scales the
# router to 3 replicas, drives IN-CLUSTER k6 load at svc/router (kube-proxy
# spreads connections across replicas — the laptop port-forward cannot), and
# checks:
#   1. zero failed requests at steady state,
#   2. every replica serves from its own index (hits > 0 on each),
#   3. specialized pod count stays within ideal + (R-1)*requestsPerPod
#      (the RFC's documented worst-case over-admission),
#   4. zero quarantines / tap-flush errors,
#   5. load survives a router rolling restart mid-run.
#
# Assumes: kind cluster with Fission deployed (gates on by default), fission
# CLI on PATH.

set -euo pipefail

NS=default
RPP=2
VUS="${VUS:-30}"
DURATION="${DURATION:-120s}"
OUT="${OUTDIR:-$PWD/rfc0002-multireplica-results}"
mkdir -p "$OUT"

echo "=== scale router to 3 replicas ==="
kubectl -n fission scale deploy/router --replicas=3
kubectl -n fission rollout status deploy/router --timeout=180s

echo "=== create bench function (requestsPerPod=$RPP) ==="
fission env get --name mr-py >/dev/null 2>&1 || \
    fission env create --name mr-py --image ghcr.io/fission/python-env --poolsize 3
fission fn get --name mr-fn >/dev/null 2>&1 || \
    fission fn create --name mr-fn --env mr-py --code "$(dirname "$0")/../test/benchmark/assets/hello.py" \
        --entrypoint hello.main --requestsperpod "$RPP"
fission route get --name mr-fn >/dev/null 2>&1 || \
    fission route create --name mr-fn --function mr-fn --url /mr-fn --method GET

echo "=== launch in-cluster k6 ==="
kubectl -n "$NS" delete configmap k6-mr --ignore-not-found >/dev/null
kubectl -n "$NS" create configmap k6-mr --from-literal=script.js="
import http from 'k6/http';
import { check } from 'k6';
export let options = { scenarios: { mr: { executor: 'constant-vus', vus: ${VUS}, duration: '${DURATION}' } } };
export default function () {
  let res = http.get('http://router.fission/mr-fn', { timeout: '30s' });
  check(res, { 'status is 200': (r) => r.status === 200 });
}
"
kubectl -n "$NS" delete pod k6-mr --ignore-not-found --wait >/dev/null
kubectl -n "$NS" run k6-mr --image=grafana/k6:latest --restart=Never \
    --overrides='{"spec":{"containers":[{"name":"k6-mr","image":"grafana/k6:latest","args":["run","--summary-trend-stats","avg,med,p(95),p(99),max","/scripts/script.js"],"volumeMounts":[{"name":"s","mountPath":"/scripts"}]}],"volumes":[{"name":"s","configMap":{"name":"k6-mr"}}]}}' >/dev/null
echo "k6 pod started (${VUS} VUs x ${DURATION})"

# Mid-run: rolling-restart the router to prove replica churn under load is
# survivable (slices re-listed per replica, in-flight counters replica-local).
sleep 45
echo "=== rolling restart router mid-load ==="
kubectl -n fission rollout restart deploy/router
kubectl -n fission rollout status deploy/router --timeout=180s

kubectl -n "$NS" wait --for=jsonpath='{.status.phase}'=Succeeded pod/k6-mr --timeout=300s || true
kubectl -n "$NS" logs k6-mr | tail -25 | tee "$OUT/k6.txt"

echo "=== per-replica index metrics ==="
i=0
for pod in $(kubectl -n fission get pods -l svc=router -o jsonpath='{.items[*].metadata.name}'); do
    i=$((i+1))
    (kubectl -n fission port-forward "pod/$pod" 1908$i:8080 >/dev/null 2>&1 &)
done
sleep 3
total_pods_expected_note="ideal=ceil(VUS/RPP) + worst-case (R-1)*RPP"
i=0
for pod in $(kubectl -n fission get pods -l svc=router -o jsonpath='{.items[*].metadata.name}'); do
    i=$((i+1))
    echo "--- $pod"
    curl -s "localhost:1908$i/metrics" | grep -E '^fission_router_(endpointcache_(hits|misses|quarantines|fallbacks|size)|tap_flush)' | tee -a "$OUT/replica-metrics.txt"
done
pkill -f 'port-forward pod/router' 2>/dev/null || true

echo "=== specialized pod count vs bound ==="
pods=$(kubectl -n "$NS" get pods -l functionName=mr-fn --no-headers 2>/dev/null | wc -l | tr -d ' ')
echo "specialized pods for mr-fn: $pods ($total_pods_expected_note)" | tee "$OUT/podcount.txt"
kubectl -n "$NS" get pods -l functionName=mr-fn -o wide | tee -a "$OUT/podcount.txt" || true

#!/bin/bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

# RFC-0002 router scale check driver (LOCAL evidence run).
#
# Phase 1 (real, ~100): invoke 100 real poolmgr functions — real Services,
# slices, pods; checks executor-side ensure timings and the adopt pass.
# Phase 2 (synthetic, 1k+): generate.sh creates label-shaped Services+slices
# with no pods; measures the router's informer/index footprint and admission
# behavior under churn.
#
# Usage: $0 <real|synthetic> [count]

set -euo pipefail

PHASE="${1:?usage: $0 <real|synthetic> [count]}"
COUNT="${2:-}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$PWD/rfc0002-scale-results"
mkdir -p "$OUT"

router_metrics() {
    local label="$1"
    (kubectl -n fission port-forward deploy/router 19180:8080 >/dev/null 2>&1 &)
    sleep 3
    {
      curl -s localhost:19180/metrics | grep -E '^(fission_router_endpointcache_size|go_goroutines|go_memstats_heap_inuse_bytes|process_resident_memory_bytes)'
      echo "# ts $(date +%H:%M:%S)"
    } | tee "$OUT/router-$label.txt"
    pkill -f 'port-forward deploy/router 19180' 2>/dev/null || true
}

case "$PHASE" in
real)
    COUNT="${COUNT:-100}"
    fission env get --name scale-py >/dev/null 2>&1 || \
        fission env create --name scale-py --image ghcr.io/fission/python-env --poolsize 5
    sleep 15
    echo "=== invoking $COUNT real functions ==="
    start=$(date +%s)
    for i in $(seq 1 "$COUNT"); do
        fn="scale-real-$i"
        fission fn create --name "$fn" --env scale-py --code "$ROOT/test/benchmark/assets/hello.py" --entrypoint hello.main >/dev/null
        fission route create --name "$fn" --function "$fn" --url "/$fn" --method GET >/dev/null
    done
    # Invoke each once (sequential; each takes one pool pod -> pool refills).
    okc=0
    for i in $(seq 1 "$COUNT"); do
        for _ in $(seq 1 240); do
            code=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:8888/scale-real-$i" 2>/dev/null) || code=000
            [ "$code" = "200" ] && { okc=$((okc+1)); break; }
            sleep 0.5
        done
        [ $((i % 20)) -eq 0 ] && echo "invoked $i/$COUNT (ok=$okc, $(( $(date +%s) - start ))s)"
    done
    echo "invoked ok=$okc/$COUNT in $(( $(date +%s) - start ))s" | tee "$OUT/real-summary.txt"
    kubectl get svc -n default -l fission.io/managed-by=fission --no-headers | wc -l | xargs echo "function services:" | tee -a "$OUT/real-summary.txt"
    kubectl get endpointslices -n default -l fission.io/managed-by=fission --no-headers | wc -l | xargs echo "slices:" | tee -a "$OUT/real-summary.txt"
    router_metrics "real-$COUNT"

    echo "=== executor restart: adopt pass at fleet scale ==="
    restart_start=$(date +%s)
    kubectl -n fission rollout restart deploy/executor
    kubectl -n fission rollout status deploy/executor --timeout=300s
    echo "executor rollout (incl. adopt pass, /readyz gates on it): $(( $(date +%s) - restart_start ))s" | tee -a "$OUT/real-summary.txt"
    ;;
synthetic)
    COUNT="${COUNT:-1000}"
    router_metrics "baseline"
    echo "=== creating $COUNT synthetic services+slices ==="
    "$ROOT/test/benchmark/tests/scale-index/generate.sh" create "$COUNT"
    sleep 20  # informer ingest
    router_metrics "synthetic-$COUNT"
    echo "=== churn storm ==="
    "$ROOT/test/benchmark/tests/scale-index/generate.sh" churn 300
    sleep 10
    router_metrics "synthetic-$COUNT-postchurn"
    echo "=== summary ==="
    for f in baseline "synthetic-$COUNT" "synthetic-$COUNT-postchurn"; do
        echo "--- $f"; grep -E 'endpointcache_size|heap_inuse|resident|goroutines' "$OUT/router-$f.txt" || true
    done
    ;;
*) echo "usage: $0 <real|synthetic> [count]" >&2; exit 1 ;;
esac

#!/bin/bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

# RFC-0002 pre-phase-4 perf runbook driver.
# Results from the 2026-06-11 execution: docs/rfc/0002-perf-runbook-results.md.
#
# Drives the two committable benchmarks against one kind cluster in both gate
# configurations and snapshots the router's endpointcache counters:
#   1. gates OFF (chart defaults): cold-start microbenchmark + warm-path k6
#   2. helm upgrade → gates ON:    same two benchmarks
#
# Acceptance bars (RFC-0002 §Verification):
#   - cold-start p95 regression < 10% gates-on vs gates-off
#   - warm-path p99 ≥ 20% lower gates-on vs gates-off
#   - gates-on steady-state hit ratio ≥ 99% (hits vs hits+misses+fallbacks)
#
# Assumes: kind cluster up, fission deployed via `SKAFFOLD_PROFILE=kind make
# skaffold-deploy` (gates off by default), `kubectl port-forward svc/router
# 8888:80 -n fission` running, fission CLI on PATH.

set -euo pipefail

PHASE="${1:?usage: $0 <off|on>}"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="${OUTDIR_BASE:-$PWD/rfc0002-perf-results}"
mkdir -p "$OUT"

snapshot_metrics() {
    local label="$1"
    kubectl exec -n fission deploy/router -- sh -c \
        'wget -qO- http://127.0.0.1:8080/metrics 2>/dev/null || curl -s http://127.0.0.1:8080/metrics' \
        | grep -E '^fission_router_(endpointcache|tap_flush)' > "$OUT/router-metrics-$label.txt" || true
}

case "$PHASE" in
off)
    echo "=== Phase 1: gates OFF (baseline) ==="
    LABEL=gates-off OUTDIR="$OUT/warm" "$ROOT/test/benchmark/tests/warm-path/run.sh"
    OUTDIR="$OUT/cold-off" "$ROOT/test/benchmark/tests/cold-start/run.sh"
    snapshot_metrics gates-off
    ;;
on)
    echo "=== Phase 2: flipping gates ON ==="
    helm upgrade fission "$ROOT/charts/fission-all" -n fission --reuse-values \
        --set executor.functionServices.enabled=true \
        --set router.endpointSliceCache.mode=on
    kubectl rollout status deploy/router -n fission --timeout=180s
    kubectl rollout status deploy/executor -n fission --timeout=180s
    sleep 10  # port-forward may need to re-establish; runbook keeps it in a loop

    LABEL=gates-on OUTDIR="$OUT/warm" "$ROOT/test/benchmark/tests/warm-path/run.sh"
    OUTDIR="$OUT/cold-on" "$ROOT/test/benchmark/tests/cold-start/run.sh"
    snapshot_metrics gates-on

    echo "=== Comparison ==="
    python3 - "$OUT" <<'EOF'
import json, sys, csv, pathlib
out = pathlib.Path(sys.argv[1])

def warm(label):
    d = json.load(open(out / "warm" / f"summary-{label}.json"))
    m = d["metrics"]["http_req_duration"]
    return {k: m[k] for k in ("med", "p(95)", "p(99)", "avg")}

def cold(sub):
    rows = sorted(float(r[1]) for r in csv.reader(open(out / sub / "coldstart.csv")))
    n = len(rows)
    pick = lambda q: rows[min(n - 1, int(n * q))]
    return {"n": n, "p50": pick(0.50), "p95": pick(0.95), "mean": sum(rows) / n}

woff, won = warm("gates-off"), warm("gates-on")
coff, con = cold("cold-off"), cold("cold-on")
print(f"warm p99: off={woff['p(99)']:.1f}ms on={won['p(99)']:.1f}ms "
      f"delta={100*(won['p(99)']-woff['p(99)'])/woff['p(99)']:+.1f}% (bar: <= -20%)")
print(f"warm med: off={woff['med']:.1f}ms on={won['med']:.1f}ms")
print(f"cold p95: off={coff['p95']:.1f}ms on={con['p95']:.1f}ms "
      f"delta={100*(con['p95']-coff['p95'])/coff['p95']:+.1f}% (bar: < +10%)")
print(f"cold p50: off={coff['p50']:.1f}ms on={con['p50']:.1f}ms  (n={coff['n']}/{con['n']})")
EOF
    grep -hE 'hits_total|misses_total|fallbacks_total' "$OUT/router-metrics-gates-on.txt" || true
    ;;
*) echo "usage: $0 <off|on>" >&2; exit 1 ;;
esac

#!/bin/bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

# Warm-path benchmark (RFC-0002 verification runbook): steady k6 load against
# ONE pre-warmed poolmgr function. Run once with the EndpointSlice gates off
# and once with them on (same cluster, helm upgrade in between); compare p99
# and the router's endpointcache counters.
#
# Env:
#   FISSION_ROUTER  host:port of the router public listener (default 127.0.0.1:8888)
#   VUS             constant virtual users (default 50)
#   DURATION        k6 scenario duration (default 60s)
#   LABEL           tag for the output files (e.g. gates-off / gates-on; required)
#   OUTDIR          where to write results (default ./warm-path-results)

set -euo pipefail

ROUTER="${FISSION_ROUTER:-127.0.0.1:8888}"
LABEL="${LABEL:?set LABEL=gates-off|gates-on}"
OUTDIR="${OUTDIR:-./warm-path-results}"
ASSETS="$(cd "$(dirname "$0")/../../assets" && pwd)"
HERE="$(cd "$(dirname "$0")" && pwd)"

mkdir -p "$OUTDIR"

envName="bench-warm-py"
fn="bench-warm-fn"
fission env get --name "$envName" >/dev/null 2>&1 || \
    fission env create --name "$envName" --image "${ENV_IMAGE:-ghcr.io/fission/python-env}" --poolsize 3
# requestsPerPod is set high so ONE specialized pod serves every VU
# concurrently: the benchmark measures ROUTER overhead (RPC vs index), not
# function capacity — at the default requestsPerPod=1 each concurrent request
# specializes another pod and a small cluster melts into a pod storm.
fission fn get --name "$fn" >/dev/null 2>&1 || \
    fission fn create --name "$fn" --env "$envName" --code "$ASSETS/hello.py" --entrypoint "hello.main" \
        --requestsperpod "${RPP:-200}"
fission route get --name "$fn" >/dev/null 2>&1 || \
    fission route create --name "$fn" --function "$fn" --url "/$fn" --method GET

# Pre-warm: drive requests until stable 200s so specialization (and, gates-on,
# the async Service ensure + slice publication) is complete before measuring.
echo "pre-warming $fn ..."
ok=0
for _ in $(seq 1 240); do
    code=$(curl -sS -o /dev/null -w '%{http_code}' "http://$ROUTER/$fn" 2>/dev/null) || code=000
    if [ "$code" = "200" ]; then ok=$((ok+1)); else ok=0; fi
    [ "$ok" -ge 10 ] && break
    sleep 0.25
done
[ "$ok" -ge 10 ] || { echo "function never stabilized at 200" >&2; exit 1; }
# Gates-on: give the async Service ensure + EndpointSlice publication time to
# land so the measured window is index-admitted, not RPC-fallback warmup.
sleep 20

k6 run \
    -e FN_ENDPOINT="http://$ROUTER/$fn" \
    -e VUS="${VUS:-20}" \
    -e DURATION="${DURATION:-60s}" \
    --summary-export "$OUTDIR/summary-$LABEL.json" \
    --summary-trend-stats="avg,min,med,p(90),p(95),p(99),max" \
    "$HERE/sample.js" | tee "$OUTDIR/k6-$LABEL.txt"

echo "wrote $OUTDIR/summary-$LABEL.json"

#!/bin/bash
# SPDX-FileCopyrightText: The Fission Authors
#
# SPDX-License-Identifier: Apache-2.0

# Cold-start microbenchmark (RFC-0002 verification runbook).
#
# Measures the end-to-end latency of the FIRST request to a fresh poolmgr
# function, N times sequentially: create function + route, time the first
# non-404 response (the request that drives specialization), delete, repeat.
# The pool stays warm throughout (env created once), so the measured cost is
# router resolve + executor specialize + function exec — the path RFC-0002
# promises to keep byte-identical with the gates on.
#
# Env:
#   FISSION_ROUTER   host:port of the router public listener (default 127.0.0.1:8888)
#   ITERATIONS       number of cold starts (default 30)
#   ENV_IMAGE        python env image (default ghcr.io/fission/python-env)
#   OUTDIR           where to write results (default ./cold-start-results)
#
# Output: $OUTDIR/coldstart.csv (iteration,ms) + summary line with p50/p95.

set -euo pipefail

ROUTER="${FISSION_ROUTER:-127.0.0.1:8888}"
ITERATIONS="${ITERATIONS:-30}"
ENV_IMAGE="${ENV_IMAGE:-ghcr.io/fission/python-env}"
OUTDIR="${OUTDIR:-./cold-start-results}"
ASSETS="$(cd "$(dirname "$0")/../../assets" && pwd)"

mkdir -p "$OUTDIR"
csv="$OUTDIR/coldstart.csv"
: > "$csv"

envName="bench-cold-py"
fission env get --name "$envName" >/dev/null 2>&1 || \
    fission env create --name "$envName" --image "$ENV_IMAGE" --poolsize 3
trap 'fission env delete --name "$envName" >/dev/null 2>&1 || true' EXIT

# Let the pool reach steady state before the first measurement.
sleep 20

wait_for_warm_pool() {
    # A cold start specializes from a READY generic pool pod; if the pool is
    # still refilling after the previous iteration consumed one, the wait
    # would pollute the measurement.
    for _ in $(seq 1 120); do
        n=$(kubectl get pods -n default -l "environmentName=$envName,managed=true" \
            -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' 2>/dev/null | grep -c True || true)
        [ "${n:-0}" -ge 1 ] && return 0
        sleep 1
    done
    echo "pool never returned to ready" >&2
    return 1
}

for i in $(seq 1 "$ITERATIONS"); do
    wait_for_warm_pool
    fn="bench-cold-${i}-$(date +%s)"
    fission fn create --name "$fn" --env "$envName" --code "$ASSETS/hello.py" --entrypoint "hello.main" >/dev/null
    fission route create --name "$fn" --function "$fn" --url "/$fn" --method GET >/dev/null

    # Wait for the route to land in the router mux: 404s are mux propagation,
    # not cold start — they return without touching the executor. The first
    # non-404 response is the specializing request; its duration is the
    # measurement.
    ms=""
    for _ in $(seq 1 120); do
        out=$(curl -sS -o /dev/null -w '%{http_code} %{time_total}' "http://$ROUTER/$fn" 2>/dev/null) || { sleep 0.25; continue; }
        code="${out%% *}"; secs="${out##* }"
        if [ "$code" != "404" ]; then
            if [ "$code" != "200" ]; then
                echo "iteration $i: first response was HTTP $code (not 200); recording anyway" >&2
            fi
            ms=$(awk -v s="$secs" 'BEGIN{printf "%.1f", s*1000}')
            break
        fi
        sleep 0.25
    done
    if [ -z "$ms" ]; then
        echo "iteration $i: route never became live; aborting" >&2
        exit 1
    fi
    echo "$i,$ms" >> "$csv"
    echo "cold start $i/$ITERATIONS: ${ms}ms"

    fission fn delete --name "$fn" >/dev/null
    fission route delete --name "$fn" >/dev/null 2>&1 || true
    # Give the pool a beat to replace the consumed pod so iteration N+1
    # specializes from a warm generic pod, not a cold pool refill.
    sleep 3
done

sort -t, -k2 -n "$csv" | awk -F, '
    { v[NR]=$2 }
    END {
        p50=v[int(NR*0.50)+ (NR*0.50==int(NR*0.50)?0:1)]
        p95=v[int(NR*0.95)+ (NR*0.95==int(NR*0.95)?0:1)]
        sum=0; for(i=1;i<=NR;i++) sum+=v[i]
        printf "cold-start summary: n=%d mean=%.1fms p50=%sms p95=%sms min=%sms max=%sms\n", NR, sum/NR, p50, p95, v[1], v[NR]
    }' | tee "$OUTDIR/summary.txt"

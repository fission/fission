# Analyzing the Prometheus TSDB dump from CI

Each integration leg uploads its whole Prometheus database as `prom-dump-<runId>-<kindversion>` ("Backup prometheus data" step → `hack/backup-prometheus.sh`).
Unlike the pprof artifact (one post-run idle snapshot), this is the **entire run as time-series** — latency histograms, custom fission metrics, and Go runtime metrics per pod at the scrape interval.
Use it for before/after performance comparisons across legs (e.g. a feature-gate-off leg vs a gate-on leg of the same run) and for leak-vs-constant-offset questions pprof cannot answer.

## Spin it up locally

```bash
run=<runId>
gh run download $run -n prom-dump-$run-v1.34.8 -D /tmp/prom-134 -R fission/fission
gh run download $run -n prom-dump-$run-v1.36.1 -D /tmp/prom-136 -R fission/fission

docker run -d --name prom-134 -p 9091:9090 -v /tmp/prom-134/prometheus:/prometheus \
  prom/prometheus --config.file=/etc/prometheus/prometheus.yml --storage.tsdb.path=/prometheus
docker run -d --name prom-136 -p 9092:9090 -v /tmp/prom-136/prometheus:/prometheus \
  prom/prometheus --config.file=/etc/prometheus/prometheus.yml --storage.tsdb.path=/prometheus
sleep 5; curl -s localhost:9091/-/ready   # "Prometheus Server is Ready."
```

The dump contains WAL + chunks; Prometheus replays it on start.
Clean up with `docker rm -f prom-134 prom-136` when done.

## Querying — the traps

- **URL-encode PromQL.** `curl "...?query={pod=~'x'}"` mangles `=~` and Prometheus throws `parse error: unexpected character after '='`. Always `curl -sG ... --data-urlencode "query=..."` (or Python `urllib.parse.urlencode`).
- **Bound your `query_range` window.** `start=0&end=<far-future>` with a small step exceeds the ~11k-points-per-series limit and returns an error or nothing. Find the real window first:

```bash
curl -sG localhost:9091/api/v1/query_range \
  --data-urlencode "query=up{pod=~'router.*|executor.*'}" \
  --data-urlencode "start=$(date -v-8H +%s)" --data-urlencode "end=$(date +%s)" \
  --data-urlencode "step=60"
# min/max timestamps in the result = the run window; query inside it with step=30
```

- **Instant queries evaluate at "now"** with a 5-minute lookback — against a dump whose data ended hours ago they return empty or only the last stale sample. Use `query_range` with explicit start/end, or pass `time=<ts>` inside the data window.
- **`increase(...[<window>s])` evaluated at the window's end** gives totals over the run (e.g. total CPU seconds).

## Per-pod, not sum() — the rollout-overlap artifact

`sum(process_resident_memory_bytes{pod=~'executor.*'})` **double-counts during Deployment rollovers**: old and new pods coexist for seconds-to-minutes, and the serial suite restarts the executor several times (`SetExecutorEnv`, scale-to-zero tests), so a leg that runs more restart-tests "uses" 2× the memory and goroutines of one that doesn't.
This produced a phantom 206 MB RSS / 715 goroutines executor reading that was really two 80 MB pods mid-rollout.

Compare **per-pod** series instead: query without `sum()`, then take avg/max across all samples of all pod generations.
`count by ()` of the matched series tells you how many pod generations the leg churned through — itself a useful signal of how much restart-testing that leg did.

## Leak check from the time series

A constant offset (new informer, new component) and a leak look identical in a single snapshot; the trend separates them:

- Split the run window into thirds; a healthy series has last-third avg ≤ first-third avg (load ramps down at suite end).
- The end-of-run baseline should land exactly on the pprof goroutine snapshot from the same leg (e.g. router `go_goroutines` settles at 72 and `router-goroutine.txt` says `total 72`) — that agreement is the cross-check that both artifacts describe the same steady state.
- Rise-under-load → return-to-flat-baseline = per-request goroutines, fine. Monotonic climb or a baseline that ratchets up after each test phase = leak; switch to the pprof goroutine fingerprint to name the frame.

## Useful queries for fission comparisons

```promql
# router overhead distribution over the functional suite
histogram_quantile(0.5, sum(rate(fission_function_overhead_seconds_bucket[5m])) by (le))

# warm-path traffic mix (RFC-0002 gates on)
fission_router_endpointcache_hits_total
fission_router_endpointcache_misses_total
sum(fission_router_endpointcache_fallbacks_total) by (reason)

# runtime, per pod (no sum() — see above)
go_goroutines{pod=~'router.*'}
go_memstats_heap_inuse_bytes{pod=~'executor.*'} / 1048576
process_resident_memory_bytes{pod=~'router.*'} / 1048576
increase(process_cpu_seconds_total{pod=~'executor.*'}[<run-window>s])
```

Normalize CPU by wall-clock (s/min) when legs have different durations, and remember legs run different test subsets (gate-specific tests skip on the gate-off leg) — flag that next to any cross-leg number.

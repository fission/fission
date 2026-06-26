<!--
SPDX-FileCopyrightText: The Fission Authors

SPDX-License-Identifier: Apache-2.0
-->

# Fission e2e benchmarking suite

A Go-native, end-to-end performance benchmarking suite for Fission (RFC-0020).
It measures execution latency, autoscaling, the build pipeline, and control-plane scale against a running Fission installation, gates the results against thresholds, and can publish a time-series trend.

It replaces the previous bash + k6 scripts.
Load is generated in pure Go (no k6); resource setup uses the version-stable typed clientset, so the same binary can benchmark HEAD or a released chart.

This is a **separate Go module** (`github.com/fission/fission/test/benchmark`).
That keeps it out of the main module's code-coverage instrumentation and dependency graph; it imports the parent via a `replace` directive.

## Layout

```
cmd/fission-benchmark/   # the CLI: run / list / report / compare
pkg/loadgen/             # pure-Go load drivers + HDR-histogram percentiles (no Fission deps)
pkg/cluster/             # kube/fission clients, HMAC signing, Prometheus + pprof capture
pkg/harness/             # per-run Env + per-scenario Scope (resource lifecycle, readiness)
pkg/scenario/            # the scenario catalog
pkg/report/              # results schema, threshold gate, summary, trend, compare
config/                  # scenarios.default.yaml + thresholds.yaml
```

## Build

```
cd test/benchmark
go build -o fission-benchmark ./cmd/fission-benchmark
```

## Scenarios

```
./fission-benchmark list
```

| Scenario | Tags | Measures |
|---|---|---|
| `cold-start-poolmgr` / `cold-start-newdeploy` | latency, coldstart | first-request latency (specialization / scale-from-zero) |
| `warm-path` | latency, throughput | steady-state p50/p95/p99 + throughput |
| `concurrency-sweep` | latency, sweep | latency/throughput across concurrency levels |
| `payload-sweep` | latency, sweep | request-copy overhead across body sizes |
| `autoscale-newdeploy` | autoscale | HPA scale-up time + peak replicas under load |
| `build-time-python` | build | builder compile time for a source package |
| `router-index-scale` | controlplane, scale | router footprint under N synthetic Services/EndpointSlices |
| `route-churn` | controlplane, scale | route-table reconcile under many HTTPTriggers |

Select with `--scenarios name1,name2` and/or `--tags smoke,latency`.
The `smoke` tag is a fast subset (cold-start-poolmgr + warm-path).

## Run against a cluster

Any cluster reachable by your kubeconfig works.
Port-forward the router (and, optionally, Prometheus/pprof) first:

```
kubectl port-forward svc/router          8888:80   -n fission &
kubectl port-forward svc/router-internal 8889:8889 -n fission &
# optional, enables server-side capture:
kubectl port-forward svc/prometheus-operated 9090:9090 -n monitoring &

export PYTHON_RUNTIME_IMAGE=ghcr.io/fission/python-env   # scenarios skip if their image is unset

./fission-benchmark run \
  --tags smoke --duration 15s --warmup 5s --concurrency 20 \
  --router-url http://127.0.0.1:8888 \
  --prometheus-url http://127.0.0.1:9090 \
  --out results.json
```

The harness reads the router-internal HMAC secret from the `fission-internal-auth` Secret automatically (or `FISSION_INTERNAL_AUTH_SECRET`); the public-router data path needs no signing.
Each scenario runs in its own dedicated set of resources and cleans them up afterwards, even on failure.

## Report, gate, and compare

```
# Summary + threshold gate (exits non-zero on breach) + trend files:
./fission-benchmark report --in results.json \
  --thresholds config/thresholds.yaml \
  --summary summary.md \
  --trend-smaller trend-smaller.json --trend-bigger trend-bigger.json

# Version-vs-version (or gates on/off) comparison:
./fission-benchmark compare --base v1.26.json --head head.json --fail-on-regression
```

`thresholds.yaml` holds absolute SLOs; `compare` checks relative regressions.
Trend files are in the `benchmark-action/github-action-benchmark` format (latency = `customSmallerIsBetter`, throughput = `customBiggerIsBetter`).

## Configuration

`config/scenarios.default.yaml` sets counts, durations, concurrency, and sweeps; flags override it.
Tune the scale counts (`indexScaleCount`, `routeChurnCount`, `concurrencyLevels`) up for a large multi-node cluster — the defaults are sized for single-node kind.

## CI

`.github/workflows/benchmark.yaml` runs the suite on manual dispatch, a weekly schedule, and a smoke subset on pushes to `main`.
The weekly run also benchmarks released chart versions for baselining and publishes the gh-pages trend.

## Interpreting results

- Cold-start scenarios report a percentile distribution over N sequential iterations; the first iteration is typically the slowest.
- Warm-path/sweep latencies isolate router + proxy overhead (a single warm pod serves the load).
- Control-plane scenarios pair client-side counts with a server-side Prometheus/pprof snapshot under `--artifact-dir`.
- A scenario whose required runtime image is unset is reported as skipped, not failed.

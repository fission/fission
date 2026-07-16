# RFC-0020: End-to-End Benchmarking Suite & Continuous Performance Tracking

- Status: Implemented ([#3542](https://github.com/fission/fission/pull/3542), merged 2026-06-26): the `fission-benchmark` engine + CLI + scenarios + `benchmark.yaml` CI entry in the separate `test/benchmark` module; scenarios extended in [#3550](https://github.com/fission/fission/pull/3550), [#3559](https://github.com/fission/fission/pull/3559).
- Tracking issue: —
- Supersedes: the ad-hoc bash/k6 benchmark assets under `test/benchmark/` (`rfc0002-perf-runbook.sh`, `tests/**/run.sh`, `tests/**/sample.js`, `picasso.go`)
- Targets: Fission v1.N+ (additive tooling; no control-plane or CRD change)
- Requires: no Kubernetes floor change.
  A reachable cluster + kubeconfig for any run.
  CI reuses the existing kind + skaffold `kind-ci` machinery.
  Adds `github.com/HdrHistogram/hdrhistogram-go` to the **separate** `test/benchmark` module only.
- Related: [RFC-0002](0002-endpointslice-native-data-plane.md) (the data-plane gates these benchmarks gate on), [RFC-0013](0013-incremental-router-routes.md) (route-churn metrics), [RFC-0001](0001-oci-native-package-delivery.md) / [RFC-0012](0012-oci-default-package-delivery.md) (OCI vs archive delivery comparison), [RFC-0016](0016-otlp-native-logging-pipeline.md) / [RFC-0019](0019-unified-opentelemetry-observability.md) (the server-side metrics this correlates against).

## Summary

Fission has no repeatable, automated way to measure or track its own performance.
The benchmark assets that exist (`test/benchmark/`) are one-off bash + k6 scripts written to verify RFC-0002 and RFC-0013, hardcoded to a kind cluster and a Python `hello.py`, with no regression gate, no trend tracking, and no way to run them against an arbitrary Fission installation.
The integration test suite was successfully migrated from bash to Go; this RFC does the same for benchmarking.

It proposes a Go-native, e2e benchmarking system built as a **reusable engine library + a portable `fission-benchmark` CLI + a thin `go test -tags=benchmark` CI entrypoint**, all living inside the existing **separate `test/benchmark` Go module** (which keeps it out of the main module's code-coverage instrumentation and dependency graph).
Load is generated in pure Go (open- and closed-loop drivers with HDR-histogram percentiles — no k6, no external load binary).
Resource setup goes through the version-stable typed clientset, so the **same binary** can benchmark HEAD or a released chart (1.26, 1.27, …) to establish baselines.
Every scenario correlates client-side latency/throughput with server-side Prometheus metrics and pprof captures.
Results are emitted as structured JSON, gated against configurable regression thresholds, summarized into the GitHub step summary, and published to a gh-pages time-series dashboard with alert-on-regression.
A new `benchmark.yaml` workflow runs on manual dispatch, a weekly schedule, and a warn-only smoke subset on every commit/PR that touches code, charts, or tests (the same breadth as the integration suite) so a data-path regression is caught against a real cluster before merge.
The every-commit smoke leg also captures control-plane coverage (a `benchmark` Codecov flag); the weekly run is left uninstrumented so its trend numbers are never skewed.

## Motivation

Performance is a first-class concern for a serverless data plane, yet today:

- **The tooling is bash + k6 + a Go chart renderer.**
  `test/benchmark/rfc0002-perf-runbook.sh` drives `tests/cold-start/run.sh` and `tests/warm-path/run.sh` (k6 `sample.js`), snapshots router `/metrics`, and computes deltas with an inline Python one-liner.
  `tests/scale-index/generate.sh` and `tests/route-churn/generate.sh` emit synthetic objects via heredoc YAML.
  `picasso.go` renders k6 JSON to PNG and is the only reason the module carries the GPL-transitive `go-chart`/`freetype` dependency.
- **It is hardcoded and narrow.**
  Router at `127.0.0.1:8888`, namespace `default`, Python `hello.py`, pool size 3 — none parameterised for other clusters or workloads.
- **There is no gate and no history.**
  Acceptance bars live in a runbook a human reads once; nothing fails CI on a regression and nothing tracks the number week-over-week.
- **It cannot benchmark releases.**
  There is no way to point the same harness at Fission 1.26 vs HEAD to see whether a change helped or hurt.
- **It is not the integration suite's quality bar.**
  The integration framework (`test/integration/framework/`) already gives us in-process resource creation, readiness polling, an HMAC-signing router client, and CI plumbing (kind + skaffold + Prometheus + pprof + port-forwards) — none of which the benchmark scripts reuse.

The cost is real: RFC-0002/0013/0014 each hand-rolled verification, the numbers are not comparable across RFCs, and a latency regression on `main` is invisible until a user reports it.

## Goals

- Pure-Go load generation (open-loop rate + closed-loop concurrency) with HDR-histogram percentiles; no k6, no external load tool installed in CI or on user clusters.
- One core engine consumed by a portable CLI (any cluster, kubeconfig-driven) **and** a `go test -tags=benchmark` CI entrypoint — so humans and CI run the identical code.
- Live entirely in the **separate `test/benchmark` module** so it is excluded from main-module code coverage (`-coverpkg=github.com/fission/fission/...`) and keeps benchmark-only deps out of the root graph.
- Version-stable setup (typed clientset) so the same binary benchmarks HEAD and released charts (multi-version baselining: 1.26, 1.27, …).
- Cover four dimensions — execution latency, autoscaling/elasticity, build/package pipeline, control-plane scale & footprint — including their edge cases.
- Correlate every run with server-side Prometheus + pprof captures (leak-vs-offset, cross-check client numbers).
- Configurable threshold gate (absolute SLOs + relative regression bars, including the existing RFC-0002 bars) and a gh-pages trend dashboard with regression alerts.
- Flexible/extensible/maintainable: a small `Scenario` interface + registry; new scenarios are one file; counts/durations/sweeps are config, not code.
- Delete all legacy bash/k6/picasso benchmark code.

## Non-goals (v1)

- Benchmarking the MCP subsystem or AI gateway (defer to a follow-up).
- Cross-Kubernetes-version performance comparison as a CI matrix axis — pin one stable k8s version so the trend signal is not polluted by kubelet/scheduler differences.
- A multi-cluster fan-out / benchmarking-as-a-service controller.
- Replacing Go micro-benchmarks (`go test -bench`) for unit hot paths — this RFC is strictly **e2e**.
- Windows / non-Linux control planes.

## Design

### Approach comparison — packaging

| | A. Library + CLI + go-test | B. Standalone CLI only | C. go-test suite only |
|---|---|---|---|
| Portable to any cluster | Yes (CLI) | Yes (CLI) | No (test binary, framework-bound) |
| CI-native gating | Yes (`go test`/CLI) | Indirect (CI runs binary) | Yes |
| Reuses integration framework patterns | Yes (selectively) | Less | Most |
| Coverage isolation | Yes (separate module) | Yes | Risk of inflating CLI coverage |
| Maintainability / extension | High (one engine, thin drivers) | Medium | Medium |
| Upfront work | Slightly more | Less | Least |

**Recommendation: A.**
A single engine library is the source of truth; the CLI makes it portable for operators on any cluster, and a thin `go test -tags=benchmark` entry runs the same scenarios in CI.
B alone loses easy CI gating and framework reuse; C alone is not shippable to users running other clusters.

### Approach comparison — load engine

| | Pure Go | k6 | Vegeta as library |
|---|---|---|---|
| External binary / JS | None | k6 + JS scripts | None |
| Percentile quality | HDR histogram | Mature | HDR (battle-tested) |
| Coordinated-omission control | Built into open-loop driver | Partial | Yes |
| Dep footprint | 1 small dep (hdrhistogram) | external | heavier dep tree |
| Fits "all Go, no bash" | Yes | No | Yes |

**Recommendation: Pure Go**, hand-rolled drivers over `net/http` with `HdrHistogram/hdrhistogram-go` for stats.
This fully eliminates k6/JS and is self-contained (nothing to install in CI or on a user's cluster).
Vegeta-as-library is a reasonable fallback if hand-rolling proves fiddly, but the driver surface we need is small and we want to reuse the framework's HMAC-signing transport, which is easier with our own client.

### Module layout & coverage isolation

Everything lives in the existing **separate module** `test/benchmark` (`module github.com/fission/fission/test/benchmark`).
A `replace github.com/fission/fission => ../../` lets it import the parent for the typed clientset, the HMAC signer, and `utils.UrlForFunction`, while remaining **outside the root module's build graph**.
Because the root integration leg builds and instruments only root-module packages (`-coverpkg=github.com/fission/fission/...`), the benchmark module is never instrumented — this is how we satisfy "skip benchmark code from coverage" structurally rather than with per-file pragmas.
Setup uses the typed clientset (not the framework's in-process CLI), so it also cannot inflate fission-CLI coverage.

```
test/benchmark/
  go.mod / go.sum            # drop go-chart/freetype; add hdrhistogram; replace -> ../../
  cmd/fission-benchmark/     # cobra CLI: run / list / report / compare
  pkg/
    loadgen/                 # pure-Go drivers + HDR (ZERO fission deps -> unit-testable, reusable)
      openloop.go  closedloop.go  histogram.go  result.go
    cluster/                 # client.go (kubeconfig/in-cluster) hmac.go capture.go (prom+pprof)
    harness/                 # harness.go resources.go (typed clientset) wait.go (readiness)
    scenario/                # coldstart.go warmpath.go autoscale.go buildpkg.go controlplane.go registry.go
    report/                  # result.go thresholds.go summary.go trend.go
  config/                    # scenarios.default.yaml  thresholds.yaml
  suites/                    # bench_test.go (//go:build benchmark) — CI entrypoint
  assets/                    # hello.py (+ per-language fixtures as scenarios need)
  README.md
```

**Layering** (each unit independently understandable/testable):

- `loadgen` — no Fission imports; HTTP load + stats only; tested against `httptest.Server`.
- `cluster` — rest config + fission/kube clientsets + Prometheus/pprof capture; reuses `pkg/auth/hmac` and `utils.UrlForFunction`.
- `harness` — namespace-scoped resource lifecycle via the typed clientset (`pkg/crd.ClientGenerator`, `pkg/generated/clientset`), idempotent teardown, readiness polling (pool-ready / deploy-ready / build-succeeded, mirroring `test/integration/framework` conditions).
- `scenario` — composes harness + loadgen + capture into a measurement behind a small interface:

```go
type Scenario interface {
    Name() string
    Tags() []string                                    // latency | autoscale | build | controlplane | smoke
    Setup(ctx, *harness.Env) error                     // provision resources
    Measure(ctx, *harness.Env) (report.Result, error)  // run load + collect client+server metrics
    Teardown(ctx, *harness.Env) error                  // always runs; idempotent
}
```

- `report` — results schema, threshold evaluation, step-summary, trend JSON; knows nothing about how a result was produced.

### loadgen

- **Open-loop** driver: fire at target RPS independent of in-flight requests (exposes the latency knee and coordinated-omission-free tail).
- **Closed-loop** driver: N goroutines, request→record→repeat (models fixed concurrency / VUs).
- Tuned `http.Transport` (keep-alive pool sized to concurrency; toggle keep-alive off for cold-connection scenarios).
- `HdrHistogram/hdrhistogram-go` → p50/p90/p95/p99/p99.9/max, mean, stddev, achieved RPS, error count/rate, bytes.
- Configurable warm-up window (samples discarded), N repetitions, reported mean ± stddev + coefficient-of-variation, and a minimum-sample guard for statistical honesty.
- Default target: the **public** router (`:8888`) via a real HTTPTrigger URL (the true user data path, no signing).
  Optional internal-listener (`:8889`) mode signs with `pkg/auth/hmac` + `utils.UrlForFunction` (which folds the default namespace) for the publisher path.

### Scenario catalog

Each scenario records **client-side** latency/throughput/errors and the correlated **server-side** snapshot.
Counts/durations/sweeps come from `config/scenarios.default.yaml` and are overridable per-run (so a quick `--tags smoke` subset and the full weekly run share one binary).

**1.
Execution latency (core)**

| Scenario | Method | Key metrics | Acceptance bar |
|---|---|---|---|
| Cold start (poolmgr / newdeploy / container) | create fn+route, single request, record, teardown, repeat N with pool-warmth gating | cold p50/p95/max per executor | RFC-0002: p95 regression < 10% gates-on vs off |
| Warm path steady-state | fixed RPS + concurrency on a pre-warmed fn | p50/p95/p99/p99.9, throughput | RFC-0002: warm p99 ≥ 20% lower gates-on |
| Concurrency / RPS sweep | closed-loop {10,50,100,250,500,1000} + open-loop RPS sweep | latency-vs-concurrency curve, max sustainable RPS | no hard gate (trend) |
| Payload-size sweep | body {1KB,10KB,100KB,1MB} | proxy copy overhead | trend |

Edge cases: concurrent cold starts (exercises the shared `EXECUTOR_SPECIALIZATION_CONCURRENCY` semaphore / head-of-line blocking); keep-alive vs fresh-conn; gates **on vs off** (built-in toggle, not a CI axis); single-pod serialization (high `requestsPerPod`) vs multi-pod; error rate under overload; tail latency under sustained load.
Setup/build/image-pull failures are recorded as errors, never counted as latency samples.

**2.
Autoscaling & elasticity**

- newdeploy HPA scale-up: step load 1→N; time-to-first-new-pod-ready, time-to-stabilize, throughput-recovery curve.
- Scale-down/stabilization after load drop.
- poolmgr pool exhaustion (concurrency > poolsize): queueing + new-pod latency + refill rate.
- Scale-to-zero → cold-start-from-zero (newdeploy minScale=0).
- MQTrigger/KEDA scaler lag (backlog → scale-out latency); scenario-skips if no broker.
- Edge cases: oscillating load (flap detection); newdeploy starving poolmgr via the shared specialization semaphore.

**3.
Build & package pipeline**

- Build time per env (py/node/go/rust/jvm); scenario-skips envs whose builder image env var is unset (mirrors integration `t.Skip`).
- Package-size cold-start overhead sweep {0,1,5,10,15,20 MB}.
- Delivery comparison: OCI image-volume (RFC-0001/0012) vs archive fetch from storagesvc.
- Concurrent builds: buildermgr throughput at K simultaneous pending packages.
- Edge cases: incremental vs fresh build; large-archive fetch (local FS vs S3); bounded wait so a build failure never hangs the run.

**4.
Control-plane scale & footprint**

- Router index scale: synthetic Services + EndpointSlices {1k,5k,10k} created via the clientset (carries forward `scale-index`); measures index memory + admission latency; asserts via `fission_router_endpointcache_*`.
- Route-table churn (RFC-0013): bulk HTTPTriggers + canary weight ticks; mux-rebuild rate, route-apply latency, `fission_router_route_resync_drift_total` (bar zero).
- Route resolution at scale (many triggers).
- Footprint under load: control-plane CPU/mem/goroutine/GC/workqueue over a sustained run (Prometheus range + pprof before/after); leak-vs-constant-offset distinction.
- Edge cases: router restart → resync recovery time; informer resync cost; executor accounting correctness under churn.

**Cross-cutting e2e edge cases (all scenarios)**

- Cold cluster vs warmed start state (recorded).
- Post-run leak check (namespace + synthetic objects gone) fails the run.
- Idempotent, resumable, namespace-isolated runs (dedicated `fission-bench-<runid>` namespace, unique resource IDs).
- NetworkPolicy: the CI client reaches the cluster via port-forward (external), so the `:8889` `from`-allowlist gotcha does not block it; internal-path scenarios still sign with HMAC.
- Cluster-sizing note in the README: kind single-node caps the scale numbers; big-cluster runs dial counts up via config.
- Failure budget: a scenario that fails to provision is recorded as an error and skipped; the suite continues.

### Metrics taxonomy

- **Client-side** (loadgen): latency percentiles, achieved RPS, error rate, in-flight concurrency.
- **Server-side** (Prometheus range queries via `cluster/capture`): router request-duration histograms, executor specialization time, `fission_router_endpointcache_*` hit ratio, mux-rebuild/route-apply counters, control-plane CPU/mem/goroutines/GC/workqueue depth.
- **pprof**: heap + goroutine snapshots before/after heavy scenarios (reusing the push_pr.yaml capture pattern).
- **k8s**: pod count over time, scale events, scheduling latency.

### Multi-version baselining

- CI matrix axis `fission_version: [HEAD, v1.27.x, v1.26.x]` (list configurable).
  - HEAD → `SKAFFOLD_PROFILE=kind-ci make skaffold-deploy`.
  - Released → `helm install fission fission-charts/fission-all --version X.Y.Z` with a CI-equivalent values overlay (NodePort router, Prometheus endpoint, pprof on); a per-version overlay handles any missing value key.
- The same `fission-benchmark` binary (built from HEAD) drives each, since setup uses stable CRD `v1` + the HTTP data path.
- Results are tagged by version; `compare` and the gh-pages trend line up versions over time and flag cross-version regressions.

### report: gate + trend

- `results.json`: run metadata (fission version, chart version, k8s version, git sha, timestamp, cluster info) + per-scenario metric maps.
- `thresholds.yaml`: per-metric bars — absolute SLOs and relative regression bars (incl. the RFC-0002 cold p95 < +10% / warm p99 ≥ −20% bars).
  `report` exits non-zero on breach.
- `summary.md`: `$GITHUB_STEP_SUMMARY` table (scenario × metric × pass/fail × delta).
- `trend.go`: emit `benchmark-action/github-action-benchmark` JSON (`customSmallerIsBetter` for latency, `customBiggerIsBetter` for throughput) → gh-pages time-series + auto-comment + alert-on-regression.

### CLI surface (`fission-benchmark`)

```
fission-benchmark run     --scenarios cold-start,warm-path --tags latency \
                          --namespace fission --kubeconfig ~/.kube/config \
                          --config scenarios.yaml --out results.json
fission-benchmark list                                   # scenarios + tags
fission-benchmark report  --in results.json --thresholds thresholds.yaml --summary summary.md
fission-benchmark compare --base v1.26.json --head head.json   # version-vs-version / gates on-off
```

### CI workflow (`.github/workflows/benchmark.yaml`)

Modeled on `push_pr.yaml`'s kind + skaffold setup.

- **Triggers**: `workflow_dispatch` (inputs: scenarios/tags, fission_versions, duration, concurrency, publish_trend); `schedule` weekly (full suite, all versions, publish trend); `push` to `main` and `pull_request`, both path-filtered to code/charts/tests (the integration-suite breadth) → **smoke subset** (short, headline scenarios, warn-only, no trend publish) running on every commit to catch gross regressions cheaply (`workflow_dispatch` can't validate an unmerged workflow — GitHub only dispatches workflows already on the default branch).
- **Coverage**: the every-commit smoke leg builds the control plane with `-cover` and uploads the data-path coverage to Codecov under a `benchmark` flag (added to, not replacing, the integration coverage).
  It is confined to the smoke leg precisely so the authoritative weekly trend run stays uninstrumented and its numbers are not skewed by counter overhead; the `fission-benchmark` CLI is built without `-cover`, so it stays excluded.
- **Matrix**: `fission_version` × one pinned `k8s_version`; `fail-fast: false`.
- **Reused steps**: disk cleanup; setup-go/helm/skaffold/goreleaser; kube-prometheus-stack + metrics-server; CRDs; deploy (skaffold for HEAD / helm for released); DNS `single-request-reopen` hardening; self-healing port-forwards (router 8888 / router-internal 8889); `FISSION_INTERNAL_AUTH_SECRET` extraction; pprof capture; `hack/backup-prometheus.sh`.
- **Bench step**: build the CLI from the sub-module (`cd test/benchmark && go build ./cmd/fission-benchmark`) or `go test -tags=benchmark ./suites/...`; produce `results.json` + `summary.md`.
- **Gate**: `report` non-zero on breach (weekly/dispatch); smoke warn-only.
- **Trend**: `benchmark-action/github-action-benchmark` (SHA-pinned) → gh-pages, alert threshold + auto-comment (weekly/dispatch only).
- **Artifacts** (always): `results.json`, `summary.md`, pprof dumps, Prometheus TSDB backup, kind logs — 5-day retention.

## Backward compatibility & migration

Pure tooling addition; no control-plane, CRD, chart-runtime, or API change.

**Removed** (legacy bash/k6/picasso): `picasso.go`, `Dockerfile`, `rfc0002-perf-runbook.sh`, `tests/**` (`run.sh`/`generate.sh`/`sample.js`), the go-chart/freetype deps, and the WSGI-variant env specs under `assets/envs/`.
**Kept**: `docs/rfc/0002-perf-data/` and `0002-perf-runbook-results.md` as historical RFC-0002 verification **evidence** (the new system regenerates equivalents, but the record stays).
The RFC-0002 `0002-implementation-plan.md` and `README.md` references to the old `tests/cold-start`/`tests/warm-path` paths are updated to point at the new harness.

## Verification

- **loadgen unit tests**: drive an `httptest.Server` with fixed-latency handlers; assert measured percentiles/RPS/error-rate within tolerance; assert warm-up discard + repetition stddev.
- **Local e2e**: kind + `kind-ci` deploy + port-forward; `go build ./cmd/fission-benchmark && ./fission-benchmark run --scenarios cold-start,warm-path --out results.json`; then `report` → metrics populated, gate works.
- **Capture cross-check**: server-side Prometheus latency ≈ client-side; pprof heap/goroutine upload.
- **Coverage check**: run the integration coverage leg; confirm benchmark packages do **not** appear in `integration-coverage.txt` / Codecov.
- **CI dry-run**: `workflow_dispatch` smoke on HEAD → green; then a released-version leg; confirm step summary + gh-pages trend entry + regression-alert path.
- **Leak check**: post-run, benchmark namespace + synthetic Services/EndpointSlices/triggers gone.
- **Legacy removal**: `rg -n 'k6|picasso|rfc0002-perf-runbook' test/` empty; `make code-checks` / `make license-check` pass for new files.

## Phasing

1. Module + `loadgen` (rework `go.mod`; drivers + HDR; unit-tested, no cluster).
2. `cluster` + `harness` (kubeconfig clients, capture, namespace-scoped idempotent lifecycle + readiness).
3. Execution-latency scenarios + `Scenario` interface/registry; first local e2e run.
4. `report` (schema, thresholds gate, step summary, trend JSON) + CLI (`run/list/report/compare`).
5. `benchmark.yaml` (dispatch + weekly + smoke-on-main); HEAD deploy; artifacts; gate; gh-pages trend.
6. Remaining scenarios: autoscaling, build/package, control-plane scale & footprint.
7. Multi-version baselining (`fission_version` matrix + released-chart helm deploy + overlay); seed trend with HEAD + 1.27 + 1.26.
8. Delete legacy bash/k6/picasso; write `test/benchmark/README.md`; update root `CLAUDE.md` + RFC-0002 references.

## Risks & mitigations

- **CI noise / flaky numbers** → warm-up discard + repetitions + CoV reporting; smoke-on-main is warn-only; trend alerts on sustained % regression, not single points.
- **kind single-node capacity** → scale counts are config; document sizing; the heavy control-plane-scale scenarios use *synthetic* objects (no pods) as today.
- **Released-chart value drift** → per-version values overlay; `compare` tolerates missing scenarios.
- **Cost** → full suite weekly/dispatch only; main pushes run a cheap subset.
- **Coordinated omission** → open-loop driver measures send-time-based latency.

## Alternatives considered

- **Keep k6** — rejected: keeps an external binary + JS, against the all-Go goal; harder to reuse the HMAC transport.
- **go-test-only suite** — rejected as the sole form: not shippable to operators on other clusters (a stated requirement).
- **External SaaS load tool / k6 Cloud** — rejected: not self-contained, not runnable on arbitrary user clusters.
- **Store results in-repo instead of gh-pages** — rejected: churns git history; gh-pages + github-action-benchmark is the idiomatic trend store.

## Future work

- MCP / AI-gateway benchmarks (RFC-0011).
- `--remote` style benchmarking from inside the cluster (avoids port-forward overhead for very high RPS).
- Cross-k8s-version comparison once the trend baseline is stable.
- Automatic PR-comment with a per-PR micro-suite on perf-labeled PRs.
- Deployment-package-size sweep: the legacy `package-size` benchmark varied the deploy archive size (and so cold-start/fetch cost); the current `payload-sweep` only varies the request body.
  Restoring it needs storagesvc upload for archives above the 256 KiB literal limit.
- Route-churn canary weights: the legacy `route-churn` rewrote canary (function-weights) trigger weights and watched `fission_router_mux_rebuilds_total`; the current scenario only creates single-function triggers.
  A churn phase that flips weights would exercise the RFC-0013 mux-rebuild path more faithfully.
- SHA-pin `benchmark-action/github-action-benchmark` (currently `@v1`).

# Analyzing pprof profiles & performance data from CI

How to pull and read the heap/goroutine profiles the integration-test job captures, separate real leaks from baseline cost, and quantify a fix's before/after delta. This is the workflow used to find and verify the router/executor memory fixes (issue #2775 line of work).

## Where the profiles come from

The integration-test job has a "Collect pprof profiles" step (added with the memory-leak harness) that port-forwards `:6060` on the router and executor pods and curls `/debug/pprof/heap` + `/debug/pprof/goroutine?debug=1` into an artifact.

- Requires `pprof.enabled=true`, which the **kind-ci** skaffold profile sets. So profiles exist on PR/main runs of the `Fission CI` workflow, not on arbitrary branches without that profile.
- Artifact name: `pprof-dumps-<runId>-v<k8sversion>` (one per matrix leg that ran the step; e.g. `-v1.34.0`, `-v1.28.15`).
- Contents: `router-heap.pprof`, `router-goroutine.txt`, `executor-heap.pprof`, `executor-goroutine.txt`.

## Download

```bash
run=$(gh run list --branch <branch> --workflow="Fission CI" --limit 1 --json databaseId -q '.[0].databaseId')
gh api repos/fission/fission/actions/runs/$run/artifacts --jq '.artifacts[].name' | grep -i pprof
rm -rf /tmp/pp && mkdir /tmp/pp
gh run download $run -n pprof-dumps-$run-v1.34.0 -D /tmp/pp
```

`go tool pprof` is available locally (Go toolchain). The `.pprof` files are standard pprof; the goroutine `.txt` is `debug=1` text format.

## Heap analysis

```bash
# Top live-heap consumers (flat = allocated by that frame itself)
go tool pprof -top -inuse_space -nodecount=15 /tmp/pp/router-heap.pprof

# Cumulative — who PULLS the allocations (init chains, GetLogger, etc.)
go tool pprof -top -inuse_space -cum -nodecount=18 /tmp/pp/executor-heap.pprof

# Is a specific allocator still present? (e.g. confirm a fix removed it)
go tool pprof -top -inuse_space /tmp/pp/router-heap.pprof | grep -iE 'quantile|newSummary|newStream'
```

- `-inuse_space` = live heap at capture (what matters for steady-state footprint). `-alloc_space` would show cumulative churn but the harness captures `heap` (inuse).
- Always run `-cum` too: the flat top is often a leaf like `zapcore.newCounters` or `endpoints.init`; the cumulative view names the caller (`loggerfactory.GetLogger`, `runtime.doInit` → `pkg/webhook.init`) that tells you *why* it's allocated.

## Goroutine analysis

```bash
head -1 /tmp/pp/executor-goroutine.txt   # "goroutine profile: total N"

# Group goroutines by their top stack frame, count each
awk '/^[0-9]+ @/ { cnt=$1; getline; sub(/^#[ \t]+0x[0-9a-f]+[ \t]+/,"",$0); gsub(/^[ \t]+/,""); print cnt"\t"$1 }' \
  /tmp/pp/executor-goroutine.txt | sort -rn | head -12

# Count a specific suspect signature (leak fingerprint)
grep -c "updateCPUUtilizationSvc" /tmp/pp/executor-goroutine.txt
```

A leaked goroutine shows up as a high count for one app frame that should be 1-per-something (e.g. `updateCPUUtilizationSvc` was **44** when it leaked per-pool, **1** after the fix).

## Leak vs. baseline — how to classify a top consumer

Not every large heap entry is fixable. Classify before proposing work:

| Pattern | Classification | Action |
|---|---|---|
| `prometheus` summary `newStream`/`quantile.newStream`, scales with label cardinality (path/function/ns) | **Reducible** — convert `SummaryVec`→`HistogramVec` | Fix (see #3412, #3414) |
| `sync.Map`/cache entry count grows with pod/function churn, never deleted | **Leak** | Fix (delete on cleanup path) |
| App goroutine count grows per pool/function/request, no `ctx.Done()` exit | **Leak** | Fix (bound or cancel) |
| `*.init` / package-var allocation (`endpoints.init`, `zapcore.newCounters` via package-level `GetLogger()` vars) | **One-time baseline, never freed but constant** | Usually leave; only fixable structurally |
| k8s scheme registration, protobuf/cbor/regexp init | **Baseline** | Leave |

Key facts that drive the classification:
- **Go runs `init()` and package-var initializers for ALL imported packages**, regardless of which subsystem the multi-headed `fission-bundle` actually starts. So a `var log = loggerfactory.GetLogger()...` at package scope in e.g. `pkg/webhook` costs *every* process (router, executor, …), not just the webhook one.
- **A zap logger's sampler pre-allocates `7 levels × 4096 × 16 B ≈ 448 KB`.** N independent `GetLogger()`/`kzap.New` calls ⇒ N × 448 KB, never freed. (Counting samplers: heap-bytes ÷ 448 KB ≈ number of loggers.)
- **A `SummaryVec` keeps a per-series quantile stream**; cost scales with active label combinations. A histogram uses fixed buckets — far cheaper and aggregatable across replicas.
- The kind CI cluster is **tiny**, so scale-dependent costs (the 10k-pod informer-cache OOM in #2775) **don't appear** in these profiles. Heap totals here are ~16-20 MB. Absence in this profile ≠ absence at scale; reason about cardinality/pod-count separately.

## Quantify a fix: before/after delta

Download an artifact from a run *before* the fix merged and one *after*, then compare:

```bash
# goroutine fingerprint before vs after
grep -c '<leaked-frame>' /tmp/before/executor-goroutine.txt   # e.g. 44
grep -c '<leaked-frame>' /tmp/after/executor-goroutine.txt    # e.g. 1

# heap top before vs after; confirm the allocator is gone / shrunk
go tool pprof -top -inuse_space /tmp/before/router-heap.pprof | grep -i '<allocator>'
go tool pprof -top -inuse_space /tmp/after/router-heap.pprof  | grep -i '<allocator>' || echo "gone"
```

Compare **composition**, not raw totals: goroutine/heap totals vary run-to-run with cluster activity. "this specific frame went 44→1" or "summary streams no longer present" is the durable signal.

## Gotchas

- **No pprof artifact on the run?** The branch/PR didn't build with `pprof.enabled` — only the kind-ci profile sets it. A branch that doesn't change Go/chart/test paths may also skip the integration job entirely (path filters in `push_pr.yaml`).
- **Artifact coverage is per-file best-effort, not guaranteed.** The "Collect pprof profiles" step curls each profile with `|| echo "capture failed"` and uploads with `if-no-files-found: ignore`, so a leg can ship a partial set (router files only) or no artifact at all. Two observed causes: (1) the executor was restarted/scaled by the serial suite right before collection, so `kubectl get pods -l svc=executor ... items[0]` hit a terminating or not-yet-ready pod and both curls failed; (2) a leg whose deploy doesn't expose `:6060` (e.g. the coverage-instrumented v1.32 leg) produces zero files → no artifact at all. Recovery: pull the same files from an **earlier round of the same PR whose Go code is identical** (check the commit delta is workflow/docs-only first) or from another leg.
- **Cross-check point-in-time pprof against the Prometheus dump.** pprof is one post-run idle snapshot; the `prom-dump-<runId>-<ver>` artifact holds the whole run as time-series (`go_goroutines`, heap inuse, RSS, CPU) and is what distinguishes "constant offset" from "growth over time". Workflow: `prometheus-dump-analysis.md`. A healthy pairing lands the Prometheus end-of-run baseline exactly on the pprof goroutine total (e.g. router 72 in both sources).
- **A metrics-registration failure silently breaks more than metrics.** `ServeMetrics` does `metrics.Registry.Register(Registry)` as one atomic call and only *logs* on failure — so one colliding collector drops *all* custom metrics. That starves the canary controller (it reads `fission_function_*` from Prometheus) and stalls `TestCanary`. If `TestCanary` fails on a metrics change, grep the router pod log for `failed to register metrics` / `already exists` before assuming a flake. (Root-caused in the #3412/#3414 line.)
- **controller-runtime already registers Go + process collectors** into the registry `ServeMetrics` serves, so `go_*`/`process_*` are already on `/metrics` — don't re-register them (it panics or collides). That's why the harness adds no runtime collectors of its own.
- Histogram bucket choice matters: a *lifetime* metric (seconds→hours, e.g. `fission_function_running_seconds`) needs `ExponentialBuckets`, not `DefBuckets` (which top out at 10s and dump everything in `+Inf`). A per-request overhead metric is fine on `DefBuckets`.

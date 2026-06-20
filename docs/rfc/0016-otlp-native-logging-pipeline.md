# RFC-0016: Cloud-Native, OTLP-Native Logging Pipeline

- Status: Partially implemented.
  The read path — driver registry + Loki reference adapter + request-ID/trace-ID/level filtering — landed in [#3516](https://github.com/fission/fission/pull/3516).
  The router per-invocation access record landed in [#3517](https://github.com/fission/fission/pull/3517).
  Collection is delegated to an **operator-run external collector** — Fission does not bundle a collector container in the chart; it emits the structured logs an external pipeline ingests.
  A reference OpenTelemetry Collector + Loki wiring exercises the full round-trip on one CI leg (`test/integration/otel/`, not the chart).
  Control-plane OTLP log push, streaming `--follow`, and the InfluxDB deprecation cutover remain.
  See "As implemented".
- Tracking issue: —
- Supersedes: the InfluxDB-v1.x + Fluent-Bit logging path (deprecated by this RFC)
- Targets: Fission v1.N+ (phased; read-path lands first and is independently useful)
- Requires: no Kubernetes floor change; no new dependencies for the implemented scope (the optional OTLP-push follow-up would add the OpenTelemetry Go log SDK).
  The external collector and Loki are **operator-run, opt-in** dependencies, never bundled or required.
- Related: [RFC-0015](0015-invocation-correlation-and-failure-attribution.md) (provides the `X-Fission-Request-ID` this pipeline keys log correlation on), RFC-0017 (planned; the CLI surfaces `--request-id` log queries), [RFC-0008](0008-streaming-invocation-path.md) (streaming responses share the same access record).

## Summary

Fission's logging stack is built on an end-of-life technology — InfluxDB v1.x (EOL December 2024) shipped via a Fluent-Bit DaemonSet whose only output is InfluxDB.
The query layer (`pkg/fission-cli/logdb`) hardcodes two drivers in a `switch`, `--follow` is one-second polling rather than streaming, and there is no per-invocation correlation: a raw user `stdout` line carries no request-ID, so `fission function logs` cannot answer "show me the logs for *this* request."
This RFC re-bases Fission logging on OpenTelemetry conventions: function and control-plane logs are emitted as structured records to **stdout** (the router gains a per-invocation access record), which an **operator-run external collector** ingests and routes to any backend (Loki, Elastic, Datadog, Cloud Logging) — Fission bundles no collector of its own.
`fission function logs` keeps working through a **Loki reference adapter**, gains request-ID / trace-ID / level filtering and real streaming, and the zero-dependency `kubernetes` driver remains the default so a bare install needs nothing new.
InfluxDB and Fluent-Bit are deprecated on a clear migration timeline, not removed out from under anyone.

## Motivation

- **EOL core.**
  `pkg/fission-cli/logdb/influxdb.go` targets InfluxDB v1.x and even carries schema-evolution fallback-column shims (a `TODO` about fluentd→fluent-bit index renames).
  The backend is past end of life; v2.x is not a drop-in.
- **Not generic.**
  `GetLogDB` (`pkg/fission-cli/logdb/logdb.go:70`) is a two-case `switch` over `influxdb` and `kubernetes`; the error string literally reads "now only support influxdb".
  Adding a backend means editing this function and shipping a new driver Fission must maintain.
- **No correlation.**
  `LogFilter` filters by pod and time window only — there is no request-ID, trace-ID, or level field.
  A user's `print()` line is a raw string with no invocation context.
- **Polling, not streaming.**
  `fission function logs --follow` (`pkg/fission-cli/cmd/function/log.go`) re-queries every second, a latency floor and a load source.
- **Default-off, limited fallback.**
  Fluent-Bit + InfluxDB are disabled by default (`influxdb.enabled=false`), so most users fall back to the `kubernetes` driver — direct `Pods().GetLogs()`, bounded by pod retention, single-pod, no search.

Every comparable platform gives structured, per-request, searchable logs (CloudWatch, Cloud Logging).
The cloud-native answer is not "write more backend drivers" — it is to speak OTLP and let the OpenTelemetry Collector fan out to whatever the operator already runs.

## Goals

- Emit **structured** logs (stdout, optionally OTLP push) that an **operator-run external collector** ingests and routes to any backend; do not bundle a collector in the chart.
- Ship a **Loki reference query adapter** so `fission function logs` keeps working against one supported open backend.
- Make the read path filterable by **request-ID / trace-ID / level**, and turn `GetLogDB` into a **registry**.
- Replace polling `--follow` with **real streaming**.
- Define a canonical structured-log **schema** aligned to OpenTelemetry semantic conventions, consuming RFC-0015's `X-Fission-Request-ID` and trace ID.
- **Deprecate** the InfluxDB driver and the Fluent-Bit/InfluxDB config with a documented migration, keeping them working in the interim.

## Non-goals

- Building or operating Loki / Elastic / Datadog — operators own the backend.
- Forcing arbitrary user code to log structured; we make correlation *possible and easy*, not mandatory.
- Replacing the trace/metric pipeline in `pkg/utils/otel/provider.go` — this RFC extends it with logs.
- Shipping the env-image logging helpers themselves (they live in the separate `fission/environments` repo); this RFC defines the contract they implement.

## Design

### Architecture

```
   CONTROL PLANE (fission-bundle)                  FUNCTION PODS (any namespace)
 ┌──────────────────────────────────────┐        ┌────────────────────────────────┐
 │ router → ACCESS RECORD (structured    │        │ env runtime: stdout/stderr      │
 │  stdout): request-id, trace-id, fn.*, │        │  (+ optional structured JSON    │
 │  backend, status, latency             │        │   carrying request-id)          │
 │  — DISPLAY_ACCESS_LOG, opt-in         │        └───────────────┬────────────────┘
 └──────────────────┬───────────────────┘                        │ pod stdout
                    │ pod stdout                                  │
                    └──────────────────┬──────────────────────────┘
                                       ▼
                  ┌────────────────────────────────────────────┐
                  │  EXTERNAL collector — operator-run, NOT       │
                  │  bundled by Fission: OTel Collector (via the  │
                  │  OTel Operator) / Promtail / Vector / Fluent  │
                  │  Bit. Scrapes pod stdout (+ optional OTLP     │
                  │  push), labels by fission.function.* → backend│
                  └───────────────────┬──────────────────────────┘
                      ┌───────────────┼───────────────┐
                      ▼               ▼               ▼
                    Loki          Elastic         Datadog …
                      ▲
                      │ LogQL (query_range)
            ┌─────────┴──────────┐
            │ fission function   │ logdb registry → loki (reference) | kubernetes (zero-dep default)
            │  logs --request-id │
            └────────────────────┘
```

Fission emits the data; the operator's existing pipeline collects it.
There is no Fission-owned collector container.

### 1. Collection — operators run an external collector

Fission does **not** bundle a log collector.
Every Fission component (and every function pod) writes structured records to **stdout**, where the operator's existing log pipeline already collects pod logs and ships them to the backend.
This keeps the chart lean and lets operators own their observability stack (the RFC non-goal "operators own the backend").

Fission's contribution to collection is the data, not the pipeline:

- **Function logs** are the env runtime's `stdout`/`stderr` — collected like any pod's logs and labeled from the function pod's labels (`functionUid`, `functionName`, `functionNamespace`, `environmentName`, `executorType`), which a `k8sattributes`-style processor promotes to the `fission.function.*` labels the read-path adapter queries.
- **The router access record** (section 1a) is the per-invocation correlation key.

An operator wires their collector to scrape the `fission`/function-namespace pod logs (OTel `filelog`+`k8sattributes`, Promtail `kubernetes_sd`, …) and export to Loki (native OTLP endpoint or push), mapping the pod labels to `fission_function_uid` etc. so `fission function logs` finds them.

The legacy in-chart Fluent-Bit→InfluxDB shipper and the `pkg/logger` symlink DaemonSet are retained behind `influxdb.enabled` (default `false`) but deprecated; they are NOT extended.

### 1a. Router access record (implemented)

The router emits one structured access record per invocation — the one place that always sees the request-id, the trace-id, the function identity, and the chosen backend together — from `functionHandler.logAccessRecord` (called by `collectFunctionMetric`).
Fields: `fission.request.id`, `trace_id`, `fission.function.{name,uid,namespace}`, `http.{method,path,status_code}`, `backend`, `retry`, `duration_ms`.
It is opt-in via the existing `DISPLAY_ACCESS_LOG` flag (chart `router.displayAccessLog`, default false) — previously an orphaned env var, now wired — so it adds no per-request log volume unless an operator wants log-based correlation.
`fission function logs --request-id <id>` resolves an invocation to its function and time window via these records once the operator's collector ships them to the backend.

### 2. Control-plane OTLP logs (optional follow-up)

By default control-plane components (router, executor, …) log structured records to **stdout**, which the operator's external collector scrapes like any pod — so no Fission code change is required for collection.

As an **optional** push transport (phase 4), Fission could add a log exporter and `LoggerProvider` to `pkg/utils/otel/provider.go`, mirroring the existing trace exporter (`otlploggrpc` + `go.opentelemetry.io/otel/sdk/log`, a `go.mod` bump), and bridge Fission's `logr`/zap loggers so `logger.Info(...)` also pushes OTLP records (carrying the `trace_id` from `LoggerWithTraceID`) to the operator's `OTEL_EXPORTER_OTLP_ENDPOINT`.
This is additive — the stdout path already works without it — and is deferred so collection stays infra-free by default.

### 3. The hard problem — per-invocation correlation of user log lines

A user's raw `print("hello")` produces a line with no request-ID.
The honest options:

| Option | How | Pros | Cons |
|---|---|---|---|
| (a) Runtime-side structured logging | Env-image helper reads `X-Fission-Request-ID`/`traceparent` from the request and emits JSON `{ts, level, msg, fission.request.id, trace_id}`; the Collector's `json_parser` promotes fields | Exact per-line correlation; carries level | Needs env-image changes (separate repo); only correlates code that *uses the helper* — bare `print()` is not stamped; per-env work |
| (b) Collector-side join by pod + timestamp | Correlate raw lines to the router access log by pod + time window | No runtime change | Fragile: concurrent requests on one pod are ambiguous; clock skew; best-effort only |
| (c) Hybrid: router access record + best-effort enrichment | Router emits one structured **access record** per invocation (request-ID, trace-ID, fn name/uid/ns, status, latency, **target pod**); the query layer joins it to that pod's logs over the request window; env images additionally adopt (a) where feasible | Always-on correlation for *every* invocation with zero user/env change; canonical request→pod→time mapping; (a) layers per-line precision on top | Raw lines within a pod's window are pod+time-correlated (inherits (b)'s ambiguity under true concurrency); precise only where (a) is used |

**Recommendation: (c), with (a) as the precision upgrade.** (c) is the only option that yields a useful answer for arbitrary user code on day one with no env change, because the router is the one place that always sees the request-ID, the trace-ID, and the chosen backend pod together.
The emission point already exists: `functionHandler.collectFunctionMetric` runs once per completed request with `start`, status, function, and the resolved pod in hand.
We add a structured access record there:

```
accessLog.Info("function invoked",
  "fission.request.id", reqID,           // X-Fission-Request-ID (RFC-0015)
  "trace_id", traceID,
  "fission.function.name", fn.Name,
  "fission.function.uid", string(fn.UID),
  "fission.function.namespace", fn.Namespace,
  "fission.target.pod", targetPod,
  "http.status_code", status,
  "duration_ms", duration.Milliseconds())
```

`fission function logs --request-id <id>` then (1) queries the access record to resolve request-ID → `{pod, ns, [start, end]}`, and (2) fetches that pod's logs over the window.
Documented limit, stated not hidden: per-line precision for concurrent requests on one pod requires option (a); the access record covers everything else.
Prioritize (a) for the highest-traffic envs (node, python, go) in the `fission/environments` repo.

### 4. Read path — registry + Loki adapter

Evolve `pkg/fission-cli/logdb/logdb.go`.
`LogFilter` gains additive fields, a streaming interface is added, and `GetLogDB` becomes a registry:

```go
type LogFilter struct {
    // ...existing fields unchanged...
    RequestID string // X-Fission-Request-ID
    TraceID   string
    Level     string // info|warn|error|debug
    Follow    bool
}

type StreamingLogDatabase interface {
    LogDatabase
    StreamLogs(context.Context, LogFilter, chan<- LogEntry) error
}

type Factory func(ctx context.Context, opts LogDBOptions) (LogDatabase, error)

var registry = map[string]Factory{}

func Register(name string, f Factory) { registry[name] = f }

func GetLogDB(dbType string, ctx context.Context, opts LogDBOptions) (LogDatabase, error) {
    f, ok := registry[dbType]
    if !ok {
        return nil, fmt.Errorf("unknown log database %q; supported: %s", dbType, strings.Join(keys(registry), ", "))
    }
    return f(ctx, opts)
}
```

Drivers self-register in `init()`:

- `kubernetes_log.go` registers `kubernetes` — kept as the zero-dependency default.
- New `pkg/fission-cli/logdb/loki.go` registers `loki` — the reference adapter.
- `influxdb.go` registers `influxdb` but prints a deprecation warning.

The Loki adapter implements both `LogDatabase` and `StreamingLogDatabase` over Loki's HTTP API, reusing `util.SetupPortForward` exactly as `influxdb.go` does today (port-forward to the Loki gateway when `LOKI_URL` is unset).
`GetLogs` builds a LogQL query from `LogFilter`:

```logql
{fission_function_uid="<uid>", fission_function_namespace="<ns>"}
  | json
  | fission_request_id="<reqid>"   # when --request-id set
  | trace_id="<traceid>"           # when --trace-id set
  | level="<level>"                # when --level set
```

mapping `Since`/`RecordLimit`/`Reverse` to `start`/`limit`/`direction` on `/loki/api/v1/query_range`; `StreamLogs` uses `/loki/api/v1/tail` (WebSocket) for `--follow`.

### 5. Streaming

Replace the one-second polling loop in `pkg/fission-cli/cmd/function/log.go` with a channel consumer.
The Loki driver streams via `/tail`; the `kubernetes` driver switches `--follow` to the Kubernetes `PodLogOptions{Follow: true}` streaming API.

### 6. Structured-log schema

OpenTelemetry semantic conventions as the base, Fission keys namespaced under `fission.*`.

- Resource attributes (set once per pod): `service.name`, `k8s.pod.name`, `k8s.namespace.name`, `k8s.container.name`, `k8s.node.name`, plus `fission.function.{name,uid,namespace}`, `fission.environment.name`, `fission.executor.type` (from pod labels via `k8sattributes`).
- Log-record attributes (per line): `fission.request.id`, `trace_id`, `span_id`, `severity_text`/`level`, and on the access record `http.status_code` + `duration_ms`.

For Loki, low-cardinality keys (`fission.function.uid`, `fission.function.namespace`, `level`) become labels; `fission.request.id` is kept as **structured metadata** (Loki 3.x), queried via `| json | fission_request_id=`, not promoted to a label, to avoid a cardinality explosion.

## As implemented

The read path (phase 1) and the router access record are implemented; collection is delegated to an operator-run external collector (no Fission-owned collector container).
Concrete surface:

- `pkg/fission-cli/logdb/logdb.go` — `GetLogDB`'s hardcoded two-driver `switch` is now a `Register`/`Factory` registry; drivers self-register in `init()`, so adding a backend is a new file, not a central edit.
  `LogFilter` gains additive `RequestID` / `TraceID` / `Level` correlation fields, and a shared `writeLogEntry` renders identical CLI output for every driver.
- `pkg/fission-cli/logdb/loki.go` (new) — the Loki reference adapter: builds a LogQL `query_range` from the filter (a stream selector on the function labels plus an optional `| json | …` pipeline for request-id/trace-id/level), reusing `util.SetupPortForward` when `LOKI_URL` is unset.
  `buildLogQL` errors rather than emit an empty matcher Loki would reject.
- `pkg/fission-cli/logdb/{kubernetes_log,influxdb}.go` — `kubernetes` (the zero-dependency default) and `influxdb` self-register; the `influxdb` driver prints an end-of-life deprecation warning when selected.
- `pkg/fission-cli/cmd/function/log.go` + `flag` — new `--request-id` / `--trace-id` / `--level` flags wire into the filter; a one-shot query now surfaces a backend error (bad query / auth / unreachable) instead of swallowing it; the CLI warns when a correlation filter is set against a backend that does not index it.
- `pkg/router/metrics.go` (`functionHandler.logAccessRecord`) + `config.go` — the per-invocation access record (section 1a), emitted to router stdout, opt-in via the pre-existing `DISPLAY_ACCESS_LOG` flag (`router.displayAccessLog`, default false) which was an orphaned env var until now.
  Threaded through the router config like `ROUTER_STRUCTURED_ERRORS`.

The Loki adapter queries against the schema in "Structured-log standard" below, which the operator's external collector produces from the function pod labels + the access record — so it is immediately useful for a cluster already running a collector + Loki with that schema.

## Phased implementation

1. **Read-path registry + Loki adapter** — refactor `GetLogDB` to a registry; add `loki.go` (`query_range`); extend `LogFilter`; wire flags in `pkg/fission-cli/flag`.
   Pure CLI change, unit-testable against an `httptest` Loki stub; immediately useful where Loki + a collector already run.
2. **Router access record** (implemented) — emit the per-invocation correlation record in `collectFunctionMetric`, opt-in via `DISPLAY_ACCESS_LOG`.
   Consumes RFC-0015's request-ID.
3. **External-collector wiring** (CI integration implemented) — a reference OpenTelemetry Collector + Loki wiring proves the pipeline end to end on one CI leg (`test/integration/otel/` + `TestFunctionLogsLokiCorrelation`): the Collector tails the router access record, hoists `fission.function.*` to resource attributes, and pushes to Loki's OTLP endpoint, which indexes them as the labels the read path queries.
   The manifests are CI-only — no Fission chart container — and double as operator guidance for pointing an OTel Collector / Promtail / Vector at the access record + function-namespace pod logs.
4. **(Optional) Control-plane OTLP log push** — add an OTLP log exporter/provider to `pkg/utils/otel/provider.go` (`go.mod` bump) so control-plane components can *push* logs to the operator's `OTEL_EXPORTER_OTLP_ENDPOINT` instead of relying on stdout scraping.
   Additive transport; the stdout access record already works without it.
5. **Streaming follow** — add `StreamingLogDatabase`; implement Loki `/tail` and the `kubernetes` follow stream; rewrite the CLI loop.
6. **Env-image structured logging** (separate `fission/environments` repo) — node/python/go helpers per the schema, so user `print()` lines carry the request-id for per-line correlation.
7. **Deprecation cutover** — flip docs/defaults to recommend the external-collector + Loki path; schedule InfluxDB-driver removal a later minor.

## Backward compatibility & migration

- **Read path is additive.**
  The registry is internal; every existing `--db-type` value still resolves (`kubernetes` stays the default, `influxdb` works with a deprecation warning, `loki` is added).
  The new `LogFilter` fields and `StreamingLogDatabase` interface are additive — existing callers compile and behave unchanged.
- **Opt-in infra.**
  The external collector, Loki, and the access record itself are opt-in (the access record via `DISPLAY_ACCESS_LOG`, default off).
  An existing install — including one with `influxdb.enabled=true` — keeps working untouched until the operator opts in.
- **Deprecation window, not a cliff.**
  The `influxdb` driver and the Fluent-Bit/InfluxDB config are deprecated now, keep working (with a `console.Warn`), and are scheduled for removal at least two minors out; the schema-fallback shims are dropped only at removal.
- **Migration path.**
  `influxdb.enabled: true` → point your existing collector (OTel Collector / Promtail / Vector) at the function-namespace pod logs + the router access record (set `router.displayAccessLog: true`), export to Loki, and query with `--db-type loki`.
  Documented in the chart `NOTES.txt` and an upgrade note.
  `--db-type` default stays `kubernetes`, so no scripted CLI usage breaks.
- **No new chart container.**
  The access record reuses the pre-existing `DISPLAY_ACCESS_LOG` env / `router.displayAccessLog` value; no Fission-owned collector is added.

## Test strategy

- **Unit.**
  Registry register/lookup table tests; `loki.go` LogQL construction and response parsing against an `httptest.Server` (no real Loki), mirroring the `influxdb.go` style; access-record field assertions via a test `logr` sink in `functionHandler_test.go`; log-exporter init covered like `provider_test.go`.
- **Integration.**
  The access record is exercised in the standard suite by enabling `DISPLAY_ACCESS_LOG` in kind-ci and asserting the structured `function access` record appears in the router logs after an invocation (the kind-logs artifact).
  A full Loki-backed round-trip — `TestFunctionLogsLokiCorrelation` — runs on one CI leg: a CI-only OpenTelemetry Collector + Loki (manifests in `test/integration/otel/`, **not** the chart) stand in for the operator's pipeline, the test invokes a function with a known `X-Fission-Request-ID`, and asserts `fission function logs --dbtype loki --request-id <id>` returns the access record for exactly that invocation.
  It is gated on `FISSION_TEST_LOKI` so it skips on legs (and locally) without the stack, mirroring the Gateway-API / OCI-registry gates.
  The `kubernetes` driver path needs no backend and stays the default smoke test.
- **Backward compat.**
  Assert the `influxdb` driver still registers and warns; assert the default `--db-type` remains `kubernetes`.

## Success metrics

- `fission function logs --request-id <id>` returns the correct lines for at least one reference env.
- Zero InfluxDB references in a default install; the Collector footprint is ≤ the retired Fluent-Bit sidecar.
- `--follow` latency below the current one-second poll granularity with true streaming.
- Vendor neutrality proven: an operator swaps the exporter to a non-Loki backend (e.g. `otlphttp`/Elastic) with Helm-values changes only.

## Open questions / risks

- **OTel Go log SDK maturity** at the pinned version — verify stability before phase 3; the logr→OTel bridge choice affects how much existing `logger.Info` is captured.
- **Loki label cardinality** — `fission.request.id` must be structured metadata, not a label; document a minimum Loki version.
- **Concurrency limit of (c)** — concurrent requests on one NewDeploy/keep-alive pod are not per-line separable without option (a); documented, not hidden.
- **Companion-RFC coupling** — phase 3's access record degrades to trace-ID-only if RFC-0015's request-ID is not yet propagated; sequence phase 3 after that header lands.
- **Collector RBAC** — `k8sattributes` needs pod-read in the function namespaces; confirm the Collector ServiceAccount matches the per-namespace vs cluster-wide posture the fluentbit role uses today.

# RFC-0016: Cloud-Native, OTLP-Native Logging Pipeline

- Status: Proposed — the read path (driver registry + Loki reference adapter + request-ID/trace-ID/level filtering) is implemented; the collection side (OpenTelemetry Collector packaging + control-plane OTLP logs + router access record) is pending.
  See "As implemented".
- Tracking issue: —
- Supersedes: the InfluxDB-v1.x + Fluent-Bit logging path (deprecated by this RFC)
- Targets: Fission v1.N+ (phased; read-path lands first and is independently useful)
- Requires: no Kubernetes floor change; a minor `go.mod` bump for the OpenTelemetry Go log SDK (`go.opentelemetry.io/otel/sdk/log` + `otlploggrpc`, already adjacent to the vendored trace SDK).
  The OpenTelemetry Collector and Loki are **opt-in** runtime dependencies, never required.
- Related: [RFC-0015](0015-invocation-correlation-and-failure-attribution.md) (provides the `X-Fission-Request-ID` this pipeline keys log correlation on), RFC-0017 (planned; the CLI surfaces `--request-id` log queries), [RFC-0008](0008-streaming-invocation-path.md) (streaming responses share the same access record).

## Summary

Fission's logging stack is built on an end-of-life technology — InfluxDB v1.x (EOL December 2024) shipped via a Fluent-Bit DaemonSet whose only output is InfluxDB.
The query layer (`pkg/fission-cli/logdb`) hardcodes two drivers in a `switch`, `--follow` is one-second polling rather than streaming, and there is no per-invocation correlation: a raw user `stdout` line carries no request-ID, so `fission function logs` cannot answer "show me the logs for *this* request."
This RFC re-bases Fission logging on OpenTelemetry: function and control-plane logs are emitted as structured records over OTLP to an OpenTelemetry **Collector**, and operators route them to any backend (Loki, Elastic, Datadog, Cloud Logging) by swapping one exporter.
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

- Emit **structured** logs over OTLP to an OpenTelemetry Collector; operators select the backend by exporter.
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
   CONTROL PLANE (fission-bundle)                         FUNCTION PODS (any namespace)
 ┌──────────────────────────────────────┐              ┌────────────────────────────────┐
 │ router → ACCESS RECORD (structured):  │              │ env runtime: stdout/stderr      │
 │  request-id, trace-id, fn.*, target   │              │  (+ optional structured JSON    │
 │  pod, status, latency — via OTLP log  │              │   carrying request-id)          │
 │ executor / buildermgr / storagesvc ───┼──OTLP log──┐ └───────────────┬────────────────┘
 └───────────────────────────────────────┘            │                 │ CRI writes
                                                       ▼   /var/log/containers/*.log
                                       ┌───────────────────────────┐     │ symlinked by
                                       │  OpenTelemetry Collector  │◄────┤ pkg/logger DaemonSet
                                       │  (DaemonSet, per node)    │ file │ → /var/log/fission/*
                                       │  receivers: filelog, otlp │ log  │
                                       │  processors: k8sattributes,      │
                                       │    resourcedetection, transform  │
                                       │  exporters: loki | otlphttp | …  │
                                       └───────────┬───────────────┘
                          ┌────────────────────────┼─────────────────────────┐
                          ▼          ▼              ▼              ▼
                        Loki      Elastic        Datadog     Cloud Logging
                          ▲
                          │ LogQL (query_range + /tail WebSocket)
                ┌─────────┴──────────┐
                │ fission function   │ logdb registry → loki (reference) | kubernetes (zero-dep default)
                │  logs --request-id │
                └────────────────────┘
```

Two sources converge in the Collector: control-plane structured logs pushed over OTLP, and function-pod `stdout`/`stderr` scraped from node files by the filelog receiver, both leaving via one operator-chosen exporter.

### 1. Collection — Collector replaces Fluent-Bit; keep `pkg/logger` as the discovery layer

`pkg/logger/logger.go` stays unchanged.
It already solves a real CRI problem: it maps opaque `/var/log/containers/<pod>_<ns>_<container>-<id>.log` files to stable, Fission-filtered symlinks in `/var/log/fission/`, only for valid function pods on the node, with a symlink reaper.
The Collector's `filelog` receiver tails `/var/log/fission/*.log` — which naturally scopes scraping to function pods without a complex include/exclude regex — and `k8sattributes` attaches the pod labels (`functionUid`, `functionName`, `functionNamespace`, `environmentName`, `executorType`) as resource attributes.
Retiring the symlink layer would force the receiver to scrape all of `/var/log/containers` and re-filter, losing the function-only scoping and the established RBAC posture; so we keep it and **no `pkg/logger` Go change is required**.

Packaging:

- New `charts/fission-all/templates/otel-collector/` (DaemonSet container added to the existing logger DaemonSet, ConfigMap, ServiceAccount, RBAC), gated on a new `logging.driver` value.
- New `charts/fission-all/config/otel-collector.yaml` replacing `charts/fission-all/config/fluentbit.conf`: `filelog` receiver (with the `container` operator to parse the CRI/Docker format and recover pod/ns/container) → `k8sattributes` + `resourcedetection` + `transform` (promote `fission.*`) → exporter (default `loki`, a commented `otlphttp`/`debug` block documents swapping).
- The `fluentbit` container and `fluentbit.conf` are retained behind `influxdb.enabled` (still default `false`) but marked deprecated.

### 2. Control-plane OTLP logs

Add a log exporter and `LoggerProvider` to `pkg/utils/otel/provider.go`, mirroring the existing trace exporter, using `otlploggrpc` + `go.opentelemetry.io/otel/sdk/log`.
Bridge Fission's `logr`/zap loggers to the OTel `LoggerProvider` so existing `logger.Info(...)` calls also emit OTLP records carrying the `trace_id` already injected by `LoggerWithTraceID` (`pkg/utils/otel/log.go`).
`InitProvider` returns the augmented shutdown closure; `cmd/fission-bundle/main.go` already calls it once per service.

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

Phase 1 (the read path) is implemented; the rest of the phasing below is pending.
Concrete surface:

- `pkg/fission-cli/logdb/logdb.go` — `GetLogDB`'s hardcoded two-driver `switch` is now a `Register`/`Factory` registry; drivers self-register in `init()`, so adding a backend is a new file, not a central edit.
  `LogFilter` gains additive `RequestID` / `TraceID` / `Level` correlation fields, and a shared `writeLogEntry` renders identical CLI output for every driver.
- `pkg/fission-cli/logdb/loki.go` (new) — the Loki reference adapter: builds a LogQL `query_range` from the filter (a stream selector on the function labels plus an optional `| json | …` pipeline for request-id/trace-id/level), reusing `util.SetupPortForward` when `LOKI_URL` is unset.
  `buildLogQL` errors rather than emit an empty matcher Loki would reject.
- `pkg/fission-cli/logdb/{kubernetes_log,influxdb}.go` — `kubernetes` (the zero-dependency default) and `influxdb` self-register; the `influxdb` driver prints an end-of-life deprecation warning when selected.
- `pkg/fission-cli/cmd/function/log.go` + `flag` — new `--request-id` / `--trace-id` / `--level` flags wire into the filter; a one-shot query now surfaces a backend error (bad query / auth / unreachable) instead of swallowing it; the CLI warns when a correlation filter is set against a backend that does not index it.

The Loki adapter queries against the schema defined in "Structured-log standard" below, which the Collection pipeline (pending) produces — so it is immediately useful for a cluster already running Loki + a collector with that schema, and becomes the default read path once collection lands.

## Phased implementation

1. **Read-path registry + Loki adapter** — refactor `GetLogDB` to a registry; add `loki.go` (`query_range`); extend `LogFilter`; wire flags in `pkg/fission-cli/flag`.
   Pure CLI change, unit-testable against an `httptest` Loki stub; immediately useful where Loki + a collector already run.
2. **Collector packaging** — add `templates/otel-collector/` + `config/otel-collector.yaml`, the `logging.driver` value, RBAC; extend the `kind-opentelemetry` skaffold profile to stand up the Collector + Loki so integration tests have a backend.
   Fluent-Bit retained behind `influxdb.enabled`.
3. **Control-plane OTLP logs + router access record** — add the log exporter/provider to `pkg/utils/otel/provider.go`; bridge logr→OTel; emit the access record in `collectFunctionMetric`.
   Consumes RFC-0015's request-ID (emit only the fields available; degrade to trace-ID-only if it is not yet propagated).
4. **Streaming follow** — add `StreamingLogDatabase`; implement Loki `/tail` and the `kubernetes` follow stream; rewrite the CLI loop.
5. **Env-image structured logging** (separate `fission/environments` repo) — node/python/go helpers per the schema; this repo only defines the contract + the Collector `json_parser`.
6. **Deprecation cutover** — flip docs/defaults to recommend Loki; schedule InfluxDB-driver removal a later minor.

## Backward compatibility & migration

- **Read path is additive.**
  The registry is internal; every existing `--db-type` value still resolves (`kubernetes` stays the default, `influxdb` works with a deprecation warning, `loki` is added).
  The new `LogFilter` fields and `StreamingLogDatabase` interface are additive — existing callers compile and behave unchanged.
- **Opt-in infra.**
  The Collector, Loki, and OTLP log export are opt-in.
  An existing install — including one with `influxdb.enabled=true` — keeps working untouched until the operator opts in.
- **Deprecation window, not a cliff.**
  The `influxdb` driver and the Fluent-Bit/InfluxDB config are deprecated now, keep working (with a `console.Warn`), and are scheduled for removal at least two minors out; the schema-fallback shims are dropped only at removal.
- **Migration path.**
  `influxdb.enabled: true` → set `logging.driver`, enable the Collector, point the exporter at Loki (or any OTLP backend).
  Documented in the chart `NOTES.txt` and an upgrade note.
  `--db-type` default stays `kubernetes`, so no scripted CLI usage breaks.
- **Dependency bump.**
  The `go.mod` bump for the OTel log SDK is internal with no user-facing change.

## Test strategy

- **Unit.**
  Registry register/lookup table tests; `loki.go` LogQL construction and response parsing against an `httptest.Server` (no real Loki), mirroring the `influxdb.go` style; access-record field assertions via a test `logr` sink in `functionHandler_test.go`; log-exporter init covered like `provider_test.go`.
- **Integration** (needs a backend).
  Extend the `kind-opentelemetry` skaffold profile to stand up Collector + Loki; invoke a function, then assert `fission function logs --db-type loki` and `--request-id <id>` return the lines and resolve to the right pod.
  The `kubernetes` driver path needs no backend and stays the default smoke test.
- **Helm.**
  `helm template` golden/lint tests for the new Collector templates under both `influxdb.enabled` and `logging.driver` values; verify RBAC renders per-namespace (matching the existing fluentbit role pattern) and cluster-wide only under cluster tenancy.
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

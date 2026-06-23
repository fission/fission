# RFC-0019: Unified OpenTelemetry Observability

- Status: Proposed.
  Fission's tracing and logging already speak OpenTelemetry; **metrics are the gap** — all 39 metric definitions are hand-built on the Prometheus `client_golang` API, with no OTel metrics SDK in the process.
  This RFC migrates the metrics *instrumentation* to the OpenTelemetry Metrics API behind the OTel→Prometheus **bridge exporter**, so the `/metrics` endpoint, every metric name, every label, and every dashboard stay byte-identical while the platform gains native OTLP metric push and trace exemplars.
  It also trims the OpenTelemetry dependency and runtime footprint (drop `autoprop`, adopt `autoexport`, bump `semconv`, unify the exporter bootstrap) and folds the three signal providers into one initialization.
  No code lands with this RFC; the work is phased into independently shippable PRs (see "Phasing").
- Tracking issue: —
- Supersedes: the prior in-team stance "metrics stay on Prometheus; OTel unifies only at the collection layer."
  That decision predated weighing the OTel→Prometheus bridge exporter, which is what makes a backward-compatible instrumentation migration possible (see "Backward compatibility").
- Targets: Fission v1.N+ (phased; the scaffold lands first and changes nothing observable, then metrics migrate subsystem by subsystem).
- Requires: the OpenTelemetry Go **metrics** packages — `go.opentelemetry.io/otel/metric` (promoted from indirect to direct), `go.opentelemetry.io/otel/sdk/metric`, `go.opentelemetry.io/otel/exporters/prometheus`, `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc`, and `go.opentelemetry.io/contrib/exporters/autoexport`.
  No Kubernetes floor change; no new **bundled** runtime dependency.
  `github.com/prometheus/client_golang` is **not** removed — controller-runtime depends on it and it remains the wire/exposition format; only the instrumentation API moves to OTel.
- Related: [RFC-0015](0015-invocation-correlation-and-failure-attribution.md) (the error-biased sampler and `X-Fission-Request-ID` correlation this builds on), [RFC-0016](0016-otlp-native-logging-pipeline.md) (OTLP-native logging — the log pillar this RFC unifies under one provider), [RFC-0002](0002-endpointslice-native-data-plane.md) (the endpointcache metrics among those being migrated).

## Summary

Fission's observability is two-thirds OpenTelemetry-native and one-third not.
Traces are mature: `pkg/utils/otel/provider.go` builds an OTLP/gRPC tracer with an error-biased sampler, `otelhttp` wraps every server and client hop, and span-attribute helpers exist for the Fission CRDs.
Logs are mature and opt-in: a zap → `otelzap` bridge pushes control-plane records as OTLP when `OTEL_LOGS_ENABLED` is set, carrying the trace ID.
Metrics are the outlier: every one of the 39 metrics is a `prometheus.NewCounterVec` / `NewGaugeVec` / `NewHistogramVec` registered into a shared `prometheus.Registry`, with **zero** OTel metrics SDK in the process — OTLP unification for metrics exists only at the *collection* layer (an external collector scrapes `/metrics` and re-emits OTLP).

This RFC closes that gap and tidies the footprint, in three pillars:

1. **Metrics on the OpenTelemetry Metrics API**, exposed through the OTel→Prometheus bridge exporter so the `/metrics` output is byte-identical (same names, labels, types, buckets, HELP), plus a second reader that pushes the same metrics over OTLP, plus trace exemplars for free.
2. **Aggressive footprint reduction** — replace the hand-rolled per-signal exporter bootstrap with `contrib/exporters/autoexport`, drop `autoprop` (and the four propagator modules it drags in) for an explicit W3C composite, bump `semconv` from the ancient v1.4.0 to current, and move test-only deps out of the direct requires.
3. **One unified provider** — a single initialization in `pkg/utils/otel` that stands up the tracer, meter, and logger providers from one resource and returns one shutdown that flushes all three.

The guiding constraint is backward compatibility: existing Grafana dashboards, alerts, ServiceMonitors, and PodMonitors must keep working untouched, enforced by a golden-file `/metrics` diff test per subsystem.

## Motivation

- **Metrics are not OpenTelemetry.**
  The platform advertises OpenTelemetry support, but metrics never moved.
  Every definition is a `client_golang` collector registered in an `init()` (e.g. `pkg/router/metrics.go:115`, `pkg/executor/metrics/metrics.go:74`), so the one signal operators most rely on for SLOs is the one signal that cannot be pushed natively over OTLP from the process.
- **Metrics and traces do not link.**
  The router already serves OpenMetrics (`promhttp.HandlerOpts{EnableOpenMetrics: true}` in `pkg/utils/metrics/server.go:34`), which is the format that carries exemplars — but `client_golang` only attaches exemplars when the caller plumbs them by hand, which Fission does nowhere.
  A latency spike on `fission_function_overhead_seconds` cannot be clicked through to an exemplar trace.
- **Two metric idioms for one platform.**
  Application metrics use `client_golang`; the collection-layer story uses OTLP.
  An operator who standardizes on an OTLP pipeline has to special-case Fission metrics with a Prometheus-receiver scrape hop, when the rest of the signals arrive over OTLP directly.
- **Dependency and footprint drag.**
  `autoprop` (`pkg/utils/otel/provider.go:130`) exists to let operators pick a propagator from the environment, and to do that it pulls `contrib/propagators/{aws,b3,jaeger,ot}` into the build as indirect modules — when the chart default is, and almost always stays, `tracecontext,baggage`.
  `semconv` is pinned at **v1.4.0** (`pkg/utils/otel/provider.go:24`), many stable-convention releases behind current.
  The exporter bootstrap is hand-rolled and duplicated per signal (`getTraceExporter`, `getLogExporter`), each re-parsing endpoint/insecure flags that the OTel SDK and `autoexport` already standardize.
- **The earlier decision deserves a second look.**
  The team previously chose to keep metrics on Prometheus to avoid breaking dashboards.
  That reasoning was sound *for a naive rewrite*, but it did not account for the OTel→Prometheus bridge exporter, which preserves the exposition exactly while moving the instrumentation API — so the dashboard-churn objection no longer holds.

## Goals

- Migrate all 39 metrics to the OpenTelemetry Metrics API, instrumented through a thin internal meter helper so call sites stay terse and carry `context.Context`.
- Keep the `/metrics` endpoint **byte-identical**: same port (8080), names, label keys and values, metric types, histogram buckets, and HELP text — enforced by a golden-file diff test.
- Emit metrics over **OTLP natively** from the process, alongside (not instead of) the Prometheus scrape, by attaching a second reader to the same meter provider.
- Attach **trace exemplars** to histograms recorded inside sampled spans, so metrics link to traces.
- **Reduce the OpenTelemetry footprint**: one env-driven exporter bootstrap via `autoexport`, an explicit W3C propagator (no `autoprop`), current `semconv`, and test-only deps out of the direct requires.
- **Unify the bootstrap**: one provider initialization for traces, metrics, and logs with one shutdown.

## Non-goals

- Bundling a collector or backend.
  [RFC-0016](0016-otlp-native-logging-pipeline.md) stands: operators run the collector; Fission emits signals.
- Migrating the Go-runtime, process, and controller-runtime collectors off Prometheus.
  These stay native and continue to be served on the same registry (the OTel exporter is just one more collector on it); adding OTel's `contrib/instrumentation/runtime` would **duplicate** the `go_*` and `process_*` series under divergent names and is explicitly excluded.
- Changing any metric's name, labels, type, or semantics.
  This RFC preserves them; new metrics (e.g. exemplar coverage) are out of scope here.
- Fully removing `github.com/prometheus/client_golang`.
  It is a controller-runtime dependency and the exposition format; it stays.
- Re-instrumenting user function code.
  Function-pod metrics are an environment-image concern (`fission/environments`), not this RFC.

## Design

### Current state (what exists, precisely)

| Signal | Status | Where |
|---|---|---|
| Traces | Mature, OTLP/gRPC, error-biased sampler, `otelhttp` on every hop | `pkg/utils/otel/{provider,errorsampler,handler,attributes}.go` |
| Logs | Mature, opt-in OTLP push via `otelzap` bridge | `pkg/utils/loggerfactory`, `pkg/utils/otel/provider.go` |
| Metrics | **Prometheus `client_golang` only; no OTel metrics SDK** | the 10 files below |

The 39 metrics and their definition files:

| File | Count | Representative names |
|---|---|---|
| `pkg/router/metrics.go` | 10 | `fission_function_calls_total`, `fission_function_overhead_seconds` (DefBuckets), `fission_router_routes`, `fission_invocation_failures_total` |
| `pkg/router/endpointcache/metrics.go` | 7 | `fission_router_endpointcache_hits_total`, `..._mode` (gauge), `..._size` (gauge func) |
| `pkg/executor/metrics/metrics.go` | 6 | `fission_function_cold_starts_total`, `fission_function_running_seconds` (ExponentialBuckets(1,2,16)) |
| `pkg/executor/client/client.go` | 2 | `fission_router_tap_flush_errors_total`, `..._notfound_total` |
| `pkg/storagesvc/metrics.go` | 3 | `fission_archives`, `fission_archive_memory_bytes` (gauges) |
| `pkg/buildermgr/metrics.go` | 1 | `fission_buildermgr_oci_publish_total` |
| `pkg/mqtrigger/metrics.go` | 5 | `fission_mqt_subscriptions`, `fission_mqt_message_lag` (gauges) |
| `pkg/utils/metrics/http_metrics.go` | 3 | `http_requests_total`, `http_requests_duration_seconds` (DefBuckets) |
| `pkg/utils/otel/errorsampler.go` | 2 | `fission_error_span_export_failures_total`, `..._drops_total` |

Every file registers into the shared `metrics.Registry` (`pkg/utils/metrics/registry.go`) via an `init()`; `ServeMetrics` (`pkg/utils/metrics/server.go`) composes that registry into controller-runtime's `metrics.Registry` and serves `/metrics` with OpenMetrics enabled.

### Pillar 1 — Metrics on the OpenTelemetry Metrics API

**Provider.**
Add a `MeterProvider` to the unified initialization in `pkg/utils/otel`, built from the same `resource` as the tracer and logger.

**Exposition stays Prometheus, byte-identical.**
Register the OTel Prometheus exporter against the *existing* registry rather than a new one:

```go
promExporter, err := otelprom.New(
    otelprom.WithRegisterer(metrics.Registry), // the same registry ServeMetrics already serves
    otelprom.WithoutUnits(),                    // no _seconds/_bytes/_ratio unit suffixing
    otelprom.WithoutCounterSuffixes(),          // names already carry _total literally
    otelprom.WithoutScopeInfo(),                // no otel_scope_* labels
    otelprom.WithoutTargetInfo(),               // no target_info series
)
mp := sdkmetric.NewMeterProvider(
    sdkmetric.WithResource(res),
    sdkmetric.WithReader(promExporter),
    // explicit-bucket views to match existing histograms — see below
)
otel.SetMeterProvider(mp)
```

The exporter implements `prometheus.Collector` and registers itself into the registry we hand it, so the OTel metrics appear on the same `/metrics:8080` endpoint with no new port and no scrape-config change.
ServiceMonitors and PodMonitors are untouched.

**Instrument mapping.**
A thin internal meter helper (extending `pkg/utils/metrics`) wraps the `Meter` so call sites read like the current ones:

| Prometheus instrument | OTel instrument | Notes |
|---|---|---|
| `CounterVec` (names end `_total`) | `Int64Counter` | keep literal name incl. `_total`; `WithoutCounterSuffixes` prevents `_total_total` |
| `Gauge`/`GaugeVec` (set to a value) | `Int64Gauge`/`Float64Gauge` (synchronous, available since SDK v1.28) | direct map of `.Set()` |
| `GaugeFunc` (e.g. `..._endpointcache_size`) | `Int64ObservableGauge` with callback | the callback reads the live value each collection |
| `HistogramVec` | `Float64Histogram` + explicit-bucket View | buckets must be pinned — see gotcha |

Labels map one-to-one to attributes with the same keys and values (e.g. `function_namespace`, `function_name`, `path`, `method`, `code`).
Because the OTel API is context-first (`counter.Add(ctx, 1, metric.WithAttributes(...))`), the `ctx` already threaded through the router and executor hot paths is what carries the active span — which is what makes exemplars automatic.

**Histogram buckets are the migration's sharpest edge.**
OTel's default histogram aggregation is the exponential/base-2 layout, not Prometheus buckets, so a naive migration silently changes bucket boundaries and breaks `histogram_quantile()` dashboards.
Each histogram must be pinned with an explicit-bucket View:

- `fission_function_overhead_seconds` and `http_requests_duration_seconds` → `prometheus.DefBuckets` boundaries.
- `fission_function_running_seconds` → `ExponentialBuckets(1, 2, 16)` boundaries (1s … ~9h).

These are reproduced with `sdkmetric.WithView(sdkmetric.NewView(..., sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: ...}}))`.

**Native OTLP push, additively.**
Attach a second reader — `sdkmetric.NewPeriodicReader(otlpmetricgrpc-exporter)` — to the same meter provider, gated by the metrics exporter config (default `prometheus`-only; `otlp` opt-in, or both).
One instrumentation then feeds both the `/metrics` scrape and the OTLP push, so an operator on a pure OTLP pipeline no longer needs the Prometheus-receiver scrape hop in `test/integration/otel/metrics-collector.reference.yaml`.

**Exemplars for free.**
The OTel metrics SDK's default exemplar filter records an exemplar (with `trace_id`/`span_id`) whenever a measurement happens inside a sampled span; the Prometheus exporter renders them in the OpenMetrics output Fission already serves.
No per-call-site plumbing — histograms recorded on the request path gain trace exemplars by virtue of being migrated.

**Registry-collision caution.**
`pkg/utils/metrics/registry.go` documents that composing Fission's registry into controller-runtime's is atomic, so a duplicate collector silently drops *every* Fission metric.
The OTel exporter registers as a single collector; the migration removes the per-metric `MustRegister` calls in lockstep with adding the meter instruments, so a metric is never registered twice (once by `client_golang`, once by the exporter) during a subsystem's migration.

### Pillar 2 — Aggressive footprint reduction

- **`autoexport` replaces the hand-rolled exporters.**
  `getTraceExporter` / `getLogExporter` and the new metric exporter collapse into `autoexport.NewSpanExporter`, `autoexport.NewMetricReader`, and the log equivalent, each configured from standard env (`OTEL_TRACES_EXPORTER`, `OTEL_METRICS_EXPORTER`, `OTEL_LOGS_EXPORTER`, `OTEL_EXPORTER_OTLP_PROTOCOL` supporting `grpc` and `http/protobuf`, plus `none`/`console` for tests and air-gapped installs).
  Fission's bespoke endpoint/insecure parsing in `parseOtelConfig` is deleted in favor of the SDK's own `OTEL_EXPORTER_OTLP_*` handling.
- **Drop `autoprop` for an explicit W3C composite.**
  Replace `autoprop.NewTextMapPropagator()` with `propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})`.
  This removes `contrib/propagators/{aws,b3,jaeger,ot}` from the module graph.
  Trade-off (documented in "Backward compatibility"): operators lose the ability to select b3/jaeger via `OTEL_PROPAGATORS`; the default is unchanged.
  If a deployment genuinely needs a non-W3C propagator, it can be reintroduced behind an explicit, narrowly-scoped option rather than dragging all four in by default.
- **Bump `semconv` v1.4.0 → current** (≈ v1.30+).
  Refreshes resource and HTTP attribute keys to the stable conventions (e.g. `http.request.method`, `url.path`).
  This changes some *trace span attribute keys* — see "Backward compatibility".
- **Test-only deps out of direct requires.**
  `go.opentelemetry.io/otel/log/logtest` moves to test scope.

A note of honesty on "footprint": the chosen metrics direction *adds* the OTel metrics SDK and the Prometheus/OTLP metric exporters, which the pure-Prometheus status quo did not carry.
The net win is not fewer total `go.mod` lines — it is **less bespoke Fission code** (one env-driven bootstrap instead of three hand-rolled ones), **fewer default propagator modules**, a **single OTLP client** shared across signals, and **standard, maintained configuration surface** instead of custom parsing.
The RFC reports the exact added/removed modules in the implementation PRs so the trade is auditable.

### Pillar 3 — One unified provider

`InitProvider` becomes a single observability initialization that:

- builds one `resource` (service name, and the standard detectors),
- selects exporters/readers for all three signals via `autoexport`,
- sets the global tracer, meter, and logger providers,
- installs the explicit W3C propagator,
- preserves the RFC-0015 error-biased sampler and error-export processor for traces,
- and returns one shutdown closure that flushes and closes the tracer, meter, and logger providers (today it already does tracer + logger; this adds meter).

The per-subsystem `Start` functions are unchanged — they already receive the logger and inherit the global providers.

### Configuration surface

The Helm `openTelemetry.*` values are preserved; new keys are additive with status-quo defaults:

| Key | Default | Effect |
|---|---|---|
| `openTelemetry.metricsExporter` | `prometheus` | `prometheus` (today's behavior), `otlp`, or `prometheus,otlp` |
| `openTelemetry.logsExporter` | unchanged (off unless `logsEnabled`) | maps to `OTEL_LOGS_EXPORTER` |
| existing `otlpCollectorEndpoint`, `otlpInsecure`, `tracesSampler`, `tracesSamplingRate`, `propagators`, `logsEnabled`, `otlpHeaders` | unchanged | as today |

With every new key at its default, a current install behaves exactly as it does now: Prometheus scrape on `/metrics`, traces to OTLP if an endpoint is set, logs opt-in.

## Backward compatibility

This is the linchpin, and the reason the earlier "keep Prometheus" decision is safe to reverse.

- **`/metrics` is byte-identical.**
  Same endpoint, port, names, label keys/values, metric types, histogram buckets, and HELP text.
  The exporter options (`WithoutUnits`, `WithoutCounterSuffixes`, `WithoutScopeInfo`, `WithoutTargetInfo`) plus explicit-bucket Views plus literal instrument names guarantee it, and a **golden-file diff test per subsystem** locks it: capture the `client_golang` `/metrics` output, migrate, assert the OTel output matches modulo runtime-volatile values.
  This is the concrete safety net the prior decision worried was missing.
- **Dashboards, alerts, ServiceMonitors, PodMonitors: unchanged.**
  Nothing in the scrape path or the series identity changes, so existing Grafana boards and Prometheus rules keep working without edits.
- **Helm values: additive only.**
  Defaults reproduce current behavior; no existing key changes meaning.
- **Documented observable changes (traces only, operator-facing, not a stable API):** the `semconv` bump shifts some span attribute keys to current conventions, and dropping `autoprop` removes env-driven selection of non-W3C propagators (the W3C default is unchanged).
  Both affect trace consumers, never metrics or the scrape contract.
- **`client_golang` stays.**
  The honest framing of "replace Prometheus": the *instrumentation API* moves to OpenTelemetry; the *exposition format* remains Prometheus/OpenMetrics, and the library remains a transitive controller-runtime dependency regardless.

## Phasing

Each phase is independently shippable and lands CI-green on its own, matching the house norm.

- **Phase 0 — unified scaffold, nothing observable changes.**
  Add the `MeterProvider` with the Prometheus exporter bound to the existing registry; fold the bootstrap into one `InitProvider`; adopt `autoexport`.
  No metric is migrated yet, so `/metrics` output is identical.
- **Phase 1 — first subsystem, prove the pattern.**
  Migrate one subsystem (router or executor) to the meter helper; land the golden-file `/metrics` diff test; verify exemplars appear on its histograms.
- **Phase 2 — migrate the rest.**
  Executor, storagesvc, mqtrigger, buildermgr, endpointcache, the generic `http_requests_*`, and the error-sampler metrics, each gated by its golden-file test.
- **Phase 3 — footprint cuts.**
  Drop `autoprop` for the explicit W3C composite, bump `semconv`, move test-only deps out, and remove the now-dead bespoke exporter code.
- **Phase 4 — native OTLP metric push.**
  Attach the OTLP periodic reader behind `metricsExporter`; document it alongside (not replacing) the collection-layer scrape, and update `test/integration/otel/` to exercise direct push.

## Risks

- **Metric-name drift from the exporter's default suffixing.**
  The exporter, left default, would append unit and `_total` suffixes and add `target_info` / `otel_scope_*`.
  Mitigated by the explicit exporter options above and caught by the golden-file test.
- **Histogram bucket divergence.**
  OTel's default aggregation is not Prometheus buckets.
  Mitigated by mandatory explicit-bucket Views for all three histograms; the golden-file test asserts the `_bucket` boundaries.
- **Double-registration during migration.**
  A metric instrumented on both APIs at once would collide on the atomic registry composition and silently drop the Fission set.
  Mitigated by migrating each metric's `MustRegister` removal in the same change as its meter instrument, per subsystem.
- **Exemplar cardinality / OpenMetrics negotiation.**
  Exemplars ride the existing OpenMetrics exposition and the default trace-based filter, so volume is bounded by the sampled-span rate; low risk, but called out for scrapers that do not request OpenMetrics (they simply receive no exemplars).
- **Trace-attribute and propagator changes.**
  The `semconv` bump and `autoprop` removal change trace-side behavior; documented above and release-noted, never affecting the metrics contract.

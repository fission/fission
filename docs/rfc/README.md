# Fission RFCs

This directory holds the architecture RFCs behind shipped (or partially shipped) Fission features.
RFCs describe non-trivial, cross-cutting changes — new CRDs, data-plane refactors, backward-compat strategies, release-skewing decisions — and are published here once their implementation lands on `main`, so the design rationale travels with the code.
Proposals are normally published here once their implementation lands; a proposal slated for near-term implementation may be published early, marked Proposed.

Each document carries a `Status` header naming the implementing PRs and, where applicable, the parts that remain.

## Index

| RFC | Title | Status |
|---|---|---|
| [0001](0001-oci-native-package-delivery.md) | OCI-Native Package Delivery | Implemented ([#3484](https://github.com/fission/fission/pull/3484)) |
| [0002](0002-endpointslice-native-data-plane.md) | EndpointSlice-Native Data Plane | Phases 0–3 implemented ([#3485](https://github.com/fission/fission/pull/3485)); phase 4 next minor |
| [0003](0003-crd-modernization.md) | CRD Modernization (CEL, SSA, webhook trim) | Partially implemented ([#3448](https://github.com/fission/fission/pull/3448), [#3449](https://github.com/fission/fission/pull/3449), [#3452](https://github.com/fission/fission/pull/3452)) |
| [0004](0004-reconciler-consolidation.md) | Reconciler Consolidation & Dependency-Driven Watches | Implemented ([#3457](https://github.com/fission/fission/pull/3457)–[#3461](https://github.com/fission/fission/pull/3461)) |
| [0006](0006-runtime-error-noise-reduction.md) | Runtime Error-Noise Reduction & Pod-Lifecycle Correctness | Implemented ([#3468](https://github.com/fission/fission/pull/3468)–[#3473](https://github.com/fission/fission/pull/3473)) |
| [0007](0007-gateway-api-route-provider.md) | Gateway API Route Provider | Implemented ([#3478](https://github.com/fission/fission/pull/3478)) |
| [0008](0008-streaming-invocation-path.md) | Streaming Invocation Path (SSE / chunked / WebSocket) | Implemented ([#3482](https://github.com/fission/fission/pull/3482)) |
| [0011](0011-functions-as-mcp-tools.md) | Functions as MCP Tools & AI Gateway | Part A implemented ([#3483](https://github.com/fission/fission/pull/3483)); Part B deferred |
| [0012](0012-oci-default-package-delivery.md) | OCI-Native Package Delivery as the Default Cold-Start Path | Implemented (phases 1–4, [#3494](https://github.com/fission/fission/pull/3494)) |
| [0013](0013-incremental-router-routes.md) | Incremental Router Route Updates | Implemented (phases 0–2, [#3493](https://github.com/fission/fission/pull/3493); phase 3 gated, not built) |
| [0014](0014-router-hot-path-efficiency.md) | Router Hot-Path Efficiency | Implemented ([#3491](https://github.com/fission/fission/pull/3491)) |
| [0015](0015-invocation-correlation-and-failure-attribution.md) | Invocation Correlation & Failure Attribution | Implemented ([#3515](https://github.com/fission/fission/pull/3515)); phase 5 folded into RFC-0017 |
| [0016](0016-otlp-native-logging-pipeline.md) | Cloud-Native, OTLP-Native Logging Pipeline | Implemented in-repo (read path [#3516](https://github.com/fission/fission/pull/3516), access record [#3517](https://github.com/fission/fission/pull/3517), CI round-trip [#3518](https://github.com/fission/fission/pull/3518), streaming `--follow` [#3520](https://github.com/fission/fission/pull/3520), control-plane OTLP log push); only the env-image per-line helpers (separate `fission/environments` repo) remain |
| [0017](0017-function-developer-debugging-toolkit.md) | Function Developer Debugging Toolkit (CLI) | Partially implemented ([#3519](https://github.com/fission/fission/pull/3519), [#3520](https://github.com/fission/fission/pull/3520): `describe` + data-plane invocability, `test` enrichment, `logs` correlation + streaming `--follow`); only the cold-start metrics panel remains (needs a new metric + a CLI→Prometheus path) |
| [0018](0018-local-development-inner-loop.md) | Local-Development Inner Loop (`fission function run-local`) | Implemented (phases 0–5 except `--remote`): local Docker loop — runtime image → `/v2/specialize` → invoke → teardown, cluster-less with `--image`, all executor types via `--executor`, `--watch` hot reload, `--build` builder leg, `--secret`/`--configmap` + `-e`/`--env-from` bridges, `--debug-port`; `--remote` (approach C) deferred to its own RFC |
| [0019](0019-unified-opentelemetry-observability.md) | Unified OpenTelemetry Observability | Implemented (phases 0–2, 4; phase 3 footprint cuts except `autoexport`): migrated the 39 metrics from Prometheus `client_golang` to the OTel Metrics API behind the OTel→Prometheus bridge exporter (scrape-compatible `/metrics`), added opt-in native OTLP metric push + trace exemplars, dropped `autoprop` (−4 propagator modules), bumped `semconv` |
| [0020](0020-e2e-benchmarking-suite.md) | End-to-End Benchmarking Suite & Continuous Performance Tracking | Implemented ([#3542](https://github.com/fission/fission/pull/3542), merged 2026-06-26): Go-native e2e benchmark engine + portable `fission-benchmark` CLI + `benchmark.yaml` CI entry in the separate `test/benchmark` module (pure-Go loadgen, HDR percentiles), replacing the legacy bash/k6/picasso assets; scenarios extended in [#3550](https://github.com/fission/fission/pull/3550), [#3559](https://github.com/fission/fission/pull/3559) |
| [0021](0021-statestore-substrate.md) | Statestore — Standard Durable-State Interface | Implemented ([#3574](https://github.com/fission/fission/pull/3574), merged 2026-07-14): `pkg/statestore` KVStore/EventLog/Queue interfaces with memory/SQLite/Postgres/HTTP-client drivers, external (user-managed Postgres) + embedded (Fission-owned SQLite) modes — Fission never ships a database; shared substrate for 0024/0027 (and 0022/0023 next). Optional Redis KV driver deferred (YAGNI) |
| [0022](0022-durable-function-workflows.md) | Durable Function Workflows | Proposed (revised 2026-07-16 pre-implementation): `Workflow`/`WorkflowRun` CRDs, EventLog-fold engine (CAS-append, no leader election, spec-snapshot-in-stream, checkpointed folds, worker-pool invocation), Step Functions-style orchestration with a pinned error model and expression grammar; TLA+-checked protocol |
| [0023](0023-stateful-functions-keyed-state-sticky-routing.md) | Stateful Functions — Keyed State & Sticky Routing | Proposed: `FunctionSpec.State` keyed KV API via scoped tokens + HRW sticky routing over the RFC-0002 endpoint index; Durable-Objects-style |
| [0024](0024-async-invocation-retries-dlq-destinations.md) | Async Invocation — Retries, DLQ, Destinations | Implemented ([#3578](https://github.com/fission/fission/pull/3578), [#3579](https://github.com/fission/fission/pull/3579), [#3580](https://github.com/fission/fission/pull/3580), merged 2026-07-14–15): `X-Fission-Invoke-Mode: async` → durable enqueue, at-least-once dispatch, dead-letter queue + redrive (CLI), on-success/failure destinations (function or topic), KEDA queue-depth scaler; on the 0021 `Queue` |
| [0025](0025-function-versions-aliases-rollback.md) | Function Versions, Aliases & Instant Rollback | Proposed: immutable `FunctionVersion` snapshots + movable aliases with weighted splits over the RFC-0013 pointer-swap path; one-command rollback; absorbs CanaryConfig |
| [0026](0026-provisioned-concurrency-scheduled-warming.md) | Provisioned Concurrency & Scheduled Warming | Proposed: `FunctionSpec.ProvisionedConcurrency` — poolmgr keeps N pre-specialized pods, cron windows; directly kills cold starts for opted-in functions |
| [0027](0027-statestore-backed-eventing.md) | Statestore-Backed Eventing — Built-in MQ Provider | Implemented ([#3582](https://github.com/fission/fission/pull/3582) docs, [#3583](https://github.com/fission/fission/pull/3583), [#3584](https://github.com/fission/fission/pull/3584), [#3585](https://github.com/fission/fission/pull/3585), merged 2026-07-15–16): `TopicPublisher` + durable topics on the 0021 `EventLog` + `messageQueueType: statestore` MQ provider — pub/sub function pipelines with zero brokers; un-defers 0024 topic destinations; broker egress (kafka) as the scale path + `fission topic` CLI + KEDA lag scaler. Orphan-stream age sweep deferred |

## Companion material

- [0002-implementation-plan.md](0002-implementation-plan.md) — RFC-0002's PR-by-PR phasing, test inventories, and risk register.
- [0002-perf-runbook-results.md](0002-perf-runbook-results.md) — RFC-0002 pre-phase-4 performance verification (all acceptance bars passed); raw data in [0002-perf-data/](0002-perf-data/).
- [0004-reconciler-map.html](0004-reconciler-map.html) — interactive map of the consolidated reconciler topology.
- The e2e benchmarking suite lives in [`test/benchmark/`](../../test/benchmark/) (RFC-0020): the `fission-benchmark` CLI runs cold-start, warm-path, autoscaling, build, and control-plane-scale scenarios against any cluster.
  The raw RFC-0002 data in [0002-perf-data/](0002-perf-data/) is kept as historical evidence.

## Numbering

Numbers are allocated in submission order; gaps in this index are proposals that have not shipped.

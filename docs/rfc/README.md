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
| [0016](0016-otlp-native-logging-pipeline.md) | Cloud-Native, OTLP-Native Logging Pipeline | Partially implemented (read path [#3516](https://github.com/fission/fission/pull/3516), access record [#3517](https://github.com/fission/fission/pull/3517), CI round-trip [#3518](https://github.com/fission/fission/pull/3518); OTLP push / streaming / deprecation cutover remain) |
| [0017](0017-function-developer-debugging-toolkit.md) | Function Developer Debugging Toolkit (CLI) | Partially implemented (`function describe` + invocability, `test` enrichment, `logs` correlation flags); server-dependent diag endpoint / streaming / cold-start metrics remain |

## Companion material

- [0002-implementation-plan.md](0002-implementation-plan.md) — RFC-0002's PR-by-PR phasing, test inventories, and risk register.
- [0002-perf-runbook-results.md](0002-perf-runbook-results.md) — RFC-0002 pre-phase-4 performance verification (all acceptance bars passed); raw data in [0002-perf-data/](0002-perf-data/).
- [0004-reconciler-map.html](0004-reconciler-map.html) — interactive map of the consolidated reconciler topology.
- Reusable benchmarks live in [`test/benchmark/tests/cold-start`](../../test/benchmark/tests/cold-start) and [`test/benchmark/tests/warm-path`](../../test/benchmark/tests/warm-path), driven by [`test/benchmark/rfc0002-perf-runbook.sh`](../../test/benchmark/rfc0002-perf-runbook.sh).

## Numbering

Numbers are allocated in submission order; gaps in this index are proposals that have not shipped.

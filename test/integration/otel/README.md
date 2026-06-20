<!--
SPDX-FileCopyrightText: The Fission Authors

SPDX-License-Identifier: Apache-2.0
-->

# CI-only OTLP logging stack (RFC-0016)

These manifests stand up an **OpenTelemetry Collector + Loki** for the
RFC-0016 logging read-path integration test
(`TestFunctionLogsLokiCorrelation`).

They are **not** part of the Fission Helm chart.
Fission bundles no log collector or backend — collection is delegated to an
operator-run external pipeline (see
[`docs/rfc/0016-otlp-native-logging-pipeline.md`](../../../docs/rfc/0016-otlp-native-logging-pipeline.md),
"Collection — operators run an external collector").
This stack plays the operator's side so CI can exercise the `loki` logdb driver
end to end; it is applied only on one CI leg (see `.github/workflows/push_pr.yaml`).

## What it proves

The router emits one structured access record per invocation
(`msg="function access"`, opt-in via `router.displayAccessLog`, on in kind-ci),
carrying the request id, trace id, and function identity.
The test asserts that record is queryable from Loki by request id, which
validates the whole correlation pipeline:

```
router stdout (access record, zap JSON)
  → otel-collector (filelog tail → keep access records → hoist
                    fission.function.{uid,namespace,name} to resource attrs)
  → Loki OTLP endpoint (/otlp/v1/logs), which indexes those resource attrs
    as the stream labels fission_function_uid / fission_function_namespace
  → fission function logs --dbtype loki --request-id <id>
    → LogQL: {fission_function_uid="<uid>"} | json | fission_request_id="<id>"
```

## Files

- `loki.yaml` — single-binary Loki (filesystem storage) in namespace
  `fission-logging`, with `otlp_config` promoting `fission.function.*` resource
  attributes to index labels.
- `otel-collector.yaml` — a Collector DaemonSet that tails the router pod's
  container log, keeps only the access record, hoists the function identity to
  resource attributes (`groupbyattrs`), and pushes to Loki's OTLP endpoint.
  No Kubernetes RBAC is needed: the function identity comes from the router log
  body, not pod labels, so the Collector reads only the node's pod-log files.
- `metrics-collector.reference.yaml` — **reference, not run by CI** (RFC-0016
  §1b): a Collector `prometheus` receiver that scrapes Fission's existing
  `/metrics:8080` endpoints (the same targets the chart's ServiceMonitor
  selects) and exports them as OTLP. This brings **metrics** into the same
  OpenTelemetry pipeline as traces and logs at the collection layer, with **no
  instrumentation change** and full backward compatibility (the Prometheus
  scrape path keeps working). Operators adapt the backend endpoint + grant the
  Collector ServiceAccount the Kubernetes service-discovery RBAC.

## Running the test locally

```sh
kubectl apply -f test/integration/otel/loki.yaml
kubectl apply -f test/integration/otel/otel-collector.yaml
kubectl rollout status deploy/loki -n fission-logging --timeout=3m
kubectl rollout status daemonset/otel-collector -n fission-logging --timeout=3m

kubectl port-forward svc/loki 3100:3100 -n fission-logging &
export LOKI_URL=http://127.0.0.1:3100
export FISSION_TEST_LOKI=1
# plus the usual router port-forwards / image env vars (see the repo CLAUDE.md)

go test -tags=integration -run TestFunctionLogsLokiCorrelation -v \
  ./test/integration/suites/common/...
```

The cluster must run the router with `router.displayAccessLog=true` (kind-ci
sets this) so the access record is emitted.

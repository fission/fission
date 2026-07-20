{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 24 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 24 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
*/}}
{{- define "fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- printf "%s-%s" .Release.Name $name | trunc 24 | trimSuffix "-" -}}
{{- end -}}


{{/*
This is a template with config parameters for optional features in fission. This gets mounted on to the controller pod
as a config map.
To add new features with config parameters, create a yaml block below with the feature name and define a corresponding struct in
controller/config.go
*/}}
{{- define "config" -}}
canary:
  enabled: {{ .Values.canaryDeployment.enabled }}
  prometheusSvc: {{ .Values.prometheus.serviceEndpoint | default "" | quote }}
  {{- printf "\n" -}}
auth:
  enabled: {{ .Values.authentication.enabled | default false }}
  {{- if .Values.authentication.enabled }}
  authUriPath: {{ .Values.authentication.authUriPath | default "/auth/login" | quote}}
  jwtExpiryTime: {{ .Values.authentication.jwtExpiryTime | default 120 }}
  jwtIssuer: {{ .Values.authentication.jwtIssuer | default "fission" | quote }}
  {{- end }}
{{- end -}}

{{/*
Helper template to construct image names with repository and tag
*/}}
{{- define "imageWithTag" -}}
{{- $repository := index . 0 -}}
{{- $image := index . 1 -}}
{{- $tag := index . 2 -}}
{{- if $repository -}}
{{- printf "%s/%s%s" $repository $image (ne $tag "" | ternary (printf ":%s" $tag) "") -}}
{{- else -}}
{{- printf "%s%s" $image (ne $tag "" | ternary (printf ":%s" $tag) "") -}}
{{- end -}}
{{- end -}}

{{- define "fission-bundleImage" -}}
{{- $args := list .Values.repository .Values.image .Values.imageTag -}}
{{- include "imageWithTag" $args -}}
{{- end -}}

{{- define "reporterImage" -}}
{{- $args := list .Values.repository .Values.postInstallReportImage .Values.imageTag -}}
{{- include "imageWithTag" $args -}}
{{- end -}}

{{- define "fetcherImage" -}}
{{- $args := list (.Values.fetcher.repository | default .Values.repository) .Values.fetcher.image .Values.fetcher.imageTag -}}
{{- include "imageWithTag" $args -}}
{{- end -}}

{{- define "preUpgradeChecksImage" -}}
{{- $args := list (.Values.preUpgradeChecks.repository | default .Values.repository) .Values.preUpgradeChecks.image .Values.preUpgradeChecks.imageTag -}}
{{- include "imageWithTag" $args -}}
{{- end -}}

{{- define "opentelemtry.envs" }}
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: "{{ .Values.openTelemetry.otlpCollectorEndpoint }}"
- name: OTEL_EXPORTER_OTLP_INSECURE
  value: "{{ .Values.openTelemetry.otlpInsecure }}"
- name: OTEL_LOGS_ENABLED
  value: "{{ .Values.openTelemetry.logsEnabled }}"
{{- if .Values.openTelemetry.otlpHeaders }}
- name: OTEL_EXPORTER_OTLP_HEADERS
  value: "{{ .Values.openTelemetry.otlpHeaders }}"
{{- end }}
- name: OTEL_TRACES_SAMPLER
  value: "{{ .Values.openTelemetry.tracesSampler }}"
- name: OTEL_TRACES_SAMPLER_ARG
  value: "{{ .Values.openTelemetry.tracesSamplingRate }}"
- name: OTEL_PROPAGATORS
  value: "{{ .Values.openTelemetry.propagators }}"
- name: OTEL_METRICS_EXPORTER
  value: "{{ .Values.openTelemetry.metricsExporter }}"
{{- end }}

{{- define "fission-resource-namespace.envs" }}
- name: FISSION_BUILDER_NAMESPACE
  value: "{{ .Values.builderNamespace }}"
- name: FISSION_FUNCTION_NAMESPACE
  value: "{{ .Values.functionNamespace }}"
- name: FISSION_DEFAULT_NAMESPACE
  value: "{{ .Values.defaultNamespace }}"
- name: FISSION_RESOURCE_NAMESPACES
{{- if gt (len .Values.additionalFissionNamespaces) 0 }}
  value: "{{ .Values.defaultNamespace }},{{ join "," .Values.additionalFissionNamespaces }}"
{{- else }}
  value: {{ .Values.defaultNamespace }}
{{- end }}
- name: FISSION_TENANCY_MODE
  value: "{{ include "fission.tenancyMode" . }}"
{{- end }}

{{/*
fission.tenancyMode — the configured multi-namespace tenancy posture, normalised
to one of static|dynamic|cluster. Single source of truth for every gate.
*/}}
{{- define "fission.tenancyMode" -}}
{{- dig "mode" "static" (.Values.tenancy | default dict) -}}
{{- end -}}

{{/*
fission.tenancyControllerEnabled — true (non-empty) when the tenant controller and
the dynamic-cluster machinery should be rendered, i.e. tenancy.mode is dynamic OR
cluster. Empty string (falsey) for static. Use: {{- if include "fission.tenancyControllerEnabled" . }}
*/}}
{{- define "fission.tenancyControllerEnabled" -}}
{{- if ne (include "fission.tenancyMode" .) "static" -}}true{{- end -}}
{{- end -}}

{{- define "kube_client.envs" }}
- name: KUBE_CLIENT_QPS
  value: "{{ .Values.kubernetesClientQPS }}"
- name: KUBE_CLIENT_BURST
  value: "{{ .Values.kubernetesClientBurst }}"
{{- end}}

{{/*
leaderElection.envs renders the env entries that enable client-go leader
election for a control-plane controller subsystem. Pass a dict with key
"enabled" (bool). POD_NAME identifies the lease holder; the lease namespace
falls back to the in-cluster service-account namespace when POD_NAMESPACE is
unset, so it is intentionally not emitted here (some deployments already set
it). Disabled by default → behaviour is unchanged for single-replica installs.
*/}}
{{- define "leaderElection.envs" }}
- name: LEADER_ELECTION_ENABLED
  value: {{ .enabled | default false | quote }}
- name: POD_NAME
  valueFrom:
    fieldRef:
      fieldPath: metadata.name
{{- end }}

{{/*
internalAuth.envs renders the two env entries that wire the HMAC shared
secret into a Fission control-plane container. See the design at docs/internal-auth/00-design.md. The OLD
secret is mounted with optional: true so rotation can drop it without
forcing the chart to render an empty key.
*/}}
{{- define "internalAuth.envs" }}
{{- if .Values.internalAuth.enabled }}
- name: FISSION_INTERNAL_AUTH_SECRET
  valueFrom:
    secretKeyRef:
      name: fission-internal-auth
      key: secret
- name: FISSION_INTERNAL_AUTH_SECRET_OLD
  valueFrom:
    secretKeyRef:
      name: fission-internal-auth
      key: oldSecret
      optional: true
{{- end }}
{{- end }}

{{/*
Define the svc's name
*/}}
{{- define "fission-webhook.svc" -}}
{{- printf "webhook-service" -}}
{{- end -}}

{{- define "fission-function-ns" -}}
{{- if .Values.functionNamespace -}}
{{- printf "%s" .Values.functionNamespace -}}
{{- else -}}
{{- printf "%s" .Values.defaultNamespace -}}
{{- end -}}
{{- end -}}

{{- define "fission-builder-ns" -}}
{{- if .Values.builderNamespace -}}
{{- printf "%s" .Values.builderNamespace -}}
{{- else -}}
{{- printf "%s" .Values.defaultNamespace -}}
{{- end -}}
{{- end -}}

{{/*
coverage.* helpers: emit GOCOVERDIR env, a hostPath volumeMount, and the
hostPath volume for integration-test binary coverage. DEV/CI ONLY — gated
by .Values.coverage.enabled (default false), so they render nothing in
production. See values.yaml `coverage`.
*/}}
{{- define "coverage.envs" }}
{{- if .Values.coverage.enabled }}
- name: GOCOVERDIR
  value: {{ .Values.coverage.mountPath | default "/coverage" | quote }}
{{- end }}
{{- end }}

{{- define "coverage.volumemount" }}
{{- if .Values.coverage.enabled }}
- name: coverage-data
  mountPath: {{ .Values.coverage.mountPath | default "/coverage" | quote }}
{{- end }}
{{- end }}

{{- define "coverage.volume" }}
{{- if .Values.coverage.enabled }}
- name: coverage-data
  hostPath:
    path: {{ .Values.coverage.hostPath | default "/fission-coverage" | quote }}
    # Directory (not DirectoryOrCreate): the dir MUST be pre-created on the
    # node owned by the pod uid (see values.yaml + the CI workflow). This
    # enforces the uid-owned/0700 contract and fails loudly if misconfigured
    # rather than letting kubelet create a root-owned dir.
    type: Directory
{{- end }}
{{- end }}

{{/*
fission.routerInternalPort is the router's internal listener port — the
/fission-function/... listener behind the GHSA-3g33-6vg6-27m8 split. Mirrored
by pkg/svcinfo.PortRouterInternal; a Go-side drift test compares the rendered
chart against those constants.
*/}}
{{- define "fission.routerInternalPort" -}}
{{ .Values.router.internalPort | default 8889 }}
{{- end -}}

{{/*
fission.routerInternalURL is the in-cluster URL internal publishers
(kubewatcher / timer / mqtrigger / mqt-keda / mcp) use to reach the router's
internal listener.
*/}}
{{- define "fission.routerInternalURL" -}}
http://router-internal.{{ .Release.Namespace }}:{{ include "fission.routerInternalPort" . }}
{{- end -}}

{{/*
fission.podNamespaceEnv injects POD_NAMESPACE via the downward API — the
namespace fission-bundle's AddressResolver derives sibling-service URL
defaults from when a URL flag/env is not explicitly set.
*/}}
{{- define "fission.podNamespaceEnv" -}}
- name: POD_NAMESPACE
  valueFrom:
    fieldRef:
      fieldPath: metadata.namespace
{{- end }}

{{/*
fission.routerPort is the router's public listener port (fronted by the
Service's port 80). Mirrored by pkg/svcinfo.PortRouter.
*/}}
{{- define "fission.routerPort" -}}
{{ .Values.router.port | default 8888 }}
{{- end -}}

{{/*
fission.mcpPort is the MCP tool server's port. Mirrored by
pkg/svcinfo.PortMCP.
*/}}
{{- define "fission.mcpPort" -}}
{{ .Values.mcp.port | default 8890 }}
{{- end -}}

{{/*
fission.statestorePort is the embedded statestore's capability API port.
Mirrored by pkg/svcinfo.PortStatestore (RFC-0021).
*/}}
{{- define "fission.statestorePort" -}}
{{ (.Values.statestore | default dict).port | default 8891 }}
{{- end -}}

{{/*
fission.statesvcPort is the statesvc function-facing keyed-state API port.
Mirrored by pkg/svcinfo.PortStateSvc (RFC-0023).
*/}}
{{- define "fission.statesvcPort" -}}
{{ (.Values.functionState | default dict).port | default 8893 }}
{{- end -}}

{{/*
fission.workflowPort is the workflow engine head's port. Mirrored by
pkg/svcinfo.PortWorkflow (RFC-0022).
*/}}
{{- define "fission.workflowPort" -}}
{{ (.Values.workflows | default dict).port | default 8892 }}
{{- end -}}

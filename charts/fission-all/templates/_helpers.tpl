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
{{- end }}

{{- define "kube_client.envs" }}
- name: KUBE_CLIENT_QPS
  value: "{{ .Values.kubernetesClientQPS }}"
- name: KUBE_CLIENT_BURST
  value: "{{ .Values.kubernetesClientBurst }}"
{{- end}}

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

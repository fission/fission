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
  {{- if .Values.prometheus.enabled }}
  prometheusSvc: "http://{{ .Release.Name }}-prometheus-server.{{ .Release.Namespace }}"
  {{- else }}
  prometheusSvc: {{ .Values.prometheus.serviceEndpoint | default "" | quote }}
  {{- end }}
  {{- printf "\n" -}}
{{- end -}}

{{/*
This template generates the image name for the deployment depending on the value of "repository" field in values.yaml file.
*/}}
{{- define "fission-bundleImage" -}}
{{- if .Values.repository -}}
    {{ .Values.repository }}/{{ .Values.image }}:{{ .Values.imageTag }}
{{- else -}}
    {{ .Values.image }}:{{ .Values.imageTag }}    
{{- end }}
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
{{- end }}

{{- define "opentracing.envs" }}
- name: OPENTRACING_ENABLED
  value: {{ .Values.openTracing.enabled | default false | quote }}
- name: TRACE_JAEGER_COLLECTOR_ENDPOINT
  value: "{{ .Values.openTracing.collectorEndpoint }}"
- name: TRACING_SAMPLING_RATE
  value: {{ .Values.openTracing.samplingRate | default "0.5" | quote }}
{{- end }}

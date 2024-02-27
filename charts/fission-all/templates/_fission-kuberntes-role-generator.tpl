{{- define "kubernetes-role-generator" }}
---
apiVersion: rbac.authorization.k8s.io/v1
{{ if .Values.fissionOnAllNamespaces -}}
  kind: ClusterRole
{{- else -}}
  kind: Role
{{- end }}
metadata:
{{- if eq "preupgrade" .component }}
  annotations:
    helm.sh/hook: pre-upgrade
    helm.sh/hook-delete-policy: before-hook-creation
    helm.sh/hook-weight: "-2"
{{- end }}
  name: "{{ .Release.Name }}-{{ .component }}"
  {{ if not .Values.fissionOnAllNamespaces -}}
    namespace: {{ .namespace }}
  {{- end }}
{{- if eq "buildermgr" .component }}
{{- include "buildermgr-kuberules" . }}
{{- end }}
{{- if eq "canaryconfig" .component }}
{{- include "canaryconfig-kuberules" . }}
{{- end }}
{{- if eq "fluentbit" .component }}
{{- include "fluentbit-kuberules" . }}
{{- end }}
{{- if eq "executor" .component }}
{{- include "executor-kuberules" . }}
{{- end }}
{{- if eq "kubewatcher" .component }}
{{- include "kubewatcher-kuberules" . }}
{{- end }}
{{- if eq "keda" .component }}
{{- include "keda-kuberules" . }}
{{- end }}
{{- if eq "preupgrade" .component }}
{{- include "preupgrade-kuberules" . }}
{{- end }}
{{- if eq "router" .component }}
{{- include "router-kuberules" . }}
{{- end }}
{{- if eq "timer" .component }}
{{- include "timer-kuberules" . }}
{{- end }}

---
{{ if .Values.fissionOnAllNamespaces -}}
  kind: ClusterRoleBinding
{{- else -}}
  kind: RoleBinding
{{- end }}
apiVersion: rbac.authorization.k8s.io/v1
metadata:
{{- if eq "preupgrade" .component }}
  annotations:
    helm.sh/hook: pre-upgrade
    helm.sh/hook-delete-policy: before-hook-creation
{{- end }}
  name: "{{ .Release.Name }}-{{ .component }}"
  {{ if not .Values.fissionOnAllNamespaces -}}
    namespace: {{ .namespace }}
  {{- end }}
subjects:
  - kind: ServiceAccount
    name: "fission-{{ .component }}"
    namespace: {{ .Release.Namespace }}
roleRef:
  {{ if .Values.fissionOnAllNamespaces -}}
    kind: ClusterRole
  {{- else -}}
    kind: Role
  {{- end }}
  name: "{{ .Release.Name }}-{{ .component }}"
  apiGroup: rbac.authorization.k8s.io
{{- end }}
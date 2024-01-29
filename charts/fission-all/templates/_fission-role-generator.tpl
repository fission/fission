{{- define "fission-role-generator" }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: {{ if .Values.fissionOnAllNamespaces }}Cluster{{- end }}Role
metadata:
{{- if eq "preupgrade" .component }}
  annotations:
    helm.sh/hook: pre-upgrade
    helm.sh/hook-delete-policy: before-hook-creation
    helm.sh/hook-weight: "-2"
{{- end }}
  name: "{{ .Release.Name }}-{{ .component }}-fission-cr"
  {{- if not .Values.fissionOnAllNamespaces }}
  namespace: {{ .namespace }}
  {{- end }}
{{- if eq "buildermgr" .component }}
{{- include "buildermgr-rules" . }}
{{- end }}
{{- if eq "executor" .component }}
{{- include "executor-rules" . }}
{{- end }}
{{- if eq "kubewatcher" .component }}
{{- include "kubewatcher-rules" . }}
{{- end }}
{{- if eq "keda" .component }}
{{- include "keda-rules" . }}
{{- end }}
{{- if eq "preupgrade" .component }}
{{- include "preupgrade-rules" . }}
{{- end }}
{{- if eq "router" .component }}
{{- include "router-rules" . }}
{{- end }}
{{- if eq "storagesvc" .component }}
{{- include "storagesvc-rules" . }}
{{- end }}
{{- if eq "timer" .component }}
{{- include "timer-rules" . }}
{{- end }}
{{- if eq "canaryconfig" .component }}
{{- include "canaryconfig-rules" . }}
{{- end }}

---
kind: {{ if .Values.fissionOnAllNamespaces }}Cluster{{- end }}RoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
{{- if eq "preupgrade" .component }}
  annotations:
    helm.sh/hook: pre-upgrade
    helm.sh/hook-delete-policy: before-hook-creation
{{- end }}
  name: "{{ .Release.Name }}-{{ .component }}-fission-cr"
  {{- if not .Values.fissionOnAllNamespaces }}
  namespace: {{ .namespace }}
  {{- end }}
subjects:
  - kind: ServiceAccount
    name: "fission-{{ .component }}"
    namespace: {{ .Release.Namespace }}
roleRef:
  kind: {{ if .Values.fissionOnAllNamespaces }}Cluster{{- end }}Role
  name: "{{ .Release.Name }}-{{ .component }}-fission-cr"
  apiGroup: rbac.authorization.k8s.io
{{- end }}
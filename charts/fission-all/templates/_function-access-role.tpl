{{- define "fissionFunction.roles" }}
---
apiVersion: rbac.authorization.k8s.io/v1
{{ if .Values.fissionOnAllNamespaces -}}
  kind: ClusterRole
{{- else -}}
  kind: Role
{{- end }}
metadata:
  name: {{ .Release.Name }}-fission-fetcher
  {{ if not .Values.fissionOnAllNamespaces -}}
    namespace: {{ .namespace }}
  {{- end }}
rules:
- apiGroups:
  - ""
  resources:
  - configmaps
  - secrets
  verbs:
  - get
- apiGroups:
  - fission.io
  resources:
  - packages
  verbs:
  - get
---
apiVersion: rbac.authorization.k8s.io/v1
{{ if .Values.fissionOnAllNamespaces -}}
  kind: ClusterRole
{{- else -}}
  kind: Role
{{- end }}
metadata:
  name: {{ .Release.Name }}-fission-builder
  {{ if not .Values.fissionOnAllNamespaces -}}
    namespace: {{ .namespace }}
  {{- end }}
rules:
- apiGroups:
  - fission.io
  resources:
  - packages
  verbs:
  - get
- apiGroups:
  - ""
  resources:
  - configmaps
  - secrets
  verbs:
  - get
---
apiVersion: rbac.authorization.k8s.io/v1
{{ if .Values.fissionOnAllNamespaces -}}
  kind: ClusterRole
{{- else -}}
  kind: Role
{{- end }}
metadata:
  name: {{ .Release.Name }}-fission-fetcher-websocket
  {{ if not .Values.fissionOnAllNamespaces -}}
    namespace: {{ .namespace }}
  {{- end }}
rules:
- apiGroups:
  - ""
  resources:
  - "events"
  verbs:
  - "get"
  - "list"
  - "watch"
  - "create"
  - "update"
  - "patch"
- apiGroups:
  - ""
  resources:
  - pods
  verbs:
  - get  
{{- end -}}

{{- define "fissionFunction.rolebindings" }}
---
apiVersion: rbac.authorization.k8s.io/v1
{{ if .Values.fissionOnAllNamespaces -}}
  kind: ClusterRoleBinding
{{- else -}}
  kind: RoleBinding
{{- end }}
metadata:
  name: {{ .Release.Name }}-fission-fetcher
  {{ if not .Values.fissionOnAllNamespaces -}}
    namespace: {{ .namespace }}
  {{- end }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  {{ if .Values.fissionOnAllNamespaces -}}
    kind: ClusterRole
  {{- else -}}
    kind: Role
  {{- end }}
  name: {{ .Release.Name }}-fission-fetcher
subjects:
  - kind: ServiceAccount
    name: fission-fetcher
    {{- if and (.Values.functionNamespace) (eq .namespace "default") }}
    namespace: {{ .Values.functionNamespace }}
    {{- else }}
    namespace: {{ .namespace }}
    {{- end }}
---
apiVersion: rbac.authorization.k8s.io/v1
{{ if .Values.fissionOnAllNamespaces -}}
  kind: ClusterRoleBinding
{{- else -}}
  kind: RoleBinding
{{- end }}
metadata:
  name: {{ .Release.Name }}-fission-builder
  {{ if not .Values.fissionOnAllNamespaces -}}
    namespace: {{ .namespace }}
  {{- end }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  {{ if .Values.fissionOnAllNamespaces -}}
    kind: ClusterRole
  {{- else -}}
    kind: Role
  {{- end }}
  name: {{ .Release.Name }}-fission-builder
subjects:
  - kind: ServiceAccount
    name: fission-builder
    {{- if and (.Values.builderNamespace) (eq .namespace "default") }}
    namespace: {{ .Values.builderNamespace }}
    {{- else }}
    namespace: {{ .namespace }}
    {{- end }}
---
apiVersion: rbac.authorization.k8s.io/v1
{{ if .Values.fissionOnAllNamespaces -}}
  kind: ClusterRoleBinding
{{- else -}}
  kind: RoleBinding
{{- end }}
metadata:
  name: {{ .Release.Name }}-fission-fetcher-websocket
  {{ if not .Values.fissionOnAllNamespaces -}}
    namespace: {{ .namespace }}
  {{- end }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  {{ if .Values.fissionOnAllNamespaces -}}
    kind: ClusterRole
  {{- else -}}
    kind: Role
  {{- end }}
  name: {{ .Release.Name }}-fission-fetcher-websocket
subjects:
  - kind: ServiceAccount
    name: fission-fetcher
    {{- if and (.Values.functionNamespace) (eq .namespace "default") }}
    namespace: {{ .Values.functionNamespace }}
    {{- else }}
    namespace: {{ .namespace }}
    {{- end }}    
{{- end -}}

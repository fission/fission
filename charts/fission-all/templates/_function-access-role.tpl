{{- define "fissionFunction.roles" }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ .Release.Name }}-fission-fetcher
  namespace: {{ .namespace }}
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
kind: Role
metadata:
  name: {{ .Release.Name }}-fission-builder
  namespace: {{ .namespace }}
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
kind: Role
metadata:
  namespace: {{ .namespace }}
  name: {{ .Release.Name }}-fission-fetcher-websocket
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
kind: RoleBinding
metadata:
  name: {{ .Release.Name }}-fission-fetcher
  namespace: {{ .namespace }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
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
kind: RoleBinding
metadata:
  name: {{ .Release.Name }}-fission-builder
  namespace: {{ .namespace }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
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
kind: RoleBinding
metadata:
  name: {{ .Release.Name }}-fission-fetcher-websocket
  namespace: {{ .namespace }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
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

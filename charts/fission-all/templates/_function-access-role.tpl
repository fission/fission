{{- /*
fetcher/builder/websocket access rules — the SINGLE SOURCE for what a function
pod's sidecars may read. Used in two places:
  - the static per-namespace Roles below (fissionFunction.roles), and
  - the fixed-name ClusterRoles for dynamic multi-namespace mode
    (templates/tenant-controller/dynamic-workload-roles.yaml), which the tenant
    controller binds into each runtime-onboarded namespace by name.
Because both render from these partials, the static and dynamic paths can no
longer drift — pkg/tenant/provision.go binds the ClusterRoles by name and no
longer carries its own copy of the rules.
*/ -}}
{{- define "fissionFunction.fetcherRules" }}
rules:
- apiGroups:
  - ""
  resources:
  - configmaps
  - secrets
  verbs:
  - get
# The OCI package keychain (RFC-0001) reads the fetcher SA's
# imagePullSecrets to resolve registry credentials.
- apiGroups:
  - ""
  resources:
  - serviceaccounts
  verbs:
  - get
- apiGroups:
  - fission.io
  resources:
  - packages
  verbs:
  - get
{{- end -}}

{{- define "fissionFunction.builderRules" }}
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
{{- end -}}

{{- define "fissionFunction.fetcherWebsocketRules" }}
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

{{- define "fissionFunction.roles" }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ .Release.Name }}-fission-fetcher
  namespace: {{ .namespace }}
{{- include "fissionFunction.fetcherRules" . }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ .Release.Name }}-fission-builder
  namespace: {{ .namespace }}
{{- include "fissionFunction.builderRules" . }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  namespace: {{ .namespace }}
  name: {{ .Release.Name }}-fission-fetcher-websocket
{{- include "fissionFunction.fetcherWebsocketRules" . }}
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

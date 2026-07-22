{{- define "fission-role-generator" }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
{{- if eq "preupgrade" .component }}
  annotations:
    helm.sh/hook: pre-upgrade
    helm.sh/hook-delete-policy: before-hook-creation
    helm.sh/hook-weight: "-2"
{{- end }}
  name: "{{ .Release.Name }}-{{ .component }}-fission-cr"
  namespace: {{ .namespace }}
{{- if eq "buildermgr" .component }}
{{- include "buildermgr-rules" . }}
{{- end }}
{{- if eq "executor" .component }}
{{- include "executor-rules" . }}
{{- end }}
{{- if eq "kubewatcher" .component }}
{{- include "kubewatcher-rules" . }}
{{- end }}
{{- if eq "mcp" .component }}
{{- include "mcp-rules" . }}
{{- end }}
{{- if eq "workflow" .component }}
{{- include "workflow-rules" . }}
{{- end }}
{{- if eq "statesvc" .component }}
{{- include "statesvc-rules" . }}
{{- end }}
{{- if eq "kafka" .component }}
{{- include "kafka-rules" . }}
{{- end }}
{{- if eq "statestore-mqt" .component }}
{{- include "statestore-mqt-rules" . }}
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
kind: RoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
{{- if eq "preupgrade" .component }}
  annotations:
    helm.sh/hook: pre-upgrade
    helm.sh/hook-delete-policy: before-hook-creation
{{- end }}
  name: "{{ .Release.Name }}-{{ .component }}-fission-cr"
  namespace: {{ .namespace }}
subjects:
  - kind: ServiceAccount
    name: "fission-{{ .component }}"
    namespace: {{ .Release.Namespace }}
roleRef:
  kind: Role
  name: "{{ .Release.Name }}-{{ .component }}-fission-cr"
  apiGroup: rbac.authorization.k8s.io
{{- end }}

{{/*
fission-cluster-role-generator emits the cluster-wide counterpart of
fission-role-generator for dynamic multi-namespace mode: a ClusterRole carrying
the SAME per-component {component}-rules (which are fission.io-only — verified:
every apiGroup in _fission-component-roles.tpl is fission.io) and a
ClusterRoleBinding to the component's ServiceAccount. This is the one
cluster-wide grant the dynamic-watch model adds, scoped to Fission's own CRD
types and no core/workload type. Rendered only for the components whose Managers
watch Fission CRDs cluster-wide (the executor stays per-namespace — Tier-B).
*/}}
{{- define "fission-cluster-role-generator" }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: "{{ .Release.Name }}-{{ .component }}-fission-cr-cluster"
{{- if eq "buildermgr" .component }}
{{- include "buildermgr-rules" . }}
{{- end }}
{{- if eq "kubewatcher" .component }}
{{- include "kubewatcher-rules" . }}
{{- end }}
{{- if eq "mcp" .component }}
{{- include "mcp-rules" . }}
{{- end }}
{{- if eq "workflow" .component }}
{{- include "workflow-rules" . }}
{{- end }}
{{- if eq "statesvc" .component }}
{{- include "statesvc-rules" . }}
{{- end }}
{{- if eq "kafka" .component }}
{{- include "kafka-rules" . }}
{{- end }}
{{- if eq "keda" .component }}
{{- include "keda-rules" . }}
{{- end }}
{{- if eq "router" .component }}
{{- include "router-rules" . }}
{{- end }}
{{- if eq "timer" .component }}
{{- include "timer-rules" . }}
{{- end }}
{{- if eq "canaryconfig" .component }}
{{- include "canaryconfig-rules" . }}
{{- end }}
{{- if eq "executor" .component }}
{{- include "executor-rules" . }}
{{- end }}
{{- if eq "statestore-mqt" .component }}
{{- include "statestore-mqt-rules" . }}
{{- end }}
# Read FissionTenant (cluster-scoped) so the component's resolver-sync keeps its
# live tenant set current and a runtime-onboarded namespace reaches its
# membership predicate without a restart. Appended to the same rules list above.
- apiGroups:
  - fission.io
  resources:
  - fissiontenants
  verbs:
  - get
  - list
  - watch
{{- if eq "executor" .component }}
# The executor's cache goes cluster-wide for its labelled workloads (pool pods,
# managed Deployments/Services) in dynamic mode — Tier A per PRD §4.1 (these are
# not tenant secrets). Read-only, and ONLY these label-bounded workload types:
# Secrets, ConfigMaps and ReplicaSets stay namespace-scoped (per-namespace Roles +
# the scoped cache), so they are deliberately NOT granted cluster-wide here.
- apiGroups:
  - ""
  resources:
  - pods
  - services
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - apps
  resources:
  - deployments
  verbs:
  - get
  - list
  - watch
{{- end }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: "{{ .Release.Name }}-{{ .component }}-fission-cr-cluster"
subjects:
  - kind: ServiceAccount
    name: "fission-{{ .component }}"
    namespace: {{ .Release.Namespace }}
roleRef:
  kind: ClusterRole
  name: "{{ .Release.Name }}-{{ .component }}-fission-cr-cluster"
  apiGroup: rbac.authorization.k8s.io
{{- end }}
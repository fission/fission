{{- define "buildermgr-rules" }}
rules:
- apiGroups:
  - fission.io
  resources:
  - environments
  - functions
  - packages
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - fission.io
  # packages/status: the buildermgr writes the package BuildStatus and
  # build conditions through the /status subresource. functions/status is
  # needed because the buildermgr propagates package build outcome onto
  # dependent functions via markFunctionsForPackage.
  resources:
  - functions/status
  - packages/status
  verbs:
  - get
  - update
  - patch
{{- end }}
{{- define "executor-rules" }}
rules:
- apiGroups:
  - fission.io
  resources:
  - environments
  - functions
  - packages
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - fission.io
  # environments/status intentionally omitted — no executor writes
  # Environment status today (see pkg/executor/util/status.go).
  resources:
  - functions/status
  verbs:
  - get
  - update
  - patch
{{- end }}
{{- define "kubewatcher-rules" }}
rules:
- apiGroups:
  - fission.io
  resources:
  - environments
  - functions
  - kuberneteswatchtriggers
  - packages
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - fission.io
  resources:
  - kuberneteswatchtriggers/status
  verbs:
  - get
  - update
  - patch
{{- end }}

{{- define "mcp-rules" }}
rules:
# The MCP server is read-only against Functions (it watches them to build the
# tool list) and writes only the ToolExposed status condition. It never mutates
# function specs and touches no other resource.
- apiGroups:
  - fission.io
  resources:
  - functions
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - fission.io
  resources:
  - functions/status
  verbs:
  - get
  - update
  - patch
{{- end }}
{{- define "statestore-mqt-rules" }}
rules:
# functions list is required by the boot path (crd.WaitForFunctionCRDs probes
# CRD readiness with a Functions List; Forbidden there crash-loops the head).
# Delivery itself is HTTP via the router, so no environments/packages needed.
- apiGroups:
  - fission.io
  resources:
  - functions
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - fission.io
  resources:
  - messagequeuetriggers
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - fission.io
  resources:
  - messagequeuetriggers/status
  verbs:
  - get
  - update
  - patch
{{- end }}

{{- define "kafka-rules" }}
rules:
- apiGroups:
  - fission.io
  resources:
  - environments
  - functions
  - messagequeuetriggers
  - packages
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - fission.io
  resources:
  - messagequeuetriggers/status
  verbs:
  - get
  - update
  - patch
{{- end }}
{{- define "keda-rules" }}
rules:
- apiGroups:
  - fission.io
  resources:
  - environments
  - functions
  - messagequeuetriggers
  - packages
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - fission.io
  resources:
  - messagequeuetriggers/status
  verbs:
  - get
  - update
  - patch
{{- end }}
{{- define "preupgrade-rules" }}
rules:
- apiGroups:
  - fission.io
  resources:
  - canaryconfigs
  - environments
  - functions
  - httptriggers
  - kuberneteswatchtriggers
  - messagequeuetriggers
  - packages
  - timetriggers
  - workflows
  - workflowruns
  verbs:
  - list
{{- end }}
{{- define "router-rules" }}
rules:
- apiGroups:
  - fission.io
  resources:
  - environments
  - functions
  - httptriggers
  - packages
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - fission.io
  resources:
  - httptriggers/status
  verbs:
  - get
  - update
  - patch
{{- end }}
{{- define "storagesvc-rules" }}
rules:
- apiGroups:
  - fission.io
  resources:
  - packages
  verbs:
  - get
  - list
{{- end }}
{{- define "timer-rules" }}
rules:
- apiGroups:
  - fission.io
  resources:
  - environments
  - functions
  - packages
  - timetriggers
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - fission.io
  resources:
  - timetriggers/status
  verbs:
  - get
  - update
  - patch
{{- end }}
{{- define "canaryconfig-rules" }}
rules:
- apiGroups:
  - fission.io
  resources:
  - canaryconfigs
  - httptriggers
  verbs:
  - list
  - watch
  - get
  - update
# canaryconfigs/status is a separate RBAC resource once the CanaryConfig
# /status subresource exists; the reconciler writes the rollout's terminal
# status (succeeded/failed) and the Progressing/Ready conditions through it.
- apiGroups:
  - fission.io
  resources:
  - canaryconfigs/status
  verbs:
  - get
  - update
  - patch
{{- end }}

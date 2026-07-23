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
- apiGroups:
  - fission.io
  # Read-only, RFC-0025: the executor's versionretain.View watches these to
  # know which (function UID, generation) pins a live FunctionAlias still
  # references, so the idle reaper does not drain a warm pool an alias points
  # at just because a newer generation exists. The executor never writes
  # either type.
  resources:
  - functionversions
  - functionaliases
  verbs:
  - get
  - list
  - watch
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
{{- define "statesvc-rules" }}
rules:
# statesvc watches Functions to index StateConfigs (quota/keyspace resolution,
# token defense-in-depth) and updates ONLY metadata.finalizers for the
# fission.io/state-cleanup keyspace-lifecycle finalizer. It never mutates
# function specs and touches no other resource.
- apiGroups:
  - fission.io
  resources:
  - functions
  verbs:
  - get
  - list
  - watch
  - update
  - patch
{{- end }}

{{- define "workflow-rules" }}
rules:
# The workflow engine reads Workflows (spec snapshots) and Functions (the
# crd.WaitForFunctionCRDs boot probe needs list — Forbidden there
# crash-loops the head), drives WorkflowRuns (update/patch covers the
# phase-3 finalizer), and writes status on both workflow kinds. Invocation
# itself is HTTP via the router internal listener.
- apiGroups:
  - fission.io
  resources:
  - functions
  - workflows
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - fission.io
  resources:
  - workflowruns
  verbs:
  - get
  - list
  - watch
  - update
  - patch
  # delete: the retention sweeper reclaims finished runs past
  # HistoryRetention (the finalizer then cleans the stream/KV).
  - delete
- apiGroups:
  - fission.io
  resources:
  - workflows/status
  - workflowruns/status
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
  - functionaliases
  - functions
  - functionversions
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

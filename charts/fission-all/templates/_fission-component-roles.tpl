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
# The buildermgr provisions the fission-builder ServiceAccount (and its Role +
# RoleBinding) on demand in each builder namespace as environments are
# discovered (EnsureBuilderSA -> setupSAAndRoleBindings). Required under
# watch-all-namespaces, where namespaces are not known at install time and the
# chart cannot pre-create the SA. Under watch-all these rules render as a
# ClusterRole, granting the create power cluster-wide.
- apiGroups:
  - ""
  resources:
  - serviceaccounts
  verbs:
  - create
  - get
  - delete
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - rolebindings
  - roles
  verbs:
  - create
  - get
  - delete
- apiGroups:
  - authorization.k8s.io
  resources:
  - localsubjectaccessreviews
  verbs:
  - create
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
# The executor provisions the fission-fetcher ServiceAccount (and its Role +
# RoleBinding) on demand in each function namespace as functions are specialized
# (EnsureFetcherSA -> setupSAAndRoleBindings). Required under watch-all-namespaces,
# where namespaces are not known at install time and the chart cannot pre-create
# the SA. Under watch-all these rules render as a ClusterRole, granting the create
# power cluster-wide.
- apiGroups:
  - ""
  resources:
  - serviceaccounts
  verbs:
  - create
  - get
  - delete
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - rolebindings
  - roles
  verbs:
  - create
  - get
  - delete
- apiGroups:
  - authorization.k8s.io
  resources:
  - localsubjectaccessreviews
  verbs:
  - create
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

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
  verbs:
  - create
  - get
  - delete
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
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
  verbs:
  - create
  - get
  - delete
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
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
{{- end }}
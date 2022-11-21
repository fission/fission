{{- define "buildermgr-kuberules" }}
rules:
- apiGroups:
  - ""
  resources:
  - configmaps
  - pods
  - services
  - serviceaccounts
  verbs:
  - create
  - delete
  - get
  - list
  - watch
  - patch
- apiGroups:
  - apps
  resources:
  - deployments
  verbs:
  - list
  - create
  - delete
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - rolebindings
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - clusterroles
  verbs:
  - bind
{{- end }}
{{- define "canaryconfig-kuberules" }}
rules:
- apiGroups:
  - ""
  resources:
  - services
  verbs:
  - list
- apiGroups:
  - ""
  resources:
  - configmaps
  - secrets
  verbs:
  - get
- apiGroups:
  - ""
  resources:
  - namespaces
  verbs:
  - get
  - create
- apiGroups:
  - ""
  resources:
  - pods
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  verbs:
  - get
  - list
  - watch
{{- end }}
{{- define "controller-kuberules" }}
rules:
- apiGroups:
  - ""
  resources:
  - services
  verbs:
  - list
- apiGroups:
  - ""
  resources:
  - configmaps
  - secrets
  verbs:
  - get
- apiGroups:
  - ""
  resources:
  - namespaces
  verbs:
  - get
  - create
- apiGroups:
  - ""
  resources:
  - pods
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  verbs:
  - get
  - list
  - watch
{{- end }}
{{- define "executor-kuberules" }}
rules:
- apiGroups:
  - ""
  resources:
  - configmaps
  - pods
  - secrets
  - services
  - serviceaccounts
  - replicationcontrollers
  - events
  verbs:
  - create
  - delete
  - get
  - list
  - watch
  - patch
- apiGroups:
  - apps
  resources:
  - deployments
  - deployments/scale
  - replicasets
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - autoscaling
  resources:
  - horizontalpodautoscalers
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - rolebindings
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - clusterroles
  verbs:
  - bind
- apiGroups:
  - metrics.k8s.io
  resources:
  - pods
  verbs:
  - get
  - list
{{- end }}
{{- define "fluentbit-kuberules" }}
rules:
- apiGroups:
  - ""
  resources:
  - pods
  verbs:
  - get
  - list
  - watch
{{- end }}
{{- define "kubewatcher-kuberules" }}
rules:
- apiGroups:
  - ""
  resources:
  - configmaps
  - pods
  - secrets
  - services
  - serviceaccounts
  - replicationcontrollers
  - events
  verbs:
  - create
  - delete
  - get
  - list
  - watch
  - patch
- apiGroups:
  - batch
  resources:
  - jobs
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - rolebindings
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - clusterroles
  verbs:
  - bind
{{- end }}
{{- define "kafka-kuberules" }}
rules:
- apiGroups:
  - ""
  resources:
  - configmaps
  - pods
  - secrets
  - services
  - serviceaccounts
  - replicationcontrollers
  - events
  verbs:
  - create
  - delete
  - get
  - list
  - watch
  - patch
- apiGroups:
  - apps
  resources:
  - deployments
  - deployments/scale
  - replicasets
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - rolebindings
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - clusterroles
  verbs:
  - bind
{{- end }}
{{- define "keda-kuberules" }}
rules:
- apiGroups:
  - ""
  resources:
  - configmaps
  - pods
  - secrets
  - services
  - serviceaccounts
  - replicationcontrollers
  - events
  verbs:
  - create
  - delete
  - get
  - list
  - watch
  - patch
- apiGroups:
  - apps
  resources:
  - deployments
  - deployments/scale
  - replicasets
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - rolebindings
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - clusterroles
  verbs:
  - bind
- apiGroups:
  - keda.sh
  resources:
  - scaledjobs
  - scaledobjects
  - scaledjobs/finalizers
  - scaledjobs/status
  - triggerauthentications
  - triggerauthentications/status
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
{{- if .Values.mqt_keda.enabled }}
- apiGroups:
  - keda.k8s.io
  resources:
  - scaledjobs
  - scaledobjects
  - scaledjobs/finalizers
  - scaledjobs/status
  - triggerauthentications
  - triggerauthentications/status
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
{{- end }}
- apiGroups:
  - metrics.k8s.io
  resources:
  - pods
  verbs:
  - get
  - list
{{- end }}
{{- define "preupgrade-kuberules" }}
rules:
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  verbs:
  - get
  - list
  - watch
{{- end }}
{{- define "router-kuberules" }}
rules:
- apiGroups:
  - ""
  resources:
  - configmaps
  - pods
  - secrets
  - services
  - serviceaccounts
  - replicationcontrollers
  - events
  verbs:
  - create
  - delete
  - get
  - list
  - watch
  - patch
- apiGroups:
  - networking.k8s.io
  resources:
  - ingresses
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - rolebindings
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - clusterroles
  verbs:
  - bind
{{- end }}
{{- define "timer-kuberules" }}
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

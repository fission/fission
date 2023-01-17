{{- define "buildermgr-kuberules" }}
rules:
- apiGroups:
  - ""
  resources:
  - pods
  - services
  verbs:
  - create
  - delete
  - get
  - list
  - watch
  - patch
- apiGroups:
  - ""
  resources:
  - configmaps
  - secrets
  verbs:
  - get
  - list
  - watch
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
  - pods
  - services
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
  - ""
  resources:
  - configmaps
  - secrets
  verbs:
  - get
  - list
  - watch
{{- if .Values.executor.serviceAccountCheck.enabled }}  
- apiGroups:
  - ""
  resources:
  - serviceaccounts
  verbs:
  - create
  - get
- apiGroups:
  - authorization.k8s.io
  resources:
  - localsubjectaccessreviews
  verbs:
  - create   
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - rolebindings
  - roles
  verbs:
  - create
{{- end }}      
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
  - replicationcontrollers
  - events
  verbs:
  - get
  - list
  - watch
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
  - ""
  resources:
  - configmaps
  - secrets
  verbs:
  - get
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
{{- end }}
{{- define "keda-kuberules" }}
rules:
- apiGroups:
  - ""
  resources:
  - pods
  - services
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
  - ""
  resources:
  - configmaps
  - secrets
  verbs:
  - get
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
# TODO: Kept for future in case preupgrade needs any permissions in the future
rules: []
{{- end }}
{{- define "router-kuberules" }}
rules:
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
{{- end }}
{{- define "timer-kuberules" }}
rules: []
{{- end }}

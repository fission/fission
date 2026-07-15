{{/*
leases-kuberules grants the permissions a control-plane subsystem needs for
controller-runtime native leader election (active-passive HA): the
coordination.k8s.io/leases lock plus events create/patch for the Manager's
leader-election event recorder. Included by every subsystem that supports
leader election. (Some roles also grant events for other reasons; duplicate
rules are merged by the API server.)
*/}}
{{- define "leases-kuberules" }}
- apiGroups:
  - coordination.k8s.io
  resources:
  - leases
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
  - events
  verbs:
  - create
  - patch
{{- end }}
{{- define "buildermgr-kuberules" }}
rules:
{{- include "leases-kuberules" . }}
- apiGroups:
  - ""
  resources:
  - events
  verbs:
  - create
  - patch
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
{{- include "leases-kuberules" . }}
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
{{- define "executor-kuberules" }}
rules:
{{- include "leases-kuberules" . }}
- apiGroups:
  - ""
  resources:
  - pods
  - replicationcontrollers
  verbs:
  - create
  - delete
  - get
  - list
  - watch
  - patch
# Services additionally need `update`: AdoptExistingResources re-stamps an
# existing Service in place (newdeploy/container createOrGetService), which the
# executor never does in steady state — so without `update` adopt 403s on the
# Service and aborts before re-stamping the backing Deployment.
- apiGroups:
  - ""
  resources:
  - services
  verbs:
  - create
  - delete
  - get
  - list
  - watch
  - patch
  - update
- apiGroups:
  - ""
  resources:
  - events
  verbs:
  - create
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
{{- define "kubewatcher-kuberules" }}
rules:
{{- include "leases-kuberules" . }}
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
{{- define "statestore-mqt-kuberules" }}
rules:
{{- include "leases-kuberules" . }}
- apiGroups:
  - ""
  resources:
  - events
  verbs:
  - create
  - patch
{{- end }}

{{- define "kafka-kuberules" }}
rules:
{{- include "leases-kuberules" . }}
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
{{- include "leases-kuberules" . }}
- apiGroups:
  - ""
  resources:
  - pods
  - services
  - replicationcontrollers
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
  - events
  verbs:
  - create
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
# TODO: Currently, router needs ingress related permissions only.
# In future if router's permissions are modified then check the configured namespace.
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
{{- if .Values.gatewayAPI.enabled }}
# Gateway API route provider (gatewayAPI.enabled): the router creates HTTPRoute
# objects per HTTPTrigger that requests the "gateway" provider. ReferenceGrants
# are read-only — a cross-namespace HTTPRoute->Gateway parentRef requires a
# ReferenceGrant the cluster operator owns; Fission does not create one.
- apiGroups:
  - gateway.networking.k8s.io
  resources:
  - httproutes
  verbs:
  - create
  - get
  - list
  - watch
  - update
  - patch
  - delete
- apiGroups:
  - gateway.networking.k8s.io
  resources:
  - referencegrants
  verbs:
  - get
  - list
  - watch
{{- end }}
{{- end }}
{{- define "timer-kuberules" }}
rules:
{{- include "leases-kuberules" . }}
{{- end }}

{{/*
router-dataplane-kuberules: the RFC-0002 EndpointSlice data-plane read grant
(Fission-managed function Services' EndpointSlices + the Services themselves).
Single-sourced so the per-namespace Role (router/role-dataplane.yaml) and the
cluster-wide ClusterRole (tenant-controller/cluster-mode-bindings.yaml) cannot
drift.
*/}}
{{- define "router-dataplane-kuberules" }}
rules:
- apiGroups:
  - discovery.k8s.io
  resources:
  - endpointslices
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - ""
  resources:
  - services
  verbs:
  - get
  - list
  - watch
{{- end }}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

var (
	MinimumKubernetesVersion = [3]int{1, 32, 0}
)

const (
	EXECUTOR_INSTANCEID_LABEL string = "executorInstanceId"
	DEFAULT_FUNCTION_TIMEOUT  int    = 60

	// DefaultStreamIdleSeconds is the idle timeout applied to a streaming function
	// when StreamingConfig.IdleTimeoutSeconds is unset. Overridable cluster-wide via
	// the router's ROUTER_STREAM_IDLE_TIMEOUT env.
	DefaultStreamIdleSeconds int = 60
)

const (
	// ResourceVersionCount env variable is used for updating configmaps and secrets in pods
	ResourceVersionCount string = "RESOURCE_VERSION_COUNT"
)

const (
	// TenantAuthKeysSecret is the controller-owned Secret (one per tenant
	// namespace) holding that namespace's derived HMAC keys. It is deliberately
	// a DIFFERENT name from the chart's master-bearing "fission-internal-auth":
	// an existing install already replicated the master copy into every function
	// namespace, so a same-named controller Secret would collide (AlreadyExists)
	// and silently never write the derived keys, leaving the data plane to 401.
	// A distinct name lets the controller create it cleanly, own it fully for
	// teardown (without touching the Helm-managed master Secret), and reach the
	// "master never in a tenant namespace" end state by simply having the chart
	// stop replicating the master copy — no in-place merge/removal needed.
	TenantAuthKeysSecret = "fission-internal-auth-keys"

	// Data-key fields inside TenantAuthKeysSecret. Shared by the tenant
	// controller (writer) and the fetcher pod-spec (reader) so the two cannot
	// drift.
	TenantAuthFetcherKey = "fetcherKey"
	TenantAuthBuilderKey = "builderKey"
	TenantAuthStorageKey = "storageKey"
)

const (
	// AuthKeySchemeAnnotation records which HMAC key scheme a fetcher-bearing
	// pod was created with, so the executor signs each /specialize call with the
	// key that pod's verifier actually expects (version-aware signing across a
	// rolling upgrade). It is stamped on the pod template only when dynamic
	// multi-namespace tenancy is on for the pod's namespace; its absence means
	// the master-derived key scheme, which is the only scheme pre-tenancy pods
	// and all single-namespace installs ever use.
	AuthKeySchemeAnnotation string = "fission.io/auth-key-scheme"

	// AuthKeySchemeNamespace is the AuthKeySchemeAnnotation value meaning the
	// pod's fetcher verifies with a per-namespace derived key (it holds only its
	// own namespace's key, never the master), so the executor must sign with
	// ServiceSignerNS for the pod's namespace.
	AuthKeySchemeNamespace string = "namespace"
)

// HasNamespaceKeyScheme reports whether a fetcher/builder pod's annotations mark
// it as verifying with a per-namespace derived key (the AuthKeySchemeNamespace
// scheme). The executor and buildermgr read it to choose version-aware signing;
// keeping the annotation key/value pairing in one place means a change to the
// scheme is a single edit.
func HasNamespaceKeyScheme(annotations map[string]string) bool {
	return annotations[AuthKeySchemeAnnotation] == AuthKeySchemeNamespace
}

const (
	ChecksumTypeSHA256 ChecksumType = "sha256"
)

const (
	// ArchiveTypeLiteral means the package contents are specified in the Literal field of
	// resource itself.
	ArchiveTypeLiteral ArchiveType = "literal"

	// ArchiveTypeUrl means the package contents are at the specified URL.
	ArchiveTypeUrl ArchiveType = "url"

	// ArchiveTypeOCI means the package contents are the filesystem of an
	// OCI image referenced in the OCI field of the resource.
	ArchiveTypeOCI ArchiveType = "oci"
)

const (
	BuildStatusPending   = "pending"
	BuildStatusRunning   = "running"
	BuildStatusSucceeded = "succeeded"
	BuildStatusFailed    = "failed"
	BuildStatusNone      = "none"
)

const (
	AllowedFunctionsPerContainerSingle   = "single"
	AllowedFunctionsPerContainerInfinite = "infinite"
)

const (
	// StreamingAuto flushes immediately and lets the upstream decide the framing
	// (SSE, chunked, or a WebSocket Upgrade); the safe default.
	StreamingAuto      StreamingProtocol = "auto"
	StreamingSSE       StreamingProtocol = "sse"
	StreamingChunked   StreamingProtocol = "chunked"
	StreamingWebSocket StreamingProtocol = "websocket"
)

const (
	ExecutorTypePoolmgr   ExecutorType = "poolmgr"
	ExecutorTypeNewdeploy ExecutorType = "newdeploy"
	ExecutorTypeContainer ExecutorType = "container"
)

// RFC-0023 keyed-state defaults and sticky-routing sources.
const (
	StickySourceHeader     StickySource = "header"
	StickySourceQueryParam StickySource = "queryparam"

	// DefaultStateMaxValueBytes caps a single state value when
	// StateConfig.MaxValueBytes is unset (256KiB; blobs belong in object
	// storage).
	DefaultStateMaxValueBytes int64 = 262144

	// DefaultStateMaxKeys caps a keyspace's live keys when
	// StateConfig.MaxKeys is unset.
	DefaultStateMaxKeys int64 = 10000
)

const (
	StrategyTypeExecution = "execution"
)

const (
	RuntimePodSpecPath = "/etc/fission/runtime-podspec-patch.yaml"
	BuilderPodSpecPath = "/etc/fission/builder-podspec-patch.yaml"
)

const (
	SharedVolumeUserfunc   = "userfunc"
	SharedVolumePackages   = "packages"
	SharedVolumeSecrets    = "secrets"
	SharedVolumeConfigmaps = "configmaps"
	PodInfoVolume          = "podinfo"
	PodInfoMount           = "/etc/podinfo"
)

const (
	MessageQueueTypeKafka = "kafka"
	// MessageQueueTypeStatestore is the RFC-0027 built-in provider: topics are
	// EventLog streams on the RFC-0021 statestore — no external broker.
	MessageQueueTypeStatestore = "statestore"
)

const (
	// FunctionReferenceFunctionName means that the function
	// reference is simply by function name.
	FunctionReferenceTypeFunctionName = "name"

	FunctionReferenceTypeFunctionWeights = "function-weights"

	// Other function reference types we'd like to support:
	//   Versioned function, latest version
	//   Versioned function. by semver "latest compatible"
	//   Set of function references (recursively), by percentage of traffic
)

const (
	// failure type currently supported is http status code. This could be extended
	// in the future.
	FailureTypeStatusCode FailureType = "status-code"

	// Status of canary config can be one of the following
	CanaryConfigStatusPending   = "pending"
	CanaryConfigStatusSucceeded = "succeeded"
	CanaryConfigStatusFailed    = "failed"
	CanaryConfigStatusAborted   = "aborted"

	// set a max number for iterations to prevent infinite processing of canary config
	MaxIterationsForCanaryConfig = 10
)

const (
	DefaultSpecializationTimeOut = 120
)

const (
	FETCH_SOURCE = iota
	FETCH_DEPLOYMENT
	FETCH_URL
)

// executor kubernetes object label key
const (
	ENVIRONMENT_NAMESPACE     = "environmentNamespace"
	ENVIRONMENT_NAME          = "environmentName"
	ENVIRONMENT_UID           = "environmentUid"
	FUNCTION_NAMESPACE        = "functionNamespace"
	FUNCTION_NAME             = "functionName"
	FUNCTION_UID              = "functionUid"
	FUNCTION_RESOURCE_VERSION = "functionResourceVersion"
	EXECUTOR_TYPE             = "executorType"
	MANAGED                   = "managed"
	// POOL_OCI_IMAGE_HASH marks pool pods whose userfunc volume is an OCI
	// image volume (RFC-0001 Path B). Pools are keyed per (env UID, image
	// hash); the pod reconciler routes warm pods on this label. Absent on
	// pods of plain (fetcher-based) pools.
	POOL_OCI_IMAGE_HASH = "ociImageHash"

	// RFC-0002 EndpointSlice-native data plane labels.
	//
	// FUNCTION_GENERATION labels a specialized pool pod with the Function
	// generation it was specialized from. The per-function Service selector
	// includes it so stale-generation pods drop out of the EndpointSlices on a
	// function update (the executor-side equivalent is CacheKeyUG keying).
	FUNCTION_GENERATION = "fission.io/function-generation"
	// SERVED_LABEL gates a specialized pod's membership in its function
	// Service: pool pods pass readiness probes before specialization, so the
	// label is set only by the post-specialization patch — without it the
	// EndpointSlice controller would publish a relabeled-but-unspecialized pod
	// as a ready endpoint.
	SERVED_LABEL = "fission.io/served"
	// SERVED_VALUE is SERVED_LABEL's only valid value. The Service selector
	// (gp_service.go) and the post-specialize pod patch (gp.go) live in
	// different files and MUST agree byte-for-byte — drift means specialized
	// pods silently never join their Service.
	SERVED_VALUE = "true"
	// PROVISIONED_LABEL marks a served pod the provisioner (RFC-0026) is
	// actively keeping warm. The idle reaper exempts these pods; the
	// provisioner counts them toward the function's floor.
	PROVISIONED_LABEL = "fission.io/provisioned"
	// PROVISIONED_VALUE is PROVISIONED_LABEL's only valid value.
	PROVISIONED_VALUE = "true"
	// MANAGED_BY_LABEL marks the Services Fission's data plane owns; the
	// EndpointSlice controller mirrors Service labels onto slices, and the
	// router's slice informer filters on it.
	MANAGED_BY_LABEL = "fission.io/managed-by"
	MANAGED_BY_VALUE = "fission"

	// ConcurrencyEnforcementAnnotation opts a Function out of router-local
	// admission (RFC-0002): with the value "strict" every request goes through
	// the executor's PoolCache exactly as before the EndpointSlice data plane.
	// See Function.StrictConcurrencyEnforcement.
	ConcurrencyEnforcementAnnotation = "fission.io/concurrency-enforcement"
	ConcurrencyEnforcementStrict     = "strict"
)

const (
	ANNOTATION_SVC_HOST = "svcHost"
)

const (
	ArchiveLiteralSizeLimit int64 = 256 * 1024
)

const (
	FissionBuilderSA = "fission-builder"
	FissionFetcherSA = "fission-fetcher"

	// Control-plane ServiceAccounts (in the install/release namespace) that need
	// workload RBAC in each tenant namespace under dynamic tenancy. The tenant
	// controller binds them to the *TenantWorkloadClusterRole below per namespace.
	FissionExecutorSA   = "fission-executor"
	FissionBuildermgrSA = "fission-buildermgr"

	// The *TenantWorkloadClusterRole names are the fixed-name ClusterRoles
	// (chart-rendered only in dynamic mode) holding the per-namespace rules a
	// runtime-onboarded tenant needs. The controller references them by name in
	// the RoleBindings it provisions, so the names must match the chart and be
	// install-independent (dynamic tenancy is one Fission install per cluster,
	// since it watches cluster-wide). Executor/buildermgr carry workload-management
	// rules; fetcher/builder/fetcher-websocket carry the function-pod sidecar read
	// rules — all single-sourced from the chart's shared partials so the static
	// and dynamic paths cannot drift.
	ExecutorTenantWorkloadClusterRole         = "fission-executor-tenant-workload"
	BuildermgrTenantWorkloadClusterRole       = "fission-buildermgr-tenant-workload"
	FetcherTenantWorkloadClusterRole          = "fission-fetcher-tenant-workload"
	BuilderTenantWorkloadClusterRole          = "fission-builder-tenant-workload"
	FetcherWebsocketTenantWorkloadClusterRole = "fission-fetcher-websocket-tenant-workload"
)

const (
	CanaryConfigResource    = "canaryconfigs"
	EnvironmentResource     = "environments"
	FunctionResource        = "functions"
	HttpTriggerResource     = "httptriggers"
	KubernetesWatchResource = "kuberneteswatchtriggers"
	MessageQueueResource    = "messagequeuetriggers"
	PackagesResource        = "packages"
	TimeTriggerResource     = "timetriggers"
)

const (
	Pods        = "pods"
	Deployments = "deployments"
	ReplicaSets = "replicasets"
	Services    = "services"
	ConfigMaps  = "configmaps"
	Secrets     = "secrets"
)

const (
	BuilderContainerName = "builder"
)

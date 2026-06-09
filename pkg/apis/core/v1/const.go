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
	ChecksumTypeSHA256 ChecksumType = "sha256"
)

const (
	// ArchiveTypeLiteral means the package contents are specified in the Literal field of
	// resource itself.
	ArchiveTypeLiteral ArchiveType = "literal"

	// ArchiveTypeUrl means the package contents are at the specified URL.
	ArchiveTypeUrl ArchiveType = "url"
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

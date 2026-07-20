// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package flagkey

const (
	Verbosity   = "verbosity"
	Server      = "server"
	ClientOnly  = "client-only"
	KubeContext = "kube-context"

	PreCheckOnly = "pre"

	resourceName = "name"
	force        = "force"
	Output       = "output"

	Labels     = "labels"
	Annotation = "annotation"

	IgnoreNotFound = "ignorenotfound"

	Namespace      = "namespace"
	ForceNamespace = "force-namespace"
	AllNamespaces  = "all-namespaces"
	NamespacePod   = "pod-namespace"
	ForceDelete    = "force"

	RuntimeMincpu      = "mincpu"
	RuntimeMaxcpu      = "maxcpu"
	RuntimeMinmemory   = "minmemory"
	RuntimeMaxmemory   = "maxmemory"
	RuntimeTargetcpu   = "targetcpu"
	RunImagePullSecret = "imagepullsecret"

	ReplicasMinscale = "minscale"
	ReplicasMaxscale = "maxscale"

	FnName                  = resourceName
	FnSpecializationTimeout = "specializationtimeout"
	FnEnvironmentName       = "env"
	FnPackageName           = "pkgname"
	FnImageName             = "image"
	FnPort                  = "port"
	FnCommand               = "command"
	FnArgs                  = "args"
	FnEntrypoint            = "entrypoint"
	FnBuildCmd              = "buildcmd"
	FnSecret                = "secret"
	FnForce                 = force
	FnCfgMap                = "configmap"
	FnExecutorType          = "executortype"
	FnExecutionTimeout      = "fntimeout"
	FnTestTimeout           = "timeout"
	FnLogPod                = "pod"
	FnLogFollow             = "follow"
	FnLogDetail             = "detail"
	FnLogDBType             = "dbtype"
	FnLogReverseQuery       = "reverse"
	FnLogCount              = "recordcount"
	FnLogRequestID          = "request-id"
	FnLogTraceID            = "trace-id"
	FnLogLevel              = "level"
	FnTestBody              = "body"
	FnTestHeader            = "header"
	FnTestQuery             = "query"
	FnIdleTimeout           = "idletimeout"
	FnStreaming             = "streaming"
	FnStreamingProtocol     = "streamingprotocol"
	FnStreamingIdleTimeout  = "streamingidletimeout"
	FnStreamingMaxDuration  = "streamingmaxduration"
	FnExposeAsMCP           = "expose-as-mcp"
	FnToolDescription       = "tool-description"
	FnToolInputSchema       = "tool-input-schema"
	FnToolName              = "tool-name"
	FnConcurrency           = "concurrency"
	FnRequestsPerPod        = "requestsperpod"
	FnOnceOnly              = "onceonly"
	FnSubPath               = "subpath"
	FnRunEnvVersion         = "env-version"
	FnRunKeep               = "keep"
	FnRunWatch              = "watch"
	FnRunDebugPort          = "debug-port"
	FnRunEnvVar             = "env-var"
	FnRunEnvFile            = "env-from"
	FnRunBuild              = "build"
	FnRunBuilderImage       = "builder-image"
	FnGracePeriod           = "graceperiod"
	FnLogAllPods            = "all-pods"
	FnRetainPods            = "retainpods"

	DlqID    = "id"
	DlqAll   = "all"
	DlqLimit = "limit"

	FnTestAsync = "async"

	// RFC-0024 async invocation config (fn create/update).
	FnAsyncMaxAttempts = "async-retry-max-attempts"
	FnAsyncMaxAge      = "async-max-age"
	FnAsyncOnSuccess   = "async-on-success"
	FnAsyncOnFailure   = "async-on-failure"
	DlqQueue           = "queue"
	// RFC-0027 `fission topic` dev commands.
	TopicName        = "topic"
	TopicData        = "data"
	TopicContentType = "content-type"

	TopicMQType = "mqtype"
	TopicLimit  = "limit"
	// RFC-0027 topic destinations (statestore built-in eventing).
	FnAsyncOnSuccessTopic = "async-on-success-topic"
	FnAsyncOnFailureTopic = "async-on-failure-topic"

	// RFC-0023 `fission fn state` admin commands.
	StateKey       = "key"
	StateValue     = "value"
	StatePrefix    = "prefix"
	StateTTL       = "ttl"
	StateIfVersion = "if-version"

	// RFC-0023 keyed-state config (fn create/update).
	FnState              = "state"
	FnStateKeyspace      = "state-keyspace"
	FnStateMaxKeys       = "state-max-keys"
	FnStateMaxValueBytes = "state-max-value-bytes"
	FnStateTTL           = "state-ttl"
	FnStateStickySource  = "state-sticky-source"
	FnStateStickyName    = "state-sticky-name"

	HtName              = resourceName
	HtMethod            = "method"
	HtInvocationMode    = "invocation-mode"
	HtUrl               = "url"
	HtHost              = "host"
	HtIngress           = "createingress"
	HtIngressRule       = "ingressrule"
	HtIngressAnnotation = "ingressannotation"
	HtIngressTLS        = "ingresstls"
	HtRouteProvider     = "route-provider"
	HtRouteHost         = "route-host"
	HtRoutePath         = "route-path"
	HtRouteAnnotation   = "route-annotation"
	HtRouteTLS          = "route-tls"
	HtGateway           = "gateway"
	HtFnName            = "function"
	HtFnWeight          = "weight"
	HtFilter            = HtFnName
	HtPrefix            = "prefix"
	HtKeepPrefix        = "keepprefix"

	TokUsername = "username"
	TokPassword = "password"
	TokAuthURI  = "authuri"

	TtName   = resourceName
	TtCron   = "cron"
	TtFnName = "function"
	TtRound  = "round"
	TtMethod = "method"

	WfName     = resourceName
	WfFile     = "file"
	WfOffline  = "offline"
	WfInput    = "input"
	WfIO       = "io"
	WfWorkflow = "workflow"
	WfOpen     = "open"

	MqtName            = resourceName
	MqtFnName          = "function"
	MqtMQType          = "mqtype"
	MqtTopic           = "topic"
	MqtRespTopic       = "resptopic"
	MqtErrorTopic      = "errortopic"
	MqtMaxRetries      = "maxretries"
	MqtMsgContentType  = "contenttype"
	MqtPollingInterval = "pollinginterval"
	MqtCooldownPeriod  = "cooldownperiod"
	MqtMinReplicaCount = "minreplicacount"
	MqtMaxReplicaCount = "maxreplicacount"
	MqtMetadata        = "metadata"
	MqtSecret          = "secret"
	MqtKind            = "mqtkind"

	EnvName            = resourceName
	EnvPoolsize        = "poolsize"
	EnvImage           = "image"
	EnvBuilderImage    = "builder"
	EnvBuildcommand    = "buildcmd"
	EnvKeeparchive     = "keeparchive"
	EnvExternalNetwork = "externalnetwork"
	EnvGracePeriod     = "graceperiod"
	EnvVersion         = "version"
	EnvImagePullSecret = "imagepullsecret"
	EnvExecutorType    = "executortype"
	EnvForce           = force
	EnvBuilder         = "builder-env"
	EnvRuntime         = "runtime-env"

	KwName      = resourceName
	KwFnName    = "function"
	KwNamespace = "namespace"
	KwObjType   = "type"
	KwLabels    = "labels"

	PkgName           = resourceName
	PkgForce          = force
	PkgEnvironment    = "env"
	PkgCode           = "code"
	PkgSrcArchive     = "sourcearchive"
	PkgDeployArchive  = "deployarchive"
	PkgSrcChecksum    = "srcchecksum"
	PkgDeployChecksum = "deploychecksum"
	PkgInsecure       = "insecure"
	PkgOCI            = "oci"
	PkgBuildCmd       = "buildcmd"
	PkgOutput         = Output
	PkgStatus         = "status"
	PkgOrphan         = "orphan"

	SpecSave             = "spec"
	SpecDir              = "specdir"
	SpecName             = resourceName
	SpecDeployID         = "deployid"
	SpecWait             = "wait"
	SpecWatch            = "watch"
	SpecDelete           = "delete"
	SpecDry              = "dry"
	SpecApplyDryRun      = "dry-run"
	SpecValidate         = "validation"
	SpecIgnore           = "specignore"
	SpecApplyCommitLabel = "commitlabel"
	SpecAllowConflicts   = "allowconflicts"

	SupportOutput = Output
	SupportNoZip  = "nozip"

	CanaryName              = resourceName
	CanaryHTTPTriggerName   = "httptrigger"
	CanaryNewFunc           = "newfunction"
	CanaryOldFunc           = "oldfunction"
	CanaryWeightIncrement   = "increment-step"
	CanaryIncrementInterval = "increment-interval"
	CanaryFailureThreshold  = "failure-threshold"

	ArchiveName   = resourceName
	ArchiveID     = "id"
	ArchiveOutput = Output

	WaitFor     = "for"
	WaitTimeout = "timeout"

	// FissionTenant (multi-namespace tenancy)
	TenantFunctionNamespace = "function-namespace"
	TenantBuilderNamespace  = "builder-namespace"
	TenantForce             = "force"

	DefaultSpecOutputDir = "fission-dump"
)

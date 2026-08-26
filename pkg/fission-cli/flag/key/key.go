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

	FnName                   = resourceName
	FnSpecializationTimeout  = "specializationtimeout"
	FnEnvironmentName        = "env"
	FnPackageName            = "pkgname"
	FnImageName              = "image"
	FnPort                   = "port"
	FnCommand                = "command"
	FnArgs                   = "args"
	FnEntrypoint             = "entrypoint"
	FnBuildCmd               = "buildcmd"
	FnSecret                 = "secret"
	FnForce                  = force
	FnCfgMap                 = "configmap"
	FnSecretMount            = "secret-mount"
	FnCfgMapMount            = "configmap-mount"
	FnEnvVar                 = "env-var"
	FnEnvFromSecret          = "env-from-secret"
	FnEnvFromConfigMap       = "env-from-configmap"
	FnExecutorType           = "executortype"
	FnExecutionTimeout       = "fntimeout"
	FnTestTimeout            = "timeout"
	FnLogPod                 = "pod"
	FnLogFollow              = "follow"
	FnLogDetail              = "detail"
	FnLogDBType              = "dbtype"
	FnLogReverseQuery        = "reverse"
	FnLogCount               = "recordcount"
	FnLogRequestID           = "request-id"
	FnLogTraceID             = "trace-id"
	FnLogLevel               = "level"
	FnTestBody               = "body"
	FnTestHeader             = "header"
	FnTestQuery              = "query"
	FnIdleTimeout            = "idletimeout"
	FnStreaming              = "streaming"
	FnStreamingProtocol      = "streamingprotocol"
	FnStreamingIdleTimeout   = "streamingidletimeout"
	FnStreamingMaxDuration   = "streamingmaxduration"
	FnExposeAsMCP            = "expose-as-mcp"
	FnToolDescription        = "tool-description"
	FnToolInputSchema        = "tool-input-schema"
	FnToolName               = "tool-name"
	FnConcurrency            = "concurrency"
	FnRequestsPerPod         = "requestsperpod"
	FnOnceOnly               = "onceonly"
	FnSubPath                = "subpath"
	FnRunEnvVersion          = "env-version"
	FnRunKeep                = "keep"
	FnRunWatch               = "watch"
	FnRunDebugPort           = "debug-port"
	FnRunEnvVar              = "env-var"
	FnRunEnvFile             = "env-from"
	FnRunBuild               = "build"
	FnRunBuilderImage        = "builder-image"
	FnGracePeriod            = "graceperiod"
	FnLogAllPods             = "all-pods"
	FnRetainPods             = "retainpods"
	FnProvisionedConcurrency = "provisioned-concurrency"
	FnProvisionedSchedule    = "provisioned-schedule"

	// RFC-0025 versioning opt-in (fn create/update).
	FnVersioning     = "versioning"
	FnRetainVersions = "retain-versions"

	DlqID    = "id"
	DlqAll   = "all"
	DlqLimit = "limit"

	FnTestAsync = "async"

	// RFC-0025 `fission fn test --alias`/`--version`: smoke-test a specific
	// FunctionAlias or pinned FunctionVersion instead of the live function.
	// Mutually exclusive with each other; the strings themselves are shared
	// with AliasName/AliasVersion/FnRollbackAlias -- flag names are scoped
	// per-subcommand, so reuse is fine.
	FnTestAlias   = "alias"
	FnTestVersion = "version"

	// RFC-0025 `fission fn describe --version`: render the SNAPSHOT inspector
	// for one pinned FunctionVersion instead of the live function view. The
	// string is reused from FnTestVersion/FnPodsVersion/FnLogVersion -- flag
	// names are scoped per-subcommand, so this is safe (see the FnTestAlias
	// comment above).
	FnDescribeVersion = "version"

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

	// Agent runtime config (fn create/update).
	FnAgent              = "agent"
	FnAgentSessionSource = "agent-session-source"
	FnAgentSessionName   = "agent-session-name"
	FnAgentIdleAfter     = "agent-idle-after"
	FnAgentArchiveAfter  = "agent-archive-after"
	FnAgentMaxSessions   = "agent-max-sessions"
	FnAgentHistoryTrim   = "agent-history-trim"

	// `fission agent create` scaffold (Task 2, G17 DX). --code reuses
	// PkgCode's "code" key below (same meaning: the single handler file's
	// path), so only --lang is genuinely new here.
	FnAgentLang = "lang"

	// `fission agent sessions` introspection CLI (slice 4).
	AgentSession      = "session"
	AgentToken        = "agent-token"
	AgentSessionsTree = "tree"

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
	HtFnAlias           = "function-alias"
	HtFnVersion         = "function-version"
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
	EnvRuntimeClass    = "runtimeclass"

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
	PkgSrcSecret      = "srcsecret"
	PkgDeploySecret   = "deploysecret"
	PkgInsecure       = "insecure"
	PkgOCI            = "oci"
	PkgSrcOCI         = "srcoci"
	PkgBuildCmd       = "buildcmd"
	PkgOutput         = Output
	PkgStatus         = "status"
	PkgOrphan         = "orphan"
	PkgWatch          = "watch"

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

	// RFC-0025 `fission fn publish`.
	PublishDescription = "description"
	PublishWait        = "wait"

	// RFC-0025 `fission alias` (FunctionAlias) commands.
	AliasName             = resourceName
	AliasFunction         = "function"
	AliasVersion          = "version"
	AliasPackageDigest    = "package-digest"
	AliasWeight           = "weight"
	AliasSecondaryVersion = "secondary-version"
	AliasClearWeight      = "clear-weight"
	AliasWait             = "wait"

	// RFC-0025 `fission fn rollback`.
	FnRollbackAlias  = "alias"
	FnRollbackTo     = "to"
	FnRollbackDetach = "detach"
	FnRollbackWait   = "wait"

	// RFC-0025 `fission fn gc-versions`.
	GCVersionsKeep = "keep"

	DefaultSpecOutputDir = "fission-dump"
)

/*
Copyright 2019 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package flag

import (
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/types"
)

type (
	FlagType = int

	FlagSet struct {
		Required []Flag
		Optional []Flag
	}

	Flag struct {
		Name         string
		Type         FlagType
		Short        string // one-letter abbreviated flag
		Aliases      []string
		Usage        string
		DefaultValue interface{}

		// If a flag is marked as deprecated, it will hided from
		// the help message automatically. Hence, a flag cannot be
		// marked as hidden and deprecated at the same time.
		Hidden     bool
		Deprecated bool
		// flag name to replace the deprecated flag
		Substitute string
	}
)

const (
	Bool FlagType = iota + 1
	String
	StringSlice
	Int
	IntSlice
	Int64
	Int64Slice
	Float32
	Float64
	Duration
)

var (
	GlobalVerbosityFlag = Flag{Type: Int, Name: flagkey.Verbosity, Short: "v", Usage: "CLI verbosity (0 is quiet, 1 is the default, 2 is verbose)", DefaultValue: 1}
	GlobalServerFlag    = Flag{Type: String, Name: flagkey.Server, Usage: "Server URL"}

	NamespaceFunctionFlag    = Flag{Type: String, Name: flagkey.NamespaceFunction, Aliases: []string{"fns"}, Usage: "Namespace for function object", DefaultValue: metav1.NamespaceDefault}
	NamespaceEnvironmentFlag = Flag{Type: String, Name: flagkey.NamespaceEnvironment, Aliases: []string{"envns"}, Usage: "Namespace for environment object", DefaultValue: metav1.NamespaceDefault}
	NamespacePackageFlag     = Flag{Type: String, Name: flagkey.NamespacePackage, Aliases: []string{"pkgns"}, Usage: "Namespace for package object", DefaultValue: metav1.NamespaceDefault}
	NamespaceTriggerFlag     = Flag{Type: String, Name: flagkey.NamespaceTrigger, Aliases: []string{"triggerns"}, Usage: "Namespace for trigger object", DefaultValue: metav1.NamespaceDefault}
	NamespaceRecorderFlag    = Flag{Type: String, Name: flagkey.NamespaceRecorder, Aliases: []string{"recorderns"}, Usage: "Namespace for recorder object", DefaultValue: metav1.NamespaceDefault}
	NamespaceCanaryFlag      = Flag{Type: String, Name: flagkey.NamespaceCanary, Aliases: []string{"canaryns"}, Usage: "Namespace for canary config object", DefaultValue: metav1.NamespaceDefault}

	RunTimeMinCPUFlag    = Flag{Type: Int, Name: flagkey.RuntimeMincpu, Usage: "Minimum CPU to be assigned to pod (In millicore, minimum 1)"}
	RunTimeMaxCPUFlag    = Flag{Type: Int, Name: flagkey.RuntimeMaxcpu, Usage: "Maximum CPU to be assigned to pod (In millicore, minimum 1)"}
	RunTimeTargetCPUFlag = Flag{Type: Int, Name: flagkey.RuntimeTargetcpu, Usage: "Target average CPU usage percentage across pods for scaling", DefaultValue: 80}
	RunTimeMinMemoryFlag = Flag{Type: Int, Name: flagkey.RuntimeMinmemory, Usage: "Minimum memory to be assigned to pod (In megabyte)"}
	RunTimeMaxMemoryFlag = Flag{Type: Int, Name: flagkey.RuntimeMaxmemory, Usage: "Maximum memory to be assigned to pod (In megabyte)"}

	ReplicasMinFlag = Flag{Type: Int, Name: flagkey.ReplicasMinscale, Usage: "Minimum number of pods (Uses resource inputs to configure HPA)", DefaultValue: 1}
	ReplicasMaxFlag = Flag{Type: Int, Name: flagkey.ReplicasMaxscale, Usage: "Maximum number of pods (Uses resource inputs to configure HPA)", DefaultValue: 1}

	FnNameFlag                  = Flag{Type: String, Name: flagkey.FnName, Usage: "Function name"}
	FnSpecializationTimeoutFlag = Flag{Type: Int, Name: flagkey.FnSpecializationTimeout, Aliases: []string{"st"}, Usage: "Timeout for newdeploy to wait for function pod creation", DefaultValue: fv1.DefaultSpecializationTimeOut}
	FnEnvNameFlag               = Flag{Type: String, Name: flagkey.FnEnvironmentName, Usage: "Environment name for function"}
	FnCodeFlag                  = Flag{Type: String, Name: flagkey.FnCode, Usage: "Local path or URL for single file source code"}
	FnKeepURLFlag               = Flag{Type: Bool, Name: flagkey.PkgKeepURL, Aliases: []string{"keepurl"}, Usage: "Keep the providing URL in archive instead of downloading file from it. (If set, no checksum will be generated for file integrity check. You must ensure the file won't be changed.)"}
	FnPkgNameFlag               = Flag{Type: String, Name: flagkey.FnPackageName, Aliases: []string{"pkg"}, Usage: "Name of the existing package (--deploy and --src and --env will be ignored), should be in the same namespace as the function"}
	FnEntryPointFlag            = Flag{Type: String, Name: flagkey.FnEntrypoint, Aliases: []string{"entry"}, Usage: "Entry point for environment v2 to load with"}
	FnBuildCmdFlag              = Flag{Type: String, Name: flagkey.FnBuildCmd, Usage: "Package build command for builder to run with"}
	FnSecretFlag                = Flag{Type: StringSlice, Name: flagkey.FnSecret, Usage: "Function access to secret, should be present in the same namespace as the function. You can provide multiple secrets using multiple --secrets flags. In the case of fn update the the secrets will be replaced by the provided list of secrets."}
	FnCfgMapFlag                = Flag{Type: StringSlice, Name: flagkey.FnCfgMap, Usage: "Function access to configmap, should be present in the same namespace as the function. You can provide multiple configmaps using multiple --configmap flags. In case of fn update the configmaps will be replaced by the provided list of configmaps."}
	FnExecutorTypeFlag          = Flag{Type: String, Name: flagkey.FnExecutorType, Usage: "Executor type for execution; one of 'poolmgr', 'newdeploy'", DefaultValue: types.ExecutorTypePoolmgr}
	FnExecutionTimeoutFlag      = Flag{Type: Int, Name: flagkey.FnExecutionTimeout, Aliases: []string{"ft"}, Usage: "Time duration to wait for the response while executing the function", DefaultValue: 60}
	FnLogPodFlag                = Flag{Type: String, Name: flagkey.FnLogPod, Usage: "Function pod name (use the latest pod name if unspecified)"}
	FnLogFollowFlag             = Flag{Type: Bool, Name: flagkey.FnLogFollow, Short: "f", Usage: "Specify if the logs should be streamed"}
	FnLogDetailFlag             = Flag{Type: Bool, Name: flagkey.FnLogDetail, Short: "d", Usage: "Display detailed information"}
	FnLogDBTypeFlag             = Flag{Type: String, Name: flagkey.FnLogDBType, Usage: "Log database type, e.g. influxdb (currently only influxdb is supported)", DefaultValue: "influxdb"}
	FnLogReverseQueryFlag       = Flag{Type: Bool, Name: flagkey.FnLogReverseQuery, Short: "r", Usage: "Specify the log reverse query base on time, it will be invalid if the 'follow' flag is specified"}
	FnLogCountFlag              = Flag{Type: Int, Name: flagkey.FnLogCount, Usage: "Get N most recent log records", DefaultValue: 20}
	FnTestBodyFlag              = Flag{Type: String, Name: flagkey.FnTestBody, Short: "b", Usage: "Request body"}
	FnTestTimeoutFlag           = Flag{Type: Duration, Name: flagkey.FnTestTimeout, Short: "t", Usage: "Length of time to wait for the response. If set to zero or negative number, no timeout is set", DefaultValue: 30 * time.Second}
	FnTestHeaderFlag            = Flag{Type: StringSlice, Name: flagkey.FnTestHeader, Short: "H", Usage: "Request headers"}
	FnTestQueryFlag             = Flag{Type: StringSlice, Name: flagkey.FnTestQuery, Short: "q", Usage: "Request query parameters: -q key1=value1 -q key2=value2"}

	HtNameFlag              = Flag{Type: String, Name: flagkey.HtName, Usage: "HTTP trigger name"}
	HtMethodFlag            = Flag{Type: String, Name: flagkey.HtMethod, Usage: "HTTP Method: GET|POST|PUT|DELETE|HEAD", DefaultValue: http.MethodGet}
	HtUrlFlag               = Flag{Type: String, Name: flagkey.HtUrl, Usage: "URL pattern (See gorilla/mux supported patterns)"}
	HtHostFlag              = Flag{Type: String, Name: flagkey.HtHost, Usage: "Use --ingressrule instead", Deprecated: true, Substitute: flagkey.HtIngressRule}
	HtIngressFlag           = Flag{Type: Bool, Name: flagkey.HtIngress, Usage: "Creates ingress with same URL"}
	HtIngressRuleFlag       = Flag{Type: String, Name: flagkey.HtIngressRule, Usage: "Host for Ingress rule: --ingressrule host=path (the format of host/path depends on what ingress controller you used)"}
	HtIngressAnnotationFlag = Flag{Type: StringSlice, Name: flagkey.HtIngressAnnotation, Usage: "Annotation for Ingress: --ingressannotation key=value (the format of annotation depends on what ingress controller you used)"}
	HtIngressTLSFlag        = Flag{Type: String, Name: flagkey.HtIngressTLS, Usage: "Name of the Secret contains TLS key and crt for Ingress (the usability of TLS features depends on what ingress controller you used)"}
	HtFnNameFlag            = Flag{Type: StringSlice, Name: flagkey.HtFnName, Usage: "Name(s) of the function for this trigger. (If 2 functions are supplied with this flag, traffic gets routed to them based on weights supplied with --weight flag.)"}
	HtFnWeightFlag          = Flag{Type: IntSlice, Name: flagkey.HtFnWeight, Usage: "Weight for each function supplied with --function flag, in the same order. Used for canary deployment"}
	HtFnFilterFlag          = Flag{Type: String, Name: flagkey.HtFilter, Usage: "Name of the function for trigger(s)"}

	TtNameFlag   = Flag{Type: String, Name: flagkey.TtName, Usage: "Time Trigger name"}
	TtCronFlag   = Flag{Type: String, Name: flagkey.TtCron, Usage: "Time trigger cron spec with each asterisk representing respectively second, minute, hour, the day of the month, month and day of the week. Also supports readable formats like '@every 5m', '@hourly'"}
	TtFnNameFlag = Flag{Type: String, Name: flagkey.TtFnName, Usage: "Function name"}
	TtRoundFlag  = Flag{Type: Int, Name: flagkey.TtRound, Usage: "Get next N rounds of invocation time", DefaultValue: 1}

	MqtNameFlag           = Flag{Type: String, Name: flagkey.MqtName, Usage: "Message queue trigger name"}
	MqtFnNameFlag         = Flag{Type: String, Name: flagkey.MqtFnName, Usage: "Function name"}
	MqtMQTypeFlag         = Flag{Type: String, Name: flagkey.MqtMQType, Usage: "Message queue type, e.g. nats-streaming, azure-storage-queue", DefaultValue: "nats-streaming"}
	MqtTopicFlag          = Flag{Type: String, Name: flagkey.MqtTopic, Usage: "Message queue Topic the trigger listens on"}
	MqtRespTopicFlag      = Flag{Type: String, Name: flagkey.MqtRespTopic, Usage: "Topic that the function response is sent on (response discarded if unspecified)"}
	MqtErrorTopicFlag     = Flag{Type: String, Name: flagkey.MqtErrorTopic, Usage: "Topic that the function error messages are sent to (errors discarded if unspecified"}
	MqtMaxRetriesFlag     = Flag{Type: Int, Name: flagkey.MqtMaxRetries, Usage: "Maximum number of times the function will be retried upon failure", DefaultValue: 0}
	MqtMsgContentTypeFlag = Flag{Type: String, Name: flagkey.MqtMsgContentType, Short: "c", Usage: "Content type of messages that publish to the topic", DefaultValue: "application/json"}

	RecorderNameFlag            = Flag{Type: String, Name: flagkey.RecorderName, Usage: "Recorder name"}
	RecorderFnFlag              = Flag{Type: String, Name: flagkey.RecorderFn, Usage: "Record Function name(s): --function=fnA"}
	RecorderTriggersFlag        = Flag{Type: StringSlice, Name: flagkey.RecorderTriggers, Usage: "Record Trigger name(s): --trigger=trigger1,trigger2,trigger3"}
	RecorderRetentionPolicyFlag = Flag{Type: String, Name: flagkey.RecorderRetentionPolicy, Usage: "Retention policy (number of days)"}
	RecorderEvictionPolicyFlag  = Flag{Type: String, Name: flagkey.RecorderEvictionPolcy, Usage: "Eviction policy (default LRU)"}
	RecorderEnabledFlag         = Flag{Type: Bool, Name: flagkey.RecorderEnabled, Usage: "Enable recorder"}
	RecorderDisabledFlag        = Flag{Type: Bool, Name: flagkey.RecorderDisabled, Usage: "Disable recorder"}

	RecordsFilterTimeFromFlag = Flag{Type: String, Name: flagkey.RecordsFilterTimeFrom, Usage: "Filter records by time interval; specify start of interval"}
	RecordsFilterTimeToFlag   = Flag{Type: String, Name: flagkey.RecordsFilterTimeTo, Usage: "Filter records by time interval; specify end of interval"}
	RecordsFilterFunctionFlag = Flag{Type: String, Name: flagkey.RecordsFilterFunction, Usage: "Filter records by function"}
	RecordsFilterTriggerFlag  = Flag{Type: String, Name: flagkey.RecordsFilterTrigger, Usage: "Filter records by trigger"}
	RecordsVerbosityFlag      = Flag{Type: Bool, Name: flagkey.RecordsVerbosity, Usage: "Toggle verbosity -- view more detailed requests/responses"}
	RecordsVvFlag             = Flag{Type: Bool, Name: flagkey.RecordsVv, Usage: "Toggle verbosity -- view raw requests/responses"}
	RecordsReqIDFlag          = Flag{Type: String, Name: flagkey.RecordsReqID, Usage: "Replay a particular request by providing the reqUID (to view reqUIDs, do 'fission records view')"}

	EnvNameFlag                   = Flag{Type: String, Name: flagkey.EnvName, Usage: "Environment name"}
	EnvPoolsizeFlag               = Flag{Type: Int, Name: flagkey.EnvPoolsize, Usage: "Size of the pool", DefaultValue: 3}
	EnvImageFlag                  = Flag{Type: String, Name: flagkey.EnvImage, Usage: "Environment image URL"}
	EnvBuilderImageFlag           = Flag{Type: String, Name: flagkey.EnvBuilderImage, Usage: "Environment builder image URL"}
	EnvBuildCmdFlag               = Flag{Type: String, Name: flagkey.EnvBuildcommand, Usage: "Build command for environment builder to build source package"}
	EnvKeepArchiveFlag            = Flag{Type: Bool, Name: flagkey.EnvKeeparchive, Usage: "Keep the archive instead of extracting it into a directory"}
	EnvExternalNetworkFlag        = Flag{Type: Bool, Name: flagkey.EnvExternalNetwork, Usage: "Allow environment access external network when istio feature enabled"}
	EnvTerminationGracePeriodFlag = Flag{Type: Int, Name: flagkey.EnvGracePeriod, Aliases: []string{"period"}, Usage: "Grace time (in seconds) for pod to perform connection draining before termination", DefaultValue: 360}
	EnvVersionFlag                = Flag{Type: Int, Name: flagkey.EnvVersion, Usage: "Environment API version (1 means v1 interface)", DefaultValue: 1}

	KwNameFlag      = Flag{Type: String, Name: flagkey.KwName, Usage: "Watch name"}
	KwFnNameFlag    = Flag{Type: String, Name: flagkey.KwFnName, Usage: "Function name"}
	KwNamespaceFlag = Flag{Type: String, Name: flagkey.KwNamespace, Usage: "Namespace of resource to watch"}
	KwObjTypeFlag   = Flag{Type: String, Name: flagkey.KwObjType, Usage: "Type of resource to watch (Pod, Service, etc.)"}
	KwLabelsFlag    = Flag{Type: String, Name: flagkey.KwLabels, Usage: "Label selector of the form a=b,c=d"}

	PkgNameFlag          = Flag{Type: String, Name: flagkey.PkgName, Usage: "Package name"}
	PkgForceFlag         = Flag{Type: Bool, Name: flagkey.PkgForce, Short: "f", Usage: "Force update a package even if it is used by one or more functions"}
	PkgEnvironmentFlag   = Flag{Type: String, Name: flagkey.PkgEnvironment, Usage: "Environment name"}
	PkgKeepURLFlag       = Flag{Type: Bool, Name: flagkey.PkgKeepURL, Aliases: []string{"keepurl"}, Usage: "Keep the providing URL in archive instead of downloading file from it. (If set, no checksum will be generated for file integrity check. You must ensure the file won't be changed.)"}
	PkgBuildCmdFlag      = Flag{Type: String, Name: flagkey.PkgBuildCmd, Usage: "Build command for builder to run with"}
	PkgOutputFlag        = Flag{Type: String, Name: flagkey.PkgOutput, Short: "o", Usage: "Output filename to save archive content"}
	PkgStatusFlag        = Flag{Type: String, Name: flagkey.PkgStatus, Usage: `Filter packages by status`}
	PkgOrphanFlag        = Flag{Type: Bool, Name: flagkey.PkgOrphan, Usage: "Orphan packages that are not referenced by any function"}
	PkgDeployArchiveFlag = Flag{Type: StringSlice, Name: flagkey.PkgDeployArchive, Aliases: []string{"deploy"}, Usage: "Local path or URL for binary archive"}
	PkgSrcArchiveFlag    = Flag{Type: StringSlice, Name: flagkey.PkgSrcArchive, Aliases: []string{"source", "src"}, Usage: "Local path or URL for source archive"}

	SpecSaveFlag     = Flag{Type: Bool, Name: flagkey.SpecSave, Usage: "Save to the spec directory instead of creating on cluster"}
	SpecDirFlag      = Flag{Type: String, Name: flagkey.SpecDir, Usage: "Directory to store specs, defaults to ./specs"}
	SpecNameFlag     = Flag{Type: String, Name: flagkey.SpecName, Usage: "Name for the app, applied to resources as a Kubernetes annotation"}
	SpecDeployIDFlag = Flag{Type: String, Name: flagkey.SpecDeployID, Aliases: []string{"id"}, Usage: "Deployment ID for the spec deployment config"}
	SpecWaitFlag     = Flag{Type: Bool, Name: flagkey.SpecWait, Usage: "Wait for package builds"}
	SpecWatchFlag    = Flag{Type: Bool, Name: flagkey.SpecWatch, Usage: "Watch local files for change, and re-apply specs as necessary"}
	SpecDeleteFlag   = Flag{Type: Bool, Name: flagkey.SpecDelete, Usage: "Allow apply to delete resources that no longer exist in the specification"}

	SupportOutputFlag = Flag{Type: String, Name: flagkey.SupportOutput, Short: "o", Usage: "Output directory to save dump archive/files", DefaultValue: flagkey.DefaultSpecOutputDir}
	SupportNoZipFlag  = Flag{Type: Bool, Name: flagkey.SupportNoZip, Usage: "Save dump information into multiple files instead of single zip file"}

	CanaryNameFlag              = Flag{Type: String, Name: flagkey.CanaryName, Usage: "Name for the canary config"}
	CanaryTriggerNameFlag       = Flag{Type: String, Name: flagkey.CanaryHTTPTriggerName, Usage: "Http trigger that this config references"}
	CanaryNewFuncFlag           = Flag{Type: String, Name: flagkey.CanaryNewFunc, Aliases: []string{"newfn"}, Usage: "New version of the function"}
	CanaryOldFuncFlag           = Flag{Type: String, Name: flagkey.CanaryOldFunc, Aliases: []string{"oldfn"}, Usage: "Old stable version of the function"}
	CanaryWeightIncrementFlag   = Flag{Type: Int, Name: flagkey.CanaryWeightIncrement, Aliases: []string{"step"}, Usage: "Weight increment step for function", DefaultValue: 20}
	CanaryIncrementIntervalFlag = Flag{Type: String, Name: flagkey.CanaryIncrementInterval, Aliases: []string{"internal"}, Usage: "Weight increment interval, string representation of time.Duration, ex : 1m, 2h, 2d", DefaultValue: "2m"}
	CanaryFailureThresholdFlag  = Flag{Type: Int, Name: flagkey.CanaryFailureThreshold, Aliases: []string{"threshold"}, Usage: "Threshold in percentage beyond which the new version of the function is considered unstable", DefaultValue: 10}
)

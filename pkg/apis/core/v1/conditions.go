/*
Copyright 2026 The Fission Authors.

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

package v1

// Standard condition Type values populated on each CRD's Status.Conditions
// by Fission controllers. Names follow the Kubernetes convention of being
// CamelCase and namespaced per CRD.
const (
	// Function conditions
	FunctionConditionReady        = "Ready"
	FunctionConditionPackageReady = "PackageReady"

	// Package conditions
	PackageConditionBuildSucceeded = "BuildSucceeded"
	PackageConditionReady          = "Ready"

	// HTTPTrigger conditions
	HTTPTriggerConditionRouteAdmitted = "RouteAdmitted"
	HTTPTriggerConditionReady         = "Ready"

	// KubernetesWatchTrigger conditions
	KubernetesWatchTriggerConditionSubscribed = "Subscribed"
	KubernetesWatchTriggerConditionReady      = "Ready"

	// TimeTrigger conditions
	TimeTriggerConditionScheduled = "Scheduled"
	TimeTriggerConditionReady     = "Ready"

	// MessageQueueTrigger conditions
	MessageQueueTriggerConditionBindingReady = "BindingReady"
	MessageQueueTriggerConditionReady        = "Ready"

	// CanaryConfig conditions
	CanaryConfigConditionProgressing = "Progressing"
	CanaryConfigConditionReady       = "Ready"

	// Environment conditions
	EnvironmentConditionReady = "Ready"
)

// Standard Reason values written alongside each condition. PascalCase per
// Kubernetes convention. Keeping them in the api package gives controllers
// a single import path to grep when introducing new conditions and
// guarantees we never drift on spelling across writers.
const (
	// Function condition reasons
	FunctionReasonSpecialized       = "Specialized"
	FunctionReasonChoosePodFailed   = "ChoosePodFailed"
	FunctionReasonSpecializeFailed  = "SpecializationFailed"
	FunctionReasonDeploymentReady   = "DeploymentAvailable"
	FunctionReasonDeploymentFailed  = "DeploymentNotReady"
	FunctionReasonServiceFailed     = "ServiceCreateFailed"
	FunctionReasonHPAFailed         = "HPACreateFailed"
	FunctionReasonFuncSvcCacheError = "FuncSvcCacheError"
	FunctionReasonPackageReady      = "PackageReady"
	FunctionReasonPackageFailed     = "PackageBuildFailed"

	// Package condition reasons (mirror BuildStatus enum + composites)
	PackageReasonBuildSucceeded  = "BuildSucceeded"
	PackageReasonBuildFailed     = "BuildFailed"
	PackageReasonBuildPending    = "BuildPending"
	PackageReasonBuildRunning    = "BuildRunning"
	PackageReasonNoBuildRequired = "NoBuildRequired"
	PackageReasonUnknown         = "Unknown"

	// Environment condition reasons
	EnvironmentReasonBuilderReady      = "BuilderReady"
	EnvironmentReasonBuilderCreateFail = "BuilderCreateFailed"
	EnvironmentReasonNoBuilderRequired = "NoBuilderRequired"
	EnvironmentReasonPoolReady         = "PoolReady"

	// HTTPTrigger condition reasons
	HTTPTriggerReasonRouteAdmitted = "RouteAdmitted"
	HTTPTriggerReasonMuxBuildFail  = "MuxBuildFailed"

	// KubernetesWatchTrigger condition reasons
	KubernetesWatchTriggerReasonSubscribed  = "Subscribed"
	KubernetesWatchTriggerReasonStartFailed = "WatchStartFailed"

	// TimeTrigger condition reasons
	TimeTriggerReasonCronRegistered = "CronRegistered"

	// MessageQueueTrigger condition reasons
	MessageQueueTriggerReasonSubscribed = "Subscribed"

	// CanaryConfig condition reasons (mirror Status string enum)
	CanaryConfigReasonInProgress = "InProgress"
	CanaryConfigReasonSucceeded  = "Succeeded"
	CanaryConfigReasonFailed     = "Failed"
	CanaryConfigReasonAborted    = "Aborted"
	CanaryConfigReasonUnknown    = "Unknown"
)

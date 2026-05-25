// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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
	//
	// EnvironmentConditionReady is reserved for future use. This PR
	// deliberately leaves Environment.Status.Conditions empty because the
	// buildermgr composes the env's builder service hostname from
	// env.ResourceVersion (see pkg/buildermgr/common.go.buildPackage);
	// any status write would bump RV and break in-flight source-archive
	// builds. The constant is kept so a follow-up that decouples the
	// service name from RV can wire writers without an api churn.
	EnvironmentConditionReady = "Ready"
)

// Standard Reason values written alongside each condition. PascalCase per
// Kubernetes convention. Keeping them in the api package gives controllers
// a single import path to grep when introducing new conditions and
// guarantees we never drift on spelling across writers.
//
// Reasons are deliberately coarse-grained: the executor runs on the
// cold-start hot path and could otherwise flip Reason every few
// milliseconds. Detailed transient failure data lives in metrics and
// logs, not in condition history.
const (
	// Function condition reasons
	FunctionReasonReady         = "Available"          // executor: backend is serving requests
	FunctionReasonPackageReady  = "PackageReady"       // buildermgr: package built
	FunctionReasonPackageFailed = "PackageBuildFailed" // buildermgr: package build failed

	// Package condition reasons (mirror BuildStatus enum + composites)
	PackageReasonBuildSucceeded  = "BuildSucceeded"
	PackageReasonBuildFailed     = "BuildFailed"
	PackageReasonBuildPending    = "BuildPending"
	PackageReasonBuildRunning    = "BuildRunning"
	PackageReasonNoBuildRequired = "NoBuildRequired"
	PackageReasonUnknown         = "Unknown"

	// Environment condition reasons — no writer in this PR.
	// See pkg/buildermgr/envwatcher.go.AddUpdateBuilder for why.

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

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
	// FunctionConditionToolExposed reports whether the MCP server is advertising
	// this function as a tool (set by pkg/mcp's reconciler).
	FunctionConditionToolExposed = "ToolExposed"
	// FunctionConditionProvisioned reports whether the executor's provisioner
	// (RFC-0026) has reached the requested warm-pod floor for this function.
	// True = ProvisionedReady >= ProvisionedTarget; False with reason
	// ProvisionedWarming = still warming or draining; False with reason
	// ProvisionedDisabled = provisioned concurrency off (target=0 / spec nil).
	FunctionConditionProvisioned = "Provisioned"

	// Package conditions
	PackageConditionBuildSucceeded = "BuildSucceeded"
	PackageConditionReady          = "Ready"
	// PackageConditionOCIPublished reports the RFC-0012 producer outcome:
	// True = the build was published as a digest-pinned OCI image; False
	// with reason OCIPublishDegraded = the push failed and the build fell
	// back to the storagesvc tarball.
	PackageConditionOCIPublished = "OCIPublished"

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

	// FissionTenant conditions (multi-namespace tenancy). The tenant-lifecycle
	// controller writes RBACProvisioned/ServiceAccountsReady/Ready; the auth-key
	// and dynamic-watch workstreams (Phases 4-5) write AuthKeyProvisioned and
	// WatchActive into the same slice. Ready is the rollup over its declared
	// prerequisites.
	FissionTenantConditionRBACProvisioned      = "RBACProvisioned"
	FissionTenantConditionServiceAccountsReady = "ServiceAccountsReady"
	FissionTenantConditionAuthKeyProvisioned   = "AuthKeyProvisioned"
	FissionTenantConditionWatchActive          = "WatchActive"
	FissionTenantConditionReady                = "Ready"

	// Workflow conditions (RFC-0022). The workflow head's Workflow reconciler
	// writes Validated (graph validation result, mirroring admission so GitOps
	// bypasses still surface); constants ship with the types in phase 1, the
	// writer lands with the head in phase 2.
	WorkflowConditionValidated = "Validated"

	// WorkflowRun conditions. Accepted reports that a running workflow
	// controller has picked the run up — CRDs install regardless of
	// workflows.enabled, so a run created with the head disabled must be
	// distinguishable from one that is merely queued.
	WorkflowRunConditionAccepted = "Accepted"

	// FunctionAlias conditions (RFC-0025). Resolved reports whether the
	// alias's spec target (Version or PackageDigest) currently resolves to a
	// FunctionVersion: True with reason FunctionAliasReasonResolved once
	// Status.ResolvedVersion is populated; False with reason
	// FunctionAliasReasonVersionNotFound (name-pinned target missing) or
	// FunctionAliasReasonDigestUnmatched (digest-pinned target not yet — or
	// no longer — recorded by any FunctionVersion) otherwise.
	FunctionAliasConditionResolved = "Resolved"
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
	FunctionReasonReady            = "Available"          // executor: backend is serving requests
	FunctionReasonPackageReady     = "PackageReady"       // buildermgr: package built
	FunctionReasonPackageFailed    = "PackageBuildFailed" // buildermgr: package build failed
	FunctionReasonToolExposed      = "ToolExposed"        // mcp: advertised as an MCP tool
	FunctionReasonToolNameConflict = "ToolNameConflict"   // mcp: tool name already used by another function
	// FunctionReasonToolAliasFallback: RFC-0025 alias-addressed Tool
	// (Spec.Tool.Alias set) whose alias has never resolved a target — the mcp
	// reconciler is serving a fallback entry built from THIS function's own
	// live Tool config (not the alias's snapshot) so the tool is not
	// invisible while the alias catches up. Distinct from
	// FunctionReasonToolExposed so `kubectl get function -o
	// jsonpath='{.status.conditions}'` can tell snapshot-serving from
	// fallback-serving without reading logs.
	FunctionReasonToolAliasFallback = "ToolAliasFallback"

	// Provisioned condition reasons (RFC-0026 provisioner).
	FunctionReasonProvisionedSatisfied = "ProvisionedSatisfied" // ProvisionedReady >= ProvisionedTarget
	FunctionReasonProvisionedWarming   = "ProvisionedWarming"   // ProvisionedReady < ProvisionedTarget (still warming or draining)
	FunctionReasonProvisionedDisabled  = "ProvisionedDisabled"  // provisioned concurrency off (target=0 / spec field nil)
	FunctionReasonProvisionedClamped   = "ProvisionedClamped"   // spec.Target exceeded the namespace cap; effective target was clamped

	// Package condition reasons (mirror BuildStatus enum + composites)
	PackageReasonBuildSucceeded  = "BuildSucceeded"
	PackageReasonBuildFailed     = "BuildFailed"
	PackageReasonBuildPending    = "BuildPending"
	PackageReasonBuildRunning    = "BuildRunning"
	PackageReasonNoBuildRequired = "NoBuildRequired"
	PackageReasonUnknown         = "Unknown"
	// OCI producer (RFC-0012) publish outcome reasons.
	PackageReasonOCIPublished       = "OCIPublished"
	PackageReasonOCIPublishDegraded = "OCIPublishDegraded" // push failed; the build fell back to the storagesvc tarball

	// Environment condition reasons — no writer in this PR.
	// See pkg/buildermgr/envwatcher.go.AddUpdateBuilder for why.

	// HTTPTrigger condition reasons
	HTTPTriggerReasonRouteAdmitted        = "RouteAdmitted"
	HTTPTriggerReasonMuxBuildFail         = "MuxBuildFailed"
	HTTPTriggerReasonInvalidCorsConfig    = "InvalidCorsConfig"    // CORS origin/max-age failed url.Parse/time.ParseDuration
	HTTPTriggerReasonInvalidIngressConfig = "InvalidIngressConfig" // ingress path/host failed POSIX-regex/DNS validation
	HTTPTriggerReasonFunctionNotFound     = "FunctionNotFound"     // the referenced function does not exist; the route is not served
	HTTPTriggerReasonRouteConflict        = "RouteConflict"        // another trigger registered the same route shape and wins by precedence; this one is shadowed
	HTTPTriggerReasonInvalidRouteTemplate = "InvalidRouteTemplate" // the path's gorilla template does not compile (capturing groups, unbalanced braces, ...)

	// KubernetesWatchTrigger condition reasons
	KubernetesWatchTriggerReasonSubscribed  = "Subscribed"
	KubernetesWatchTriggerReasonStartFailed = "WatchStartFailed"

	// TimeTrigger condition reasons
	TimeTriggerReasonCronRegistered = "CronRegistered"
	TimeTriggerReasonInvalidCron    = "InvalidCron" // cron failed the robfig/cron parser (CEL cannot express it)

	// MessageQueueTrigger condition reasons
	MessageQueueTriggerReasonSubscribed = "Subscribed"
	// MessageQueueTriggerReasonNotOwned: the head that held this trigger's
	// subscription tore it down because a spec change moved the trigger to a
	// different MQ type/kind — whether another head picks it up depends on that
	// head being deployed.
	MessageQueueTriggerReasonNotOwned = "NotOwned"

	// CanaryConfig condition reasons (mirror Status string enum)
	CanaryConfigReasonInProgress = "InProgress"
	CanaryConfigReasonSucceeded  = "Succeeded"
	CanaryConfigReasonFailed     = "Failed"
	CanaryConfigReasonAborted    = "Aborted"
	CanaryConfigReasonUnknown    = "Unknown"

	// Workflow condition reasons
	WorkflowReasonGraphValid   = "GraphValid"
	WorkflowReasonGraphInvalid = "GraphInvalid"

	// WorkflowRun condition reasons
	WorkflowRunReasonAccepted     = "AcceptedByController"
	WorkflowRunReasonNoController = "NoWorkflowController"

	// FunctionAlias condition reasons (RFC-0025)
	FunctionAliasReasonResolved        = "Resolved"
	FunctionAliasReasonVersionNotFound = "VersionNotFound"
	FunctionAliasReasonDigestUnmatched = "DigestUnmatched"
)

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	asv2 "k8s.io/api/autoscaling/v2"
	apiv1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	DefaultConcurrency    = 500
	DefaultRequestsPerPod = 1
)

// RouteProviderType selects how the router exposes an HTTPTrigger externally.
// It is the type of RouteConfig.Provider; the allowed values are the constants
// below (also enforced by the field's kubebuilder Enum marker).
type RouteProviderType string

const (
	// RouteProviderIngress creates a networking.k8s.io Ingress (deprecated).
	RouteProviderIngress RouteProviderType = "ingress"
	// RouteProviderGateway creates a gateway.networking.k8s.io HTTPRoute.
	RouteProviderGateway RouteProviderType = "gateway"
)

// Workflow state kinds (RFC-0022). The enum marker on WorkflowStateType must
// list exactly these values; both grow together as later phases add
// Parallel/Map/Wait.
const (
	WorkflowStateTask     WorkflowStateType = "Task"
	WorkflowStateChoice   WorkflowStateType = "Choice"
	WorkflowStateParallel WorkflowStateType = "Parallel"
	WorkflowStateMap      WorkflowStateType = "Map"
	WorkflowStateWait     WorkflowStateType = "Wait"
	WorkflowStateSucceed  WorkflowStateType = "Succeed"
	WorkflowStateFail     WorkflowStateType = "Fail"
)

// WorkflowRun lifecycle phases.
const (
	WorkflowRunPending   WorkflowRunPhase = "Pending"
	WorkflowRunRunning   WorkflowRunPhase = "Running"
	WorkflowRunSucceeded WorkflowRunPhase = "Succeeded"
	WorkflowRunFailed    WorkflowRunPhase = "Failed"
	WorkflowRunCancelled WorkflowRunPhase = "Cancelled"
	WorkflowRunTimedOut  WorkflowRunPhase = "TimedOut"
)

// Built-in workflow error classes: the wire contract Catch routes on. A
// function signals a typed error by returning non-2xx with a JSON body
// {"errorType": "<Name>", "cause": ...}; these built-ins classify everything
// else (4xx = permanent, 5xx/transport = retryable, attempt timeout).
const (
	WorkflowErrAll             = "Fission.All"
	WorkflowErrPermanentError  = "Fission.PermanentError"
	WorkflowErrFunctionError   = "Fission.FunctionError"
	WorkflowErrTimeout         = "Fission.Timeout"
	WorkflowErrInvalidPath     = "Fission.InvalidPath"
	WorkflowErrNoChoiceMatched = "Fission.NoChoiceMatched"
	// WorkflowErrFailed is the class a bare Fail state fails the run with.
	WorkflowErrFailed = "Fission.Failed"
	// WorkflowErrBranchFailed is the class a Parallel/Map state fails with
	// when a branch fails terminally (fail-fast); a Catch on the state may
	// route it.
	WorkflowErrBranchFailed = "Fission.BranchFailed"
)

// WorkflowBuiltinErrorTypes is the canonical list of the classes above —
// consumers (e.g. the webhook's typo warning) derive lookups from it rather
// than re-enumerating the constants and drifting.
var WorkflowBuiltinErrorTypes = []string{
	WorkflowErrAll,
	WorkflowErrPermanentError,
	WorkflowErrFunctionError,
	WorkflowErrTimeout,
	WorkflowErrInvalidPath,
	WorkflowErrNoChoiceMatched,
	WorkflowErrFailed,
	WorkflowErrBranchFailed,
}

//
// To add a Fission CRD type:
//   1. Create a "spec" type, for everything in the type except metadata
//   2. Create the type with metadata + the spec
//   3. Create a list type (for example see FunctionList and Function, below)
//   4. Add methods at the bottom of this file for satisfying Object and List interfaces
//   5. Add the type to configureClient in fission/crd/client.go
//   6. Add the type to EnsureFissionCRDs in fission/crd/crd.go
//   7. Add tests to fission/crd/crd_test.go
//   8. Add a CRUD Interface type (analogous to FunctionInterface in fission/crd/function.go)
//   9. Add a getter method for your interface type to FissionClient in fission/crd/client.go
//  10. Follow the instruction in README.md to regenerate CRD type deepcopy methods
//

type (

	// Package Think of these as function-level images.
	// +genclient
	// +kubebuilder:object:root=true
	// +kubebuilder:subresource:status
	// +kubebuilder:resource:singular="package",scope="Namespaced",shortName={pkg}
	Package struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata"`

		Spec PackageSpec `json:"spec"`

		// Status indicates the build status of package.
		//+optional
		Status PackageStatus `json:"status,omitempty"`
	}

	// PackageList is a list of Packages.
	// +kubebuilder:object:root=true
	PackageList struct {
		metav1.TypeMeta `json:",inline"`
		metav1.ListMeta `json:"metadata"`
		Items           []Package `json:"items"`
	}

	// Function is function runs within environment runtime with given package and secrets/configmaps.
	// +genclient
	// +kubebuilder:object:root=true
	// +kubebuilder:subresource:status
	// +kubebuilder:resource:singular="function",scope="Namespaced",shortName={fn}
	Function struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata"`
		Spec              FunctionSpec `json:"spec"`
		// +optional
		Status FunctionStatus `json:"status,omitempty"`
	}

	// FunctionList is a list of Functions.
	//+kubebuilder:object:root=true
	FunctionList struct {
		metav1.TypeMeta `json:",inline"`
		metav1.ListMeta `json:"metadata"`
		Items           []Function `json:"items"`
	}

	// Environment is environment for building and running user functions.
	// +genclient
	// +kubebuilder:object:root=true
	// +kubebuilder:subresource:status
	Environment struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata"`
		Spec              EnvironmentSpec `json:"spec"`
		// +optional
		Status EnvironmentStatus `json:"status,omitempty"`
	}

	// EnvironmentList is a list of Environments.
	//+kubebuilder:object:root=true
	EnvironmentList struct {
		metav1.TypeMeta `json:",inline"`
		metav1.ListMeta `json:"metadata"`
		Items           []Environment `json:"items"`
	}

	// HTTPTrigger is the trigger invokes user functions when receiving HTTP requests.
	// +genclient
	// +kubebuilder:object:root=true
	// +kubebuilder:subresource:status
	HTTPTrigger struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata"`
		Spec              HTTPTriggerSpec `json:"spec"`
		// +optional
		Status HTTPTriggerStatus `json:"status,omitempty"`
	}

	// HTTPTriggerList is a list of HTTPTriggers
	//+kubebuilder:object:root=true
	HTTPTriggerList struct {
		metav1.TypeMeta `json:",inline"`
		metav1.ListMeta `json:"metadata"`
		Items           []HTTPTrigger `json:"items"`
	}

	// KubernetesWatchTrigger watches kubernetes resource events and invokes functions.
	// +genclient
	// +kubebuilder:object:root=true
	// +kubebuilder:subresource:status
	KubernetesWatchTrigger struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata"`
		Spec              KubernetesWatchTriggerSpec `json:"spec"`
		// +optional
		Status KubernetesWatchTriggerStatus `json:"status,omitempty"`
	}

	// KubernetesWatchTriggerList is a list of KubernetesWatchTriggers
	// +kubebuilder:object:root=true
	KubernetesWatchTriggerList struct {
		metav1.TypeMeta `json:",inline"`
		metav1.ListMeta `json:"metadata"`
		Items           []KubernetesWatchTrigger `json:"items"`
	}

	// TimeTrigger invokes functions based on given cron schedule.
	// +genclient
	// +kubebuilder:object:root=true
	// +kubebuilder:subresource:status
	TimeTrigger struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata"`

		Spec TimeTriggerSpec `json:"spec"`
		// +optional
		Status TimeTriggerStatus `json:"status,omitempty"`
	}

	// TimeTriggerList is a list of TimeTriggers.
	// +kubebuilder:object:root=true
	TimeTriggerList struct {
		metav1.TypeMeta `json:",inline"`
		metav1.ListMeta `json:"metadata"`

		Items []TimeTrigger `json:"items"`
	}

	// MessageQueueTrigger invokes functions when messages arrive to certain topic that trigger subscribes to.
	// +genclient
	// +kubebuilder:object:root=true
	// +kubebuilder:subresource:status
	MessageQueueTrigger struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata"`

		Spec MessageQueueTriggerSpec `json:"spec"`
		// +optional
		Status MessageQueueTriggerStatus `json:"status,omitempty"`
	}

	// MessageQueueTriggerList is a list of MessageQueueTriggers.
	// +kubebuilder:object:root=true
	MessageQueueTriggerList struct {
		metav1.TypeMeta `json:",inline"`
		metav1.ListMeta `json:"metadata"`
		Items           []MessageQueueTrigger `json:"items"`
	}

	// CanaryConfig is for canary deployment of two functions.
	// +genclient
	// +kubebuilder:object:root=true
	// +kubebuilder:subresource:status
	CanaryConfig struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata"`
		Spec              CanaryConfigSpec `json:"spec"`
		// +optional
		Status CanaryConfigStatus `json:"status,omitempty"`
	}

	// CanaryConfigList is a list of CanaryConfigs.
	// +kubebuilder:object:root=true
	CanaryConfigList struct {
		metav1.TypeMeta `json:",inline"`
		metav1.ListMeta `json:"metadata"`

		Items []CanaryConfig `json:"items"`
	}

	// FissionTenant onboards a Kubernetes namespace for Fission. It is the
	// cluster-scoped source of truth the tenant-lifecycle controller reconciles
	// into the live resource-namespace set (and, in later phases, per-namespace
	// RBAC, service accounts, and auth keys). Setting the label
	// fission.io/enabled=true on a Namespace is sugar the controller
	// materializes into one of these. See docs/multiple-namespace/prd.md.
	// +genclient
	// +genclient:nonNamespaced
	// +kubebuilder:object:root=true
	// +kubebuilder:subresource:status
	// +kubebuilder:resource:scope="Cluster",shortName={ftenant}
	// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.namespace`
	// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
	// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
	FissionTenant struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata"`
		Spec              FissionTenantSpec `json:"spec"`
		// +optional
		Status FissionTenantStatus `json:"status,omitempty"`
	}

	// FissionTenantList is a list of FissionTenants.
	// +kubebuilder:object:root=true
	FissionTenantList struct {
		metav1.TypeMeta `json:",inline"`
		metav1.ListMeta `json:"metadata"`
		Items           []FissionTenant `json:"items"`
	}

	// FissionTenantSpec declares which namespace Fission manages and, optionally,
	// where that tenant's function and builder workloads run.
	FissionTenantSpec struct {
		// Namespace is the Kubernetes namespace this tenant onboards. It is the
		// immutable join key to the live Namespace.
		// +kubebuilder:validation:MinLength=1
		// +kubebuilder:validation:MaxLength=63
		// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
		// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.namespace is immutable"
		Namespace string `json:"namespace"`

		// FunctionNamespace, if set, is where this tenant's function pods and
		// Services run; empty means they run in spec.namespace. Generalizes the
		// deprecated cluster-global FISSION_FUNCTION_NAMESPACE to a per-tenant
		// mapping.
		// +optional
		// +kubebuilder:validation:MaxLength=63
		// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
		FunctionNamespace string `json:"functionNamespace,omitempty"`

		// BuilderNamespace, if set, is where this tenant's builder pods run;
		// empty means they run in spec.namespace.
		// +optional
		// +kubebuilder:validation:MaxLength=63
		// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
		BuilderNamespace string `json:"builderNamespace,omitempty"`
	}

	// FissionTenantStatus reports the controller's progress onboarding the tenant.
	FissionTenantStatus struct {
		// ObservedGeneration is the spec generation the controller last reconciled.
		// +optional
		ObservedGeneration int64 `json:"observedGeneration,omitempty"`

		// Conditions are the latest observations of the tenant's state:
		// RBACProvisioned, ServiceAccountsReady, AuthKeyProvisioned, WatchActive,
		// and the Ready rollup.
		// +optional
		// +patchMergeKey=type
		// +patchStrategy=merge
		// +listType=map
		// +listMapKey=type
		Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	}

	//
	// Workflows (RFC-0022)
	//

	// Workflow declares a durable state machine whose task states are Fission
	// functions (RFC-0022). The engine executes WorkflowRuns against a snapshot
	// of this spec embedded in the run's event stream; editing a Workflow never
	// changes in-flight runs.
	// +genclient
	// +kubebuilder:object:root=true
	// +kubebuilder:subresource:status
	// +kubebuilder:resource:scope="Namespaced",shortName={wf}
	// +kubebuilder:printcolumn:name="StartAt",type=string,JSONPath=`.spec.startAt`
	// +kubebuilder:printcolumn:name="Validated",type=string,JSONPath=`.status.conditions[?(@.type=="Validated")].status`
	// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
	Workflow struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata"`
		Spec              WorkflowSpec `json:"spec"`
		// +optional
		Status WorkflowStatus `json:"status,omitempty"`
	}

	// WorkflowList is a list of Workflows.
	// +kubebuilder:object:root=true
	WorkflowList struct {
		metav1.TypeMeta `json:",inline"`
		metav1.ListMeta `json:"metadata"`
		Items           []Workflow `json:"items"`
	}

	// WorkflowRun is one execution of a Workflow. Full step history lives in
	// the statestore EventLog stream for the run, never in etcd; status carries
	// a bounded tail for kubectl visibility.
	// +genclient
	// +kubebuilder:object:root=true
	// +kubebuilder:subresource:status
	// +kubebuilder:resource:scope="Namespaced",shortName={wfr}
	// +kubebuilder:printcolumn:name="Workflow",type=string,JSONPath=`.spec.workflowRef`
	// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
	// +kubebuilder:printcolumn:name="Started",type=date,JSONPath=`.status.startedAt`
	// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
	WorkflowRun struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata"`
		Spec              WorkflowRunSpec `json:"spec"`
		// +optional
		Status WorkflowRunStatus `json:"status,omitempty"`
	}

	// WorkflowRunList is a list of WorkflowRuns.
	// +kubebuilder:object:root=true
	WorkflowRunList struct {
		metav1.TypeMeta `json:",inline"`
		metav1.ListMeta `json:"metadata"`
		Items           []WorkflowRun `json:"items"`
	}

	// WorkflowStateType enumerates the state kinds the engine executes.
	// +kubebuilder:validation:Enum=Task;Choice;Parallel;Map;Wait;Succeed;Fail
	WorkflowStateType string

	// WorkflowSpec is a state machine: states are data, logic lives in
	// functions.
	WorkflowSpec struct {
		// StartAt names the state execution begins at.
		StartAt string `json:"startAt"`

		// States is the state machine graph, keyed by state name. The size
		// bound mirrors validation.MaxWorkflowStates and lets the apiserver's
		// CEL cost estimator bound rules on nested types.
		// +kubebuilder:validation:MinProperties=1
		// +kubebuilder:validation:MaxProperties=100
		States map[string]WorkflowState `json:"states"`

		// DefaultRetry applies to Task states that do not set their own Retry.
		// +optional
		DefaultRetry *RetryPolicy `json:"defaultRetry,omitempty"`

		// Timeout bounds a whole run; expiry fails it with errorType
		// Fission.Timeout. Defaults to 24h (a mis-authored graph or endlessly
		// caught-and-retried loop must not hold an active run forever).
		// +optional
		Timeout *metav1.Duration `json:"timeout,omitempty"`

		// HistoryRetention bounds stored history (count + age) per finished run.
		// +optional
		HistoryRetention *WorkflowRetentionPolicy `json:"historyRetention,omitempty"`
	}

	// WorkflowState is one state in the machine. Exactly the fields for its
	// Type may be set (enforced at admission).
	WorkflowState struct {
		Type WorkflowStateType `json:"type"`

		// Function is the Task state's target.
		// +optional
		Function *FunctionReference `json:"function,omitempty"`

		// Timeout bounds one attempt of a Task invocation.
		// +optional
		Timeout *metav1.Duration `json:"timeout,omitempty"`

		// Retry overrides the workflow's DefaultRetry for this Task.
		// +optional
		Retry *RetryPolicy `json:"retry,omitempty"`

		// Catch routes a failed Task (retries exhausted, or a permanent error)
		// to another state by matched errorType; first match wins.
		// +optional
		Catch []WorkflowCatchRoute `json:"catch,omitempty"`

		// Choices are the Choice state's ordered rules; first match wins.
		// +optional
		Choices []WorkflowChoiceRule `json:"choices,omitempty"`

		// Default names the state a Choice falls through to when no rule
		// matches; without it, no-match fails the run (Fission.NoChoiceMatched).
		// +optional
		Default string `json:"default,omitempty"`

		// Branches are the Parallel state's concurrent sub-machines (or the
		// Map state's single iterator template). Branch states cannot nest
		// further fan-out — enforced by the bounded WorkflowBranchState type.
		// +optional
		// +kubebuilder:validation:MaxItems=10
		Branches []WorkflowBranch `json:"branches,omitempty"`

		// ItemsPath selects the array a Map state iterates (one branch per
		// element, input = the element).
		// +optional
		ItemsPath string `json:"itemsPath,omitempty"`

		// Duration is how long a Wait state pauses the run — durably: the
		// delay is a statestore Queue message, so a controller restart never
		// loses it (robfig/cron-style absolute schedules stay with the timer
		// subsystem; only durations here).
		// +optional
		Duration *metav1.Duration `json:"duration,omitempty"`

		// MaxConcurrency throttles how many branches execute at once. Zero
		// means the engine default (10) — NOT unbounded: an unthrottled
		// large Map against poolmgr is a self-inflicted cold-start burst.
		// The default is applied by the engine, not the schema: a schema
		// default would stamp the field onto every state type.
		// +optional
		// +kubebuilder:validation:Minimum=0
		MaxConcurrency int32 `json:"maxConcurrency,omitempty"`

		// InputPath/ResultPath/OutputPath shape step I/O with JSONPath
		// (Step Functions semantics; dialect pinned in pkg/workflow/expr).
		// +optional
		InputPath string `json:"inputPath,omitempty"`
		// +optional
		ResultPath string `json:"resultPath,omitempty"`
		// +optional
		OutputPath string `json:"outputPath,omitempty"`

		// Next names the state to run after this one; exactly one of Next/End
		// is set on Task states (Succeed/Fail are implicitly terminal).
		// +optional
		Next string `json:"next,omitempty"`
		// +optional
		End bool `json:"end,omitempty"`
	}

	// WorkflowBranch is one concurrent sub-machine of a Parallel state (or
	// the iterator template of a Map state).
	WorkflowBranch struct {
		StartAt string `json:"startAt"`
		// MaxProperties=20 (vs 100 top-level) keeps the apiserver's CEL cost
		// estimate for doubly-nested rules under budget — the phase-1 lesson.
		// +kubebuilder:validation:MinProperties=1
		// +kubebuilder:validation:MaxProperties=20
		States map[string]WorkflowBranchState `json:"states"`
	}

	// WorkflowBranchState is WorkflowState minus the fan-out fields: nested
	// Parallel/Map is impossible BY TYPE, which is what keeps the CRD schema
	// non-recursive (controller-gen cannot render a self-referential type).
	WorkflowBranchState struct {
		Type WorkflowStateType `json:"type"`
		// +optional
		Function *FunctionReference `json:"function,omitempty"`
		// +optional
		Duration *metav1.Duration `json:"duration,omitempty"`
		// +optional
		Timeout *metav1.Duration `json:"timeout,omitempty"`
		// +optional
		Retry *RetryPolicy `json:"retry,omitempty"`
		// +optional
		Catch []WorkflowCatchRoute `json:"catch,omitempty"`
		// +optional
		Choices []WorkflowChoiceRule `json:"choices,omitempty"`
		// +optional
		Default string `json:"default,omitempty"`
		// +optional
		InputPath string `json:"inputPath,omitempty"`
		// +optional
		ResultPath string `json:"resultPath,omitempty"`
		// +optional
		OutputPath string `json:"outputPath,omitempty"`
		// +optional
		Next string `json:"next,omitempty"`
		// +optional
		End bool `json:"end,omitempty"`
	}

	// WorkflowCatchRoute routes a matched error class to a next state.
	WorkflowCatchRoute struct {
		// ErrorType matches a typed function error ({"errorType": ...} body),
		// a built-in class (Fission.PermanentError, Fission.FunctionError,
		// Fission.Timeout), or Fission.All (matches anything).
		ErrorType string `json:"errorType"`
		Next      string `json:"next"`
		// ResultPath, when set, merges the error object
		// ({"errorType": ..., "cause": ...}) into the flowing document at
		// this JSONPath, so the catch target still sees the business data
		// (e.g. retry a charge after a grace period). Unset keeps the
		// Step-Functions-parity default: the error object REPLACES the
		// document.
		// +optional
		ResultPath string `json:"resultPath,omitempty"`
	}

	// WorkflowChoiceCondition is a leaf comparison against the state input.
	// Exactly one operator must be set. Numeric values use resource.Quantity
	// (CRDs cannot carry floats; Quantity accepts YAML numbers and strings).
	WorkflowChoiceCondition struct {
		// Variable is a JSONPath into the state's (shaped) input. Required on
		// every leaf condition — enforced by the webhook, not the schema: this
		// struct is inline-embedded in WorkflowChoiceRule, and a
		// schema-required field would wrongly reject composite (and/or/not)
		// rules that carry no inline leaf.
		// +optional
		Variable string `json:"variable,omitempty"`
		// +optional
		StringEquals *string `json:"stringEquals,omitempty"`
		// +optional
		NumericEquals *resource.Quantity `json:"numericEquals,omitempty"`
		// +optional
		NumericGreaterThan *resource.Quantity `json:"numericGreaterThan,omitempty"`
		// +optional
		NumericLessThan *resource.Quantity `json:"numericLessThan,omitempty"`
		// +optional
		BooleanEquals *bool `json:"booleanEquals,omitempty"`
		// +optional
		IsPresent *bool `json:"isPresent,omitempty"`
		// +optional
		IsNull *bool `json:"isNull,omitempty"`
	}

	// WorkflowChoiceRule is one ordered rule of a Choice state: either a leaf
	// condition (inline) or exactly one of And/Or/Not over leaf conditions
	// (depth-1 composition; deeper nesting is additive later).
	WorkflowChoiceRule struct {
		WorkflowChoiceCondition `json:",inline"`

		// +optional
		And []WorkflowChoiceCondition `json:"and,omitempty"`
		// +optional
		Or []WorkflowChoiceCondition `json:"or,omitempty"`
		// +optional
		Not *WorkflowChoiceCondition `json:"not,omitempty"`

		// Next names the state to transition to when this rule matches.
		Next string `json:"next"`
	}

	// WorkflowRetentionPolicy bounds retained history for finished runs.
	WorkflowRetentionPolicy struct {
		// +optional
		MaxCount *int32 `json:"maxCount,omitempty"`
		// +optional
		MaxAge *metav1.Duration `json:"maxAge,omitempty"`
	}

	// WorkflowStatus describes the observed state of a Workflow.
	WorkflowStatus struct {
		// +optional
		ObservedGeneration int64 `json:"observedGeneration,omitempty"`

		// +optional
		// +patchMergeKey=type
		// +patchStrategy=merge
		// +listType=map
		// +listMapKey=type
		Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	}

	// WorkflowRunPhase is the run's coarse lifecycle phase.
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Cancelled;TimedOut
	WorkflowRunPhase string

	// WorkflowRunSpec identifies the Workflow to execute and the run's input.
	WorkflowRunSpec struct {
		// WorkflowRef names the Workflow (same namespace) this run executes.
		WorkflowRef string `json:"workflowRef"`

		// WorkflowGeneration records (for observability) which Workflow
		// generation this run executes. It is NOT the pinning mechanism: the
		// authoritative spec is the snapshot the engine embeds in the run's
		// event stream at RunStarted; a Workflow edit or deletion mid-run can
		// neither fork nor strand a run. Set by the CLI; 0 means unknown.
		// +optional
		WorkflowGeneration int64 `json:"workflowGeneration,omitempty"`

		// Input is the run's initial input document — ANY JSON value
		// (apiextensionsv1.JSON, not RawExtension: the RawExtension schema is
		// type=object and the apiserver would reject a bare string/array/
		// number). Webhook-capped at 256KiB (etcd objects cap at ~1.5MiB) —
		// pass larger inputs by reference.
		// +optional
		Input *apiextensionsv1.JSON `json:"input,omitempty"`
	}

	// WorkflowRunEventSummary is one bounded-tail history entry for kubectl
	// visibility; the full history lives in the statestore EventLog.
	WorkflowRunEventSummary struct {
		Seq  int64  `json:"seq"`
		Type string `json:"type"`
		// +optional
		State string `json:"state,omitempty"`
		// +optional
		Attempt int32       `json:"attempt,omitempty"`
		At      metav1.Time `json:"at"`
		// +optional
		Note string `json:"note,omitempty"`
	}

	// WorkflowRunStatus describes the observed state of a WorkflowRun.
	WorkflowRunStatus struct {
		// +optional
		Phase WorkflowRunPhase `json:"phase,omitempty"`

		// ActiveStates lists the state names currently executing.
		// +optional
		ActiveStates []string `json:"activeStates,omitempty"`

		// +optional
		StartedAt *metav1.Time `json:"startedAt,omitempty"`
		// +optional
		FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

		// Output holds the final output inline up to the step-I/O spill
		// threshold — ANY JSON value (see Input for why apiextensionsv1.JSON);
		// larger outputs spill to the statestore KV and OutputRef points
		// there (the CLI dereferences).
		// +optional
		Output *apiextensionsv1.JSON `json:"output,omitempty"`
		// +optional
		OutputRef string `json:"outputRef,omitempty"`

		// ErrorType and Cause carry the terminal failure classification so
		// kubectl answers "why did it fail" without the history endpoint.
		// Cause is bounded; the full detail lives in the run history.
		// +optional
		ErrorType string `json:"errorType,omitempty"`
		// +optional
		// +kubebuilder:validation:MaxLength=1024
		Cause string `json:"cause,omitempty"`

		// RecentEvents is a bounded (<=20) tail; full history is in the
		// EventLog.
		// +optional
		RecentEvents []WorkflowRunEventSummary `json:"recentEvents,omitempty"`

		// +optional
		ObservedGeneration int64 `json:"observedGeneration,omitempty"`

		// +optional
		// +patchMergeKey=type
		// +patchStrategy=merge
		// +listType=map
		// +listMapKey=type
		Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	}

	//
	// Function versions & aliases (RFC-0025)
	//

	// FunctionVersion is an immutable snapshot of a Function's spec at publish
	// time (RFC-0025). Versions are minted by the version-control loop (auto
	// mode) or `fission fn publish` (manual mode) and are never mutated after
	// creation — only garbage collected once unreferenced by any FunctionAlias
	// and beyond the retain floor. FunctionVersion carries no Status: its
	// content is fixed at creation, so there is nothing to reconcile.
	// +genclient
	// +kubebuilder:object:root=true
	// +kubebuilder:resource:scope="Namespaced",shortName={fnver}
	// +kubebuilder:printcolumn:name="Sequence",type=integer,JSONPath=`.spec.sequence`
	// +kubebuilder:printcolumn:name="PackageDigest",type=string,JSONPath=`.spec.packageDigest`
	// +kubebuilder:printcolumn:name="PublishedAt",type=date,JSONPath=`.spec.publishedAt`
	FunctionVersion struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata"`
		Spec              FunctionVersionSpec `json:"spec"`
	}

	// FunctionVersionList is a list of FunctionVersions.
	// +kubebuilder:object:root=true
	FunctionVersionList struct {
		metav1.TypeMeta `json:",inline"`
		metav1.ListMeta `json:"metadata"`
		Items           []FunctionVersion `json:"items"`
	}

	// FunctionAlias is a mutable, named pointer at one (or, during a weighted
	// rollout, two) FunctionVersion(s) of a Function (RFC-0025). Aliases are
	// what triggers reference in production; moving an alias is how a rollout
	// or rollback happens without touching the trigger.
	// +genclient
	// +kubebuilder:object:root=true
	// +kubebuilder:subresource:status
	// +kubebuilder:resource:scope="Namespaced",shortName={fnalias}
	// +kubebuilder:printcolumn:name="Function",type=string,JSONPath=`.spec.functionName`
	// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
	// +kubebuilder:printcolumn:name="Weight",type=integer,JSONPath=`.spec.weight`
	// +kubebuilder:printcolumn:name="ResolvedVersion",type=string,JSONPath=`.status.resolvedVersion`
	FunctionAlias struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata"`
		Spec              FunctionAliasSpec `json:"spec"`
		// +optional
		Status FunctionAliasStatus `json:"status,omitempty"`
	}

	// FunctionAliasList is a list of FunctionAliases.
	// +kubebuilder:object:root=true
	FunctionAliasList struct {
		metav1.TypeMeta `json:",inline"`
		metav1.ListMeta `json:"metadata"`
		Items           []FunctionAlias `json:"items"`
	}

	// VersioningMode selects when versions are minted.
	// +kubebuilder:validation:Enum=auto;manual
	VersioningMode string

	// VersioningConfig opts a Function into RFC-0025 immutable version
	// snapshots and named aliases.
	VersioningConfig struct {
		// Mode auto (default) mints a version on every runtime-affecting
		// update once the referenced package build succeeds; manual mints
		// only on explicit `fission fn publish`.
		// +kubebuilder:default:=auto
		// +optional
		Mode VersioningMode `json:"mode,omitempty"`
		// Retain bounds unaliased version history per function (GC floor 1).
		// Defaults to 10. Alias-referenced versions are never GC'd.
		// +kubebuilder:validation:Minimum=1
		// +optional
		Retain *int `json:"retain,omitempty"`
	}

	// FunctionVersionSpec is the immutable snapshot recorded by one publish.
	FunctionVersionSpec struct {
		// +kubebuilder:validation:MaxLength=63
		FunctionName string `json:"functionName"`
		// FunctionUID and FunctionGeneration pin the executor identity of this
		// snapshot: (UID, Generation) is the pool/cache key (crd.CacheKeyUG),
		// so a version is a generation pin, not a new identity.
		FunctionUID        types.UID `json:"functionUID"`
		FunctionGeneration int64     `json:"functionGeneration"`
		// +kubebuilder:validation:Minimum=1
		Sequence int64 `json:"sequence"`
		// Snapshot is the function spec at publish time with Versioning
		// zeroed (never nested) and, for legacy packages, PackageRef
		// repointed at the version-owned snapshot Package.
		Snapshot FunctionSpec `json:"snapshot"`
		// PackageDigest pins content: the OCI digest or sha256:<archive checksum>.
		PackageDigest string `json:"packageDigest"`
		// Environment observation at publish time (observational, not pinning).
		// +optional
		EnvObservedGeneration int64 `json:"envObservedGeneration,omitempty"`
		// +optional
		EnvRuntimeImage string      `json:"envRuntimeImage,omitempty"`
		PublishedAt     metav1.Time `json:"publishedAt"`
	}

	// Repo convention (types.go:755,778,955): guard BOTH absent and explicit-empty on optional strings.
	// +kubebuilder:validation:XValidation:rule="(has(self.version) && self.version != '') != (has(self.packageDigest) && self.packageDigest != '')",message="exactly one of version and packageDigest must be set"
	// +kubebuilder:validation:XValidation:rule="!has(self.weight) || (has(self.secondaryVersion) && self.secondaryVersion != '')",message="weight requires secondaryVersion"
	// +kubebuilder:validation:XValidation:rule="!has(self.secondaryVersion) || self.secondaryVersion == '' || !has(self.version) || self.secondaryVersion != self.version",message="secondaryVersion must differ from version"
	FunctionAliasSpec struct {
		// +kubebuilder:validation:MaxLength=63
		FunctionName string `json:"functionName"`
		// Version pins by FunctionVersion name (imperative path). XOR PackageDigest.
		// +optional
		Version string `json:"version,omitempty"`
		// PackageDigest pins declaratively (GitOps): resolved asynchronously to
		// the FunctionVersion that recorded this digest; eventually consistent.
		// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
		// +optional
		PackageDigest string `json:"packageDigest,omitempty"`
		// Weight (0-100) served by the primary target; nil = 100%.
		// +kubebuilder:validation:Minimum=0
		// +kubebuilder:validation:Maximum=100
		// +optional
		Weight *int `json:"weight,omitempty"`
		// SecondaryVersion receives 100-Weight. Name-pinned only.
		// +optional
		SecondaryVersion string `json:"secondaryVersion,omitempty"`
	}

	// AliasTargetRecord is one entry in FunctionAliasStatus.History: a
	// previously resolved target, kept for audit / rollback visibility.
	AliasTargetRecord struct {
		Version       string      `json:"version"`
		PackageDigest string      `json:"packageDigest,omitempty"`
		SwitchedAt    metav1.Time `json:"switchedAt"`
	}

	// FunctionAliasStatus describes the observed state of a FunctionAlias.
	FunctionAliasStatus struct {
		// ResolvedVersion is the FunctionVersion name this alias currently
		// resolves to (always name-pinned, even when Spec.PackageDigest
		// declares the target declaratively).
		// +optional
		ResolvedVersion string `json:"resolvedVersion,omitempty"`

		// History is a bounded tail of previously resolved targets, most
		// recent last.
		// +optional
		History []AliasTargetRecord `json:"history,omitempty"`

		// +optional
		// +patchMergeKey=type
		// +patchStrategy=merge
		// +listType=map
		// +listMapKey=type
		Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	}

	//
	// Functions and packages
	//

	// ChecksumType specifies the checksum algorithm, such as
	// sha256, used for a checksum.
	ChecksumType string

	// Checksum of package contents when the contents are stored
	// outside the Package struct. Type is the checksum algorithm;
	// "sha256" is the only currently supported one. Sum is hex
	// encoded.
	Checksum struct {
		Type ChecksumType `json:"type,omitempty"`
		Sum  string       `json:"sum,omitempty"`
	}

	// ArchiveType is literal, url, or oci, indicating whether the
	// package is specified in the Archive struct or externally.
	ArchiveType string

	// Archive contains or references a collection of sources or
	// binary files.
	// The CEL rule below deliberately never references self.literal: any
	// access to a byte-format field (even has()) makes the apiserver convert
	// its base64 value for CEL using URL-safe decoding, which rejects any
	// standard-base64 payload containing '/' or '+' — in practice every
	// zipped literal archive. The literal/oci combination is instead
	// rejected by the webhook (Archive.Validate), with the same message.
	// +kubebuilder:validation:XValidation:rule="!has(self.oci) || !has(self.url) || self.url == ''",message="at most one of literal, url, or oci may be set"
	Archive struct {
		// Type defines how the package is specified: literal, URL, or OCI.
		// Available value:
		//  - literal
		//  - url
		//  - oci
		// +optional
		// +kubebuilder:validation:Enum="";literal;url;oci
		Type ArchiveType `json:"type,omitempty"`

		// Literal contents of the package. Can be used for
		// encoding packages below TODO (256 KB?) size.
		// +optional
		Literal []byte `json:"literal,omitempty"`

		// URL references a package.
		// +optional
		URL string `json:"url,omitempty"`

		// Checksum ensures the integrity of packages
		// referenced by URL. Ignored for literals.
		// +optional
		// +kubebuilder:validation:XValidation:rule="((!has(self.type) || self.type == '') && (!has(self.sum) || self.sum == '')) || self.type == 'sha256'",message="checksum must be empty, or its type must be 'sha256' (the only supported checksum type)"
		Checksum Checksum `json:"checksum,omitempty"`

		// OCI references an OCI image holding the deployment code.
		// Mutually exclusive with Literal and URL. Supported only on
		// PackageSpec.Deployment; PackageSpec.Validate rejects it on Source
		// (source archives feed the builder, which has no OCI pull path).
		// +optional
		OCI *OCIArchive `json:"oci,omitempty"`
	}

	// OCIArchive references an OCI image whose flattened filesystem
	// contains the deployment code (RFC-0001). The environment runtime
	// image stays the pod's main container; only how the code reaches
	// the shared volume changes.
	OCIArchive struct {
		// Image is a fully qualified OCI reference: registry/repo:tag[@digest].
		// +kubebuilder:validation:MinLength=1
		Image string `json:"image"`

		// ImagePullSecrets are resolved when pulling the image. The
		// fetcher-pull path passes them to the in-fetcher keychain; the
		// image-volume path sets them on pod.Spec.ImagePullSecrets.
		// They must exist in the namespace the function pods run in —
		// the function's own namespace, or the configured function
		// namespace for default-namespace functions.
		// +optional
		ImagePullSecrets []apiv1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

		// SubPath points at the deployment root inside the image
		// filesystem, as a clean relative path; empty means the image
		// root. It must be a directory: the image-volume path mounts it
		// via the pod volumeMount subPath, and kubelets reject file
		// subpaths on image volumes.
		// +optional
		SubPath string `json:"subPath,omitempty"`

		// Digest is an optional content hash validated on pull.
		// +optional
		// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
		Digest string `json:"digest,omitempty"`
	}

	// EnvironmentReference is a reference to an environment. It is used by both
	// FunctionSpec.Environment and PackageSpec.Environment.
	EnvironmentReference struct {
		Namespace string `json:"namespace"`
		// Name of the referenced environment. Optional + omitempty: an unset
		// reference is omitted and its Pattern skipped (a container function has
		// no environment; a Package with an unset environment is admitted and
		// fails later with a clear builder error — the fission CLI still rejects
		// it). When set, it must be a DNS-1123 label.
		// +optional
		// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
		// +kubebuilder:validation:MaxLength=63
		Name string `json:"name,omitempty"`
	}

	// SecretReference is a reference to a kubernetes secret.
	SecretReference struct {
		Namespace string `json:"namespace"`
		// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
		// +kubebuilder:validation:MaxLength=63
		Name string `json:"name"`
	}

	// ConfigMapReference is a reference to a kubernetes configmap.
	ConfigMapReference struct {
		Namespace string `json:"namespace"`
		// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
		// +kubebuilder:validation:MaxLength=63
		Name string `json:"name"`
	}

	// BuildStatus indicates the current build status of a package.
	BuildStatus string

	// PackageSpec includes source/deploy archives and the reference of environment to build the package.
	PackageSpec struct {
		// Environment is a reference to the environment for building source archive.
		Environment EnvironmentReference `json:"environment"`

		// Source is the archive contains source code and dependencies file.
		// If the package status is in PENDING state, builder manager will then
		// notify builder to compile source and save the result as deployable archive.
		// +optional
		Source Archive `json:"source,omitempty"`

		// Deployment is the deployable archive that environment runtime used to run user function.
		// +optional
		Deployment Archive `json:"deployment,omitempty"`

		// BuildCommand is a custom build command that builder used to build the source archive.
		// +optional
		BuildCommand string `json:"buildcmd,omitempty"`

		// In the future, we can have a debug build here too
	}

	// PackageStatus contains the build status of a package also the build log for examination.
	PackageStatus struct {
		// TODO: Add another status field to indicate whether a package
		//   is ready for deploy instead of setting "none" in build status.

		// BuildStatus is the package build status.
		// +kubebuilder:default:="pending"
		// +kubebuilder:validation:Enum="";pending;running;succeeded;failed;none
		BuildStatus BuildStatus `json:"buildstatus,omitempty"`

		// BuildLog stores build log during the compilation.
		// +optional
		BuildLog string `json:"buildlog,omitempty"` // output of the build (errors etc)

		// LastUpdateTimestamp will store the timestamp the package was last updated
		// metav1.Time is a wrapper around time.Time which supports correct marshaling to YAML and JSON.
		// https://github.com/kubernetes/apimachinery/blob/44bd77c24ef93cd3a5eb6fef64e514025d10d44e/pkg/apis/meta/v1/time.go#L26-L35
		// +optional
		// +nullable
		LastUpdateTimestamp metav1.Time `json:"lastUpdateTimestamp,omitempty"`

		// Conditions represent the latest observations of the package's state.
		// +optional
		// +patchMergeKey=type
		// +patchStrategy=merge
		// +listType=map
		// +listMapKey=type
		Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	}

	// PackageRef is a reference to the package.
	PackageRef struct {
		// +optional
		Namespace string `json:"namespace"`
		// The package reference is optional, so Name is omitempty: when unset it
		// is omitted from the object and the Pattern below is skipped (a function
		// may legitimately have no package). A present name must be a DNS-1123
		// label. A leaf Pattern (cheap structural validation) is used rather than
		// a spec-level CEL matches() (which would exceed the cost budget).
		// +optional
		// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
		// +kubebuilder:validation:MaxLength=63
		Name string `json:"name,omitempty"`

		// Including resource version in the reference forces the function to be updated on
		// package update, making it possible to cache the function based on its metadata.
		ResourceVersion string `json:"resourceversion,omitempty"`
	}

	// FunctionPackageRef includes the reference to the package also the entrypoint of package.
	FunctionPackageRef struct {
		// Package reference
		// +optional
		PackageRef PackageRef `json:"packageref"`

		// FunctionName specifies a specific function within the package. This allows
		// functions to share packages, by having different functions within the same
		// package.
		//
		// Fission itself does not interpret this path. It is passed verbatim to
		// build and runtime environments.
		//
		// This is optional: if unspecified, the environment has a default name.
		FunctionName string `json:"functionName,omitempty"`
	}

	// ExecutorType is the primary executor for an environment
	ExecutorType string

	// StrategyType is the strategy to be used for function execution
	StrategyType string

	// FunctionSpec describes the contents of the function.
	// +kubebuilder:validation:XValidation:rule="!(has(self.InvokeStrategy.ExecutionStrategy) && self.InvokeStrategy.ExecutionStrategy.ExecutorType == 'container') || has(self.podspec)",message="executor type container requires a pod spec (spec.podspec)"
	// +kubebuilder:validation:XValidation:rule="!(has(self.InvokeStrategy.ExecutionStrategy) && (self.InvokeStrategy.ExecutionStrategy.ExecutorType == 'newdeploy' || self.InvokeStrategy.ExecutionStrategy.ExecutorType == 'container')) || !has(self.InvokeStrategy.ExecutionStrategy.MinScale) || self.InvokeStrategy.ExecutionStrategy.MinScale >= 0",message="minimum scale must be greater than or equal to 0 for newdeploy/container executors"
	// +kubebuilder:validation:XValidation:rule="!(has(self.InvokeStrategy.ExecutionStrategy) && (self.InvokeStrategy.ExecutionStrategy.ExecutorType == 'newdeploy' || self.InvokeStrategy.ExecutionStrategy.ExecutorType == 'container')) || (has(self.InvokeStrategy.ExecutionStrategy.MaxScale) && self.InvokeStrategy.ExecutionStrategy.MaxScale > 0)",message="maximum scale must be greater than 0 for newdeploy/container executors"
	// +kubebuilder:validation:XValidation:rule="!(has(self.InvokeStrategy.ExecutionStrategy) && (self.InvokeStrategy.ExecutionStrategy.ExecutorType == 'newdeploy' || self.InvokeStrategy.ExecutionStrategy.ExecutorType == 'container')) || !has(self.InvokeStrategy.ExecutionStrategy.MaxScale) || self.InvokeStrategy.ExecutionStrategy.MaxScale >= (has(self.InvokeStrategy.ExecutionStrategy.MinScale) ? self.InvokeStrategy.ExecutionStrategy.MinScale : 0)",message="maximum scale must be greater than or equal to minimum scale for newdeploy/container executors"
	// +kubebuilder:validation:XValidation:rule="!(has(self.InvokeStrategy.ExecutionStrategy) && (self.InvokeStrategy.ExecutionStrategy.ExecutorType == 'newdeploy' || self.InvokeStrategy.ExecutionStrategy.ExecutorType == 'container')) || !has(self.InvokeStrategy.ExecutionStrategy.TargetCPUPercent) || (self.InvokeStrategy.ExecutionStrategy.TargetCPUPercent >= 0 && self.InvokeStrategy.ExecutionStrategy.TargetCPUPercent <= 100)",message="TargetCPUPercent must be a value between 0 and 100 for newdeploy/container executors"
	// +kubebuilder:validation:XValidation:rule="!has(self.InvokeStrategy.StrategyType) || self.InvokeStrategy.StrategyType == '' || self.InvokeStrategy.StrategyType == 'execution'",message="InvokeStrategy.StrategyType must be 'execution'"
	// +kubebuilder:validation:XValidation:rule="!has(self.InvokeStrategy.ExecutionStrategy) || !has(self.InvokeStrategy.ExecutionStrategy.ExecutorType) || self.InvokeStrategy.ExecutionStrategy.ExecutorType == '' || self.InvokeStrategy.ExecutionStrategy.ExecutorType == 'poolmgr' || self.InvokeStrategy.ExecutionStrategy.ExecutorType == 'newdeploy' || self.InvokeStrategy.ExecutionStrategy.ExecutorType == 'container'",message="ExecutionStrategy.ExecutorType must be one of poolmgr, newdeploy, container"
	// Bounded podspec safety rules — CEL admission gate for the simple pod-level
	// invariants. Per-container SecurityContext checks stay in the webhook
	// (ValidatePodSpecSafety) because iterating containers exceeds the CEL cost
	// budget; the rules here cover only the bounded, cheap cases. The has()
	// guards on each scalar are required: PodSpec's bool/string fields are
	// json:"...,omitempty" so a zero/empty value is OMITTED from the object,
	// and CEL errors with "no such key" if the rule accesses an absent field.
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.hostNetwork) || !self.podspec.hostNetwork",message="spec.podspec.hostNetwork is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.hostPID) || !self.podspec.hostPID",message="spec.podspec.hostPID is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.hostIPC) || !self.podspec.hostIPC",message="spec.podspec.hostIPC is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.serviceAccountName) || self.podspec.serviceAccountName == ''",message="spec.podspec.serviceAccountName override is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.serviceAccount) || self.podspec.serviceAccount == ''",message="spec.podspec.serviceAccount override is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.provisionedConcurrency) || !has(self.InvokeStrategy.ExecutionStrategy) || !has(self.InvokeStrategy.ExecutionStrategy.ExecutorType) || self.InvokeStrategy.ExecutionStrategy.ExecutorType == '' || self.InvokeStrategy.ExecutionStrategy.ExecutorType == 'poolmgr'",message="provisionedConcurrency is only supported with executortype poolmgr"
	FunctionSpec struct {
		// Environment is the build and runtime environment that this function is
		// associated with. An Environment with this name should exist, otherwise the
		// function cannot be invoked.
		Environment EnvironmentReference `json:"environment"`

		// Reference to a package containing deployment and optionally the source.
		Package FunctionPackageRef `json:"package"`

		// Reference to a list of secrets.
		// +optional
		// +nullable
		// +listType=map
		// +listMapKey=name
		Secrets []SecretReference `json:"secrets,omitempty"`

		// Reference to a list of configmaps.
		// +optional
		// +nullable
		// +listType=map
		// +listMapKey=name
		ConfigMaps []ConfigMapReference `json:"configmaps,omitempty"`

		// cpu and memory resources as per K8S standards
		// This is only for newdeploy to set up resource limitation
		// when creating deployment for a function.
		// +optional
		Resources apiv1.ResourceRequirements `json:"resources"`

		// InvokeStrategy is a set of controls which affect how function executes
		InvokeStrategy InvokeStrategy `json:"InvokeStrategy"`

		// FunctionTimeout provides a maximum amount of duration within which a request for
		// a particular function execution should be complete.
		// This is optional. If not specified default value will be taken as 60s
		// +optional
		FunctionTimeout int `json:"functionTimeout,omitempty"`

		// IdleTimeout specifies the length of time that a function is idle before the
		// function pod(s) are eligible for deletion. If no traffic to the function
		// is detected within the idle timeout, the executor will then recycle the
		// function pod(s) to release resources.
		// +optional
		IdleTimeout *int `json:"idletimeout,omitempty"`

		// Streaming opts this function into the router's streaming invocation path:
		// incremental flushing, an idle/max timeout split, and a router-driven pod
		// keepalive for the connection's lifetime. When nil (the default) the function
		// uses the classic buffered, retry-on-transient-error proxy path with a single
		// FunctionTimeout deadline. Additive and backward compatible.
		// +optional
		Streaming *StreamingConfig `json:"streaming,omitempty"`

		// Tool, when non-nil, advertises this function as a Model Context Protocol
		// (MCP) tool on the fission-bundle --mcpPort server. The MCP server watches
		// Function CRDs and hot-updates its tool list from this field. Presence is
		// the on switch (like Streaming): nil (the default) means the function is
		// never advertised as a tool. Additive and backward compatible.
		// +optional
		Tool *ToolConfig `json:"tool,omitempty"`

		// State, when non-nil, opts this function into the RFC-0023 keyed-state
		// API: a scoped statesvc keyspace backed by the RFC-0021 statestore, with
		// a per-function token injected at specialization time. Presence is the
		// on switch (like Streaming and Tool): nil (the default) means exactly
		// today's behavior. Additive and backward compatible.
		// +optional
		State *StateConfig `json:"state,omitempty"`

		// Invocation, when non-nil, tunes RFC-0024 asynchronous invocation
		// (X-Fission-Invoke-Mode: async) for this function: the durable retry policy
		// and the maximum event age before an undelivered invocation is
		// dead-lettered. A function without it still accepts async mode with platform
		// defaults; this field only tunes them. Additive and backward compatible.
		// +optional
		Invocation *InvocationConfig `json:"invocation,omitempty"`

		// Maximum number of pods to be specialized which will serve requests
		// This is optional. If not specified default value will be taken as 500
		// +optional
		Concurrency int `json:"concurrency,omitempty"`

		// RequestsPerPod indicates the maximum number of concurrent requests that can be served by a specialized pod
		// This is optional. If not specified default value will be taken as 1
		// +optional
		RequestsPerPod int `json:"requestsPerPod,omitempty"`

		// OnceOnly specifies if specialized pod will serve exactly one request in its lifetime and would be garbage collected after serving that one request
		// This is optional. If not specified default value will be taken as false
		// +optional
		OnceOnly bool `json:"onceOnly,omitempty"`

		// RetainPods specifies the number of specialized pods that should be retained after serving requests
		// This is optional. If not specified default value will be taken as 0
		// +optional
		RetainPods int `json:"retainPods,omitempty"`

		// ProvisionedConcurrency, when non-nil, opts this function into eager
		// pre-warming of specialized pods (RFC-0026). The executor's provisioner
		// keeps at least the configured Target specialized pods warm, published to
		// the function's headless Service, and exempt from the idle reaper. nil
		// (the default) is the classic on-demand cold-start path. Additive and
		// backward compatible. Only valid when
		// InvokeStrategy.ExecutionStrategy.ExecutorType is poolmgr.
		// +optional
		ProvisionedConcurrency *ProvisionedConcurrencyConfig `json:"provisionedConcurrency,omitempty"`

		// Versioning, when non-nil, opts this function into RFC-0025 immutable
		// version snapshots and named aliases. Presence is the on switch (like
		// Streaming and Tool): nil (the default) means exactly today's mutable
		// in-place behavior. Additive and backward compatible.
		// +optional
		Versioning *VersioningConfig `json:"versioning,omitempty"`

		// Podspec specifies podspec to use for executor type container based functions
		// Different arguments mentioned for container based function are populated inside a pod.
		// +optional
		PodSpec *apiv1.PodSpec `json:"podspec,omitempty"`
	}

	// StreamingProtocol selects how the router treats the upstream response.
	// +kubebuilder:validation:Enum=auto;sse;chunked;websocket
	StreamingProtocol string

	// StreamingConfig controls the router's streaming behavior for a function.
	// Presence is the on switch: a non-nil Streaming enables the streaming path,
	// nil (the default) is the classic buffered path. There is no separate enabled
	// flag, so the in-memory zero value and the stored object never disagree.
	StreamingConfig struct {
		// Protocol hints how the router proxies the response.
		// +optional
		// +kubebuilder:default=auto
		Protocol StreamingProtocol `json:"protocol,omitempty"`

		// IdleTimeoutSeconds is the maximum time the router waits without bytes flowing
		// from the function before it aborts the stream; reset on every chunk. 0 means
		// use the package default (DefaultStreamIdleSeconds).
		// +optional
		// +kubebuilder:validation:Minimum=0
		IdleTimeoutSeconds int `json:"idleTimeoutSeconds,omitempty"`

		// MaxDurationSeconds is an optional hard ceiling on total stream lifetime
		// regardless of activity. 0 (the default) means no ceiling — the idle
		// timeout governs. A streaming function does NOT inherit FunctionTimeout as
		// a ceiling; that total-wall-clock cap is exactly what streaming escapes.
		// +optional
		// +kubebuilder:validation:Minimum=0
		MaxDurationSeconds int `json:"maxDurationSeconds,omitempty"`
	}

	// ToolConfig declares how a Function is exposed as an MCP (Model Context
	// Protocol) tool. The MCP server reuses the function's existing internal
	// invocation path; this struct only declares the agent-facing tool contract.
	// Presence of the enclosing FunctionSpec.Tool is the on switch — there is no
	// separate enabled flag, so the in-memory zero value and the stored object
	// never disagree (the same rationale as StreamingConfig).
	// +kubebuilder:validation:XValidation:rule="!(has(self.alias) && self.alias != '') || self.alias.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="tool.alias must be a valid DNS1123 label (lowercase alphanumeric or '-', start/end alphanumeric, max 63 chars)"
	ToolConfig struct {
		// Description is the human/agent-facing tool description surfaced in the MCP
		// tools/list response. Required.
		// +optional
		Description string `json:"description,omitempty"`

		// InputSchema is the JSON Schema (draft 2020-12) for the tool's arguments,
		// surfaced verbatim as the MCP tool inputSchema. Stored as raw JSON so the
		// CRD does not constrain the schema shape. When empty the tool advertises an
		// open object schema ({"type":"object"}).
		// +optional
		// +kubebuilder:pruning:PreserveUnknownFields
		InputSchema *apiextensionsv1.JSON `json:"inputSchema,omitempty"`

		// ToolName overrides the advertised tool name. Defaults to
		// "<namespace>-<function name>". Must match ^[a-zA-Z0-9_-]{1,64}$.
		// +optional
		// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_-]{1,64}$`
		ToolName string `json:"toolName,omitempty"`

		// Alias, when set, targets a FunctionAlias by name (RFC-0025) instead of
		// the live Function: the MCP registry serves the tool from the alias's
		// currently-resolved FunctionVersion snapshot, and tools/call is proxied
		// to the ":<alias>" route rather than straight to the live Function.
		// Empty (the default) preserves today's behavior. Router/registry-side
		// resolution lands in a later RFC-0025 task — until then this field is
		// accepted but inert.
		// +kubebuilder:validation:MaxLength=63
		// +optional
		Alias string `json:"alias,omitempty"`
	}

	// StateConfig declares a function's keyed-state keyspace and quotas
	// (RFC-0023). Presence of the enclosing FunctionSpec.State is the on switch —
	// there is no separate enabled flag, so the in-memory zero value and the
	// stored object never disagree (the same rationale as StreamingConfig).
	StateConfig struct {
		// Keyspace names the durable keyspace this function reads and writes.
		// Defaults to the function name; explicit so a function can be renamed
		// without orphaning its data. The charset deliberately excludes ':' and
		// '#' — ':' is a token-derivation info-string separator and '#' marks the
		// platform-reserved "<keyspace>#meta" quota-accounting sibling.
		// +optional
		// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
		// +kubebuilder:validation:MaxLength=63
		Keyspace string `json:"keyspace,omitempty"`

		// DefaultTTL, when set, is applied to writes that carry no explicit TTL.
		// Must be >= 0; zero (or nil) means keys do not expire by default.
		// +optional
		DefaultTTL *metav1.Duration `json:"defaultTTL,omitempty"`

		// MaxValueBytes caps a single value's size. 0 means the platform default
		// (DefaultStateMaxValueBytes, 256KiB). Blobs belong in object storage.
		// +optional
		// +kubebuilder:validation:Minimum=0
		MaxValueBytes int64 `json:"maxValueBytes,omitempty"`

		// MaxKeys caps the number of live keys in the keyspace, enforced
		// atomically with each write (quota.tla S3). 0 means the platform
		// default (DefaultStateMaxKeys).
		// +optional
		// +kubebuilder:validation:Minimum=0
		MaxKeys int64 `json:"maxKeys,omitempty"`

		// Backend selects a named statestore driver for this keyspace. Accepted
		// and validated in v1 but not yet acted on: statesvc serves every
		// keyspace from its single configured driver (per-function backend
		// selection is a documented deferral).
		// +optional
		Backend string `json:"backend,omitempty"`

		// Sticky, when non-nil, opts the function into sticky routing: the
		// router consistent-hashes the declared request key onto the ready-pod
		// set so one key's requests land on one pod while the pod set is stable.
		// Best-effort (an optimization, never a correctness dependency — S6):
		// durable truth stays behind the state API.
		// +optional
		Sticky *StickyConfig `json:"sticky,omitempty"`
	}

	// StickySource selects where the router extracts the sticky routing key
	// from an incoming request.
	// +kubebuilder:validation:Enum=header;queryparam
	StickySource string

	// StickyConfig declares how the sticky routing key is extracted from a
	// request. Requests missing the key fall back to the default endpoint pick.
	StickyConfig struct {
		// Source is where to look for the key.
		Source StickySource `json:"source"`

		// Name is the header or query-parameter name holding the key,
		// e.g. "X-Session-Id".
		Name string `json:"name"`
	}

	// InvocationConfig tunes RFC-0024 asynchronous invocation for a function.
	// Presence of the enclosing FunctionSpec.Invocation is optional — a function
	// without it still accepts async mode (X-Fission-Invoke-Mode: async) with
	// platform defaults; this struct only tunes them. Field bounds are validated in
	// Go (InvocationConfig.Validate, run at admission via validateForAdmission),
	// not CEL, because metav1.Duration CEL rules are unproven in this CRD.
	// An external dead-letter target is a later RFC-0024 phase.
	InvocationConfig struct {
		// Retry is the durable delivery retry policy. The zero value means platform
		// defaults (a bounded exponential backoff over DefaultMaxAttempts attempts).
		// +optional
		Retry RetryPolicy `json:"retry,omitempty"`

		// MaxAge caps how long an invocation may wait for successful delivery,
		// measured from its enqueue time; once exceeded it is dead-lettered with
		// reason "expired". nil means the platform default. Must be > 0 when set.
		// +optional
		MaxAge *metav1.Duration `json:"maxAge,omitempty"`

		// OnSuccess, when set, invokes a destination with a Lambda-shaped result
		// envelope after the invocation is delivered successfully (2xx).
		// +optional
		OnSuccess *DestinationRef `json:"onSuccess,omitempty"`

		// OnFailure, when set, invokes a destination with the result envelope after
		// the invocation permanently fails (a non-retryable 4xx, the retry budget
		// spent, or MaxAge exceeded).
		// +optional
		OnFailure *DestinationRef `json:"onFailure,omitempty"`
	}

	// DestinationRef routes an async invocation's result to exactly one target: a
	// Function (invoked async through the same machinery, depth-capped) or a Topic
	// (published to a message queue). Exactly one of Function/Topic must be set.
	// Topic destinations on the built-in statestore provider are supported
	// (RFC-0027); broker types are rejected by the webhook until the egress phase
	// lands.
	DestinationRef struct {
		// Function is a same-namespace function destination, invoked asynchronously
		// with the result envelope as its body (depth-capped to stop runaway chains).
		// +optional
		Function *FunctionReference `json:"function,omitempty"`

		// Topic publishes the result envelope to a message-queue topic.
		// +optional
		Topic *TopicRef `json:"topic,omitempty"`
	}

	// TopicRef is a message-queue topic destination for an async invocation result.
	// Topics are namespace-scoped: the destination publishes to the source
	// function's namespace (RFC-0024 rule R6).
	TopicRef struct {
		// MessageQueueType selects the provider: "statestore" (the RFC-0027
		// built-in, no broker) now; broker types (e.g. kafka) with the egress phase.
		MessageQueueType MessageQueueType `json:"messageQueueType"`

		// Topic is the topic the result envelope is published to. The schema bounds
		// mirror ValidateTopicName: a stream-safe charset excluding "/" so the
		// topic/<namespace>/<topic> mapping cannot alias across namespaces.
		// +kubebuilder:validation:MaxLength=249
		// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9._-]+$`
		Topic string `json:"topic"`
	}

	// RetryPolicy is the async delivery retry policy: the attempt budget and the
	// exponential-backoff schedule between delivery attempts. All fields are
	// optional; a nil field takes the platform default.
	RetryPolicy struct {
		// MaxAttempts is the total number of delivery attempts before the invocation
		// is dead-lettered. nil means DefaultMaxAttempts. Must be >= 1 when set.
		// +optional
		MaxAttempts *int `json:"maxAttempts,omitempty"`

		// BackoffBase is the delay before the first retry; it grows exponentially per
		// attempt up to BackoffCap. nil means the platform default. Must be >= 0.
		// +optional
		BackoffBase *metav1.Duration `json:"backoffBase,omitempty"`

		// BackoffCap bounds the per-retry backoff. nil means the platform default.
		// Must be >= 0 and >= BackoffBase when both are set.
		// +optional
		BackoffCap *metav1.Duration `json:"backoffCap,omitempty"`

		// Jitter, when non-nil and false, disables the randomized jitter the
		// dispatcher otherwise adds to each backoff to avoid synchronized retries.
		// nil means the platform default (jitter enabled).
		// +optional
		Jitter *bool `json:"jitter,omitempty"`
	}

	// ProvisionedConcurrencyConfig opts this function into eager pre-warming of
	// specialized pods (RFC-0026). Presence is the on switch: nil (the default)
	// means the function uses the classic on-demand cold-start path. When non-nil,
	// the executor's provisioner keeps at least Target specialized pods warm and
	// published to the function's headless Service, exempt from the idle reaper.
	// Additive and backward compatible.
	// +optional
	ProvisionedConcurrencyConfig struct {
		// Target is the base number of warm specialized pods to maintain outside
		// any schedule window. Must be >= 1. Schedule windows may override this
		// (see Windows). Bounded by the namespace cap
		// (executor.provisionedConcurrency.maxPerFunction, default 20).
		// +kubebuilder:validation:Minimum=1
		Target int `json:"target"`

		// Windows is an optional list of schedule windows that override Target
		// during specific time ranges (RFC-0026 PR 2). Empty in PR 1 — base Target
		// is always in effect. Each window: a cron start expression, a duration,
		// and a window-local target (0 means "un-warm" for the window's duration).
		// +optional
		// +listType=map
		// +listMapKey=name
		Windows []ProvisionedWindow `json:"windows,omitempty"`
	}

	// ProvisionedWindow describes a schedule window that overrides the base
	// ProvisionedConcurrencyConfig.Target during a time range. The window is
	// active from the cron-triggered start for Duration; while active, the
	// effective target is the window's Target (overlapping windows take the max).
	// +optional
	ProvisionedWindow struct {
		// Name identifies this window within the function's
		// ProvisionedConcurrencyConfig.Windows list. Must be unique within the
		// list (listMapKey=name).
		// +kubebuilder:validation:MinLength=1
		// +kubebuilder:validation:MaxLength=63
		Name string `json:"name"`

		// Start is a cron expression (5-field, robfig/cron, same parser as
		// TimeTrigger) marking when each window instance opens.
		// +kubebuilder:validation:MinLength=1
		Start string `json:"start"`

		// Duration is how long each window instance stays open. Format is Go
		// time.ParseDuration (e.g. "12h", "30m"). Must be > 0.
		// +kubebuilder:validation:Pattern=`^[0-9]+(ns|us|µs|ms|s|m|h)$`
		Duration string `json:"duration"`

		// Target is the effective target while the window is open. 0 means
		// "un-warm" — provisioned pods are drained for the window's duration.
		// +kubebuilder:validation:Minimum=0
		Target int `json:"target"`
	}

	// InvokeStrategy is a set of controls over how the function executes.
	// It affects the performance and resource usage of the function.
	//
	// An InvokeStrategy is of one of two types: ExecutionStrategy, which controls low-level
	// parameters such as which ExecutorType to use, when to autoscale, minimum and maximum
	// number of running instances, etc. A higher-level AbstractInvokeStrategy will also be
	// supported; this strategy would specify the target request rate of the function,
	// the target latency statistics, and the target cost (in terms of compute resources).
	InvokeStrategy struct {
		// ExecutionStrategy specifies low-level parameters for function execution,
		// such as the number of instances.
		// +optional
		ExecutionStrategy ExecutionStrategy `json:"ExecutionStrategy"`

		// StrategyType is the strategy type of function.
		// Now it only supports 'execution'.
		// +optional
		StrategyType StrategyType `json:"StrategyType"`
	}

	// ExecutionStrategy specifies low-level parameters for function execution,
	// such as the number of instances.
	//
	// MinScale affects the cold start behavior for a function. If MinScale is 0 then the
	// deployment is created on first invocation of function and is good for requests of
	// asynchronous nature. If MinScale is greater than 0 then MinScale number of pods are
	// created at the time of creation of function. This ensures faster response during first
	// invocation at the cost of consuming resources.
	//
	// MaxScale is the maximum number of pods that function will scale to based on TargetCPUPercent
	// and resources allocated to the function pod.
	ExecutionStrategy struct {
		// ExecutorType is the executor type of function used. Defaults to "poolmgr".
		//
		// Available value:
		//  - poolmgr
		//  - newdeploy
		//  - container
		// +optional
		ExecutorType ExecutorType `json:"ExecutorType"`

		// This is only for newdeploy to set up minimum replicas of deployment.
		// +optional
		MinScale int `json:"MinScale"`

		// This is only for newdeploy to set up maximum replicas of deployment.
		// +optional
		MaxScale int `json:"MaxScale"`

		// Deprecated: use hpaMetrics instead.
		// This is only for executor type newdeploy and container to set up target CPU utilization of HPA.
		// Applicable for executor type newdeploy and container.
		// +optional
		TargetCPUPercent int `json:"TargetCPUPercent"`

		// This is the timeout setting for executor to wait for pod specialization.
		// +optional
		SpecializationTimeout int `json:"SpecializationTimeout"`

		// hpaMetrics is the list of metrics used to determine the desired replica count of the Deployment
		// created for the function.
		// Applicable for executor type newdeploy and container.
		// +optional
		Metrics []asv2.MetricSpec `json:"hpaMetrics,omitempty"`

		// hpaBehavior is the behavior of HPA when scaling in up/down direction.
		// Applicable for executor type newdeploy and container.
		// +optional
		Behavior *asv2.HorizontalPodAutoscalerBehavior `json:"hpaBehavior,omitempty"`
	}

	// FunctionReferenceType refers to type of Function
	FunctionReferenceType string

	// FunctionReference refers to a function
	// +kubebuilder:validation:XValidation:rule="self.type != 'name' || (self.name.size() <= 63 && self.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$'))",message="functionref.name must be a valid DNS1123 label (lowercase alphanumeric or '-', start/end alphanumeric, max 63 chars) when type is 'name'"
	// +kubebuilder:validation:XValidation:rule="!((has(self.alias) && self.alias != '') && (has(self.version) && self.version != ''))",message="functionref.alias and functionref.version are mutually exclusive"
	// +kubebuilder:validation:XValidation:rule="!(has(self.alias) && self.alias != '') || self.type == 'name'",message="functionref.alias is only valid when type is 'name'"
	// +kubebuilder:validation:XValidation:rule="!(has(self.version) && self.version != '') || self.type == 'name'",message="functionref.version is only valid when type is 'name'"
	// +kubebuilder:validation:XValidation:rule="!(has(self.alias) && self.alias != '') || self.alias.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="functionref.alias must be a valid DNS1123 label (lowercase alphanumeric or '-', start/end alphanumeric, max 63 chars)"
	// +kubebuilder:validation:XValidation:rule="!(has(self.version) && self.version != '') || self.version.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="functionref.version must be a valid DNS1123 label (lowercase alphanumeric or '-', start/end alphanumeric, max 63 chars)"
	FunctionReference struct {
		// Type indicates whether this function reference is by name or selector. For now,
		// the only supported reference type is by "name".  Future reference types:
		//   * Function by label or annotation
		//   * Branch or tag of a versioned function
		//   * A "rolling upgrade" from one version of a function to another
		// Available value:
		// - name
		// - function-weights
		// +kubebuilder:validation:Enum=name;function-weights
		Type FunctionReferenceType `json:"type"`

		// Name of the function. Bounded to a DNS-1123 label length: the CEL
		// rule on this type needs the schema bound so the apiserver's cost
		// estimator can price the regex — without it, embedding the type
		// under a map (WorkflowSpec.States) blows the per-CRD cost budget.
		// +kubebuilder:validation:MaxLength=63
		Name string `json:"name"`

		// Alias, when set, targets a FunctionAlias by name instead of the live
		// Function directly (RFC-0025): the alias is a movable pointer that the
		// router resolves at request time to whatever FunctionVersion it
		// currently points at, so repointing the alias (e.g. for a canary
		// rollout or a rollback) redirects traffic without touching this
		// reference. Valid only when Type is "name"; mutually exclusive with
		// Version. Empty (the default) preserves today's behavior: route
		// straight to the live Function.
		// +kubebuilder:validation:MaxLength=63
		// +optional
		Alias string `json:"alias,omitempty"`

		// Version, when set, pins this reference to one FunctionVersion CR by
		// name (RFC-0025) — an immutable published snapshot that never moves,
		// unlike Alias. Valid only when Type is "name"; mutually exclusive
		// with Alias. Empty (the default) preserves today's behavior: route
		// straight to the live Function.
		// +kubebuilder:validation:MaxLength=63
		// +optional
		Version string `json:"version,omitempty"`

		// Function Reference by weight. this map contains function name as key and its weight
		// as the value. This is for canary upgrade purpose.
		// +nullable
		// +optional
		FunctionWeights map[string]int `json:"functionweights"`
	}

	//
	// Environments
	//

	// Runtime is the setting for environment runtime.
	// Bounded podspec / container safety rules — CEL admission gate for the
	// simple, bounded fields. Per-container PodSpec.containers iteration stays
	// in the webhook (ValidatePodSpecSafety / ValidateContainerSafety) because
	// it exceeds the CEL cost budget. The has() guards are required because
	// json:"...,omitempty" omits zero/empty values from the object.
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.hostNetwork) || !self.podspec.hostNetwork",message="spec.runtime.podspec.hostNetwork is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.hostPID) || !self.podspec.hostPID",message="spec.runtime.podspec.hostPID is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.hostIPC) || !self.podspec.hostIPC",message="spec.runtime.podspec.hostIPC is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.serviceAccountName) || self.podspec.serviceAccountName == ''",message="spec.runtime.podspec.serviceAccountName override is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.serviceAccount) || self.podspec.serviceAccount == ''",message="spec.runtime.podspec.serviceAccount override is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.container) || !has(self.container.securityContext) || !has(self.container.securityContext.privileged) || !self.container.securityContext.privileged",message="spec.runtime.container.securityContext.privileged=true is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.container) || !has(self.container.securityContext) || !has(self.container.securityContext.allowPrivilegeEscalation) || !self.container.securityContext.allowPrivilegeEscalation",message="spec.runtime.container.securityContext.allowPrivilegeEscalation=true is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.container) || !has(self.container.securityContext) || !has(self.container.securityContext.capabilities) || !has(self.container.securityContext.capabilities.add) || self.container.securityContext.capabilities.add.all(c, c == 'NET_BIND_SERVICE')",message="spec.runtime.container.securityContext.capabilities.add may only contain NET_BIND_SERVICE (PSA restricted)"
	Runtime struct {
		// Image for containing the language runtime.
		Image string `json:"image"`

		// NOT USED NOW
		// LoadEndpointPort defines the port on which the
		// server listens for function load
		// requests. Optional; default 8888.
		LoadEndpointPort int32 `json:"-"` // `json:"loadendpointport"`

		// NOT USED NOW
		// LoadEndpointPath defines the relative URL on which
		// the server listens for function load
		// requests. Optional; default "/specialize".
		LoadEndpointPath string `json:"-"` // `json:"loadendpointpath"`

		// NOT USED NOW
		// FunctionEndpointPort defines the port on which the
		// server listens for function requests. Optional;
		// default 8888.
		FunctionEndpointPort int32 `json:"-"` // `json:"functionendpointport"`

		// (Optional) Container allows the modification of the deployed runtime
		// container using the Kubernetes Container spec. Fission overrides
		// the following fields:
		// - Name
		// - Image; set to the Runtime.Image
		// - TerminationMessagePath
		// - ImagePullPolicy
		//
		// You can set either PodSpec or Container, but not both.
		// kubebuilder:validation:XPreserveUnknownFields=true
		Container *apiv1.Container `json:"container,omitempty"`

		// (Optional) Podspec allows modification of deployed runtime pod with Kubernetes PodSpec
		// The merging logic is briefly described below and detailed MergePodSpec function
		// - Volumes mounts and env variables for function and fetcher container are appended
		// - All additional containers and init containers are appended
		// - Volume definitions are appended
		// - Lists such as tolerations, ImagePullSecrets, HostAliases are appended
		// - Structs are merged and variables from pod spec take precedence
		//
		// You can set either PodSpec or Container, but not both.
		PodSpec *apiv1.PodSpec `json:"podspec,omitempty"`
	}

	// Builder is the setting for environment builder.
	// Bounded podspec / container safety rules — see the matching Runtime block above.
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.hostNetwork) || !self.podspec.hostNetwork",message="spec.builder.podspec.hostNetwork is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.hostPID) || !self.podspec.hostPID",message="spec.builder.podspec.hostPID is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.hostIPC) || !self.podspec.hostIPC",message="spec.builder.podspec.hostIPC is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.serviceAccountName) || self.podspec.serviceAccountName == ''",message="spec.builder.podspec.serviceAccountName override is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.podspec) || !has(self.podspec.serviceAccount) || self.podspec.serviceAccount == ''",message="spec.builder.podspec.serviceAccount override is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.container) || !has(self.container.securityContext) || !has(self.container.securityContext.privileged) || !self.container.securityContext.privileged",message="spec.builder.container.securityContext.privileged=true is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.container) || !has(self.container.securityContext) || !has(self.container.securityContext.allowPrivilegeEscalation) || !self.container.securityContext.allowPrivilegeEscalation",message="spec.builder.container.securityContext.allowPrivilegeEscalation=true is not allowed"
	// +kubebuilder:validation:XValidation:rule="!has(self.container) || !has(self.container.securityContext) || !has(self.container.securityContext.capabilities) || !has(self.container.securityContext.capabilities.add) || self.container.securityContext.capabilities.add.all(c, c == 'NET_BIND_SERVICE')",message="spec.builder.container.securityContext.capabilities.add may only contain NET_BIND_SERVICE (PSA restricted)"
	Builder struct {
		// Image for containing the language compilation environment.
		Image string `json:"image,omitempty"`

		// (Optional) Default build command to run for this build environment.
		Command string `json:"command,omitempty"`

		// (Optional) Container allows the modification of the deployed builder
		// container using the Kubernetes Container spec. Fission overrides
		// the following fields:
		// - Name
		// - Image; set to the Builder.Image
		// - Command; set to the Builder.Command
		// - TerminationMessagePath
		// - ImagePullPolicy
		// - ReadinessProbe
		Container *apiv1.Container `json:"container,omitempty"`

		// PodSpec will store the spec of the pod that will be applied to the pod created for the builder
		PodSpec *apiv1.PodSpec `json:"podspec,omitempty"`
	}

	// EnvironmentSpec contains with builder, runtime and some other related environment settings.
	EnvironmentSpec struct {
		// Version is the Environment API version
		//
		// Version "1" allows user to run code snippet in a file, and
		// it's supported by most of the environments except tensorflow-serving.
		//
		// Version "2" supports downloading and compiling user function if source archive is not empty.
		//
		// Version "3" is almost the same with v2, but you're able to control the size of pre-warm pool of the environment.
		// +kubebuilder:validation:Minimum=1
		// +kubebuilder:validation:Maximum=3
		// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.version is immutable"
		Version int `json:"version"`

		// Runtime is configuration for running function, like container image etc.
		Runtime Runtime `json:"runtime"`

		// (Optional) Builder is configuration for builder manager to launch environment builder to build source code into
		// deployable binary.
		// +optional
		Builder Builder `json:"builder"`

		// NOT USED NOW.
		// (Optional) Strongly encouraged. Used to populate links from UI, CLI, etc.
		// +optional
		DocumentationURL string `json:"-"` // `json:"documentationurl,omitempty"`

		// (Optional) defaults to 'single'. Fission workflow uses
		// 'infinite' to load multiple functions in one function pod.
		// Available value:
		// - single
		// - infinite
		// +optional
		// +kubebuilder:validation:Enum=single;infinite
		AllowedFunctionsPerContainer AllowedFunctionsPerContainer `json:"allowedFunctionsPerContainer,omitempty"`

		// Istio default blocks all egress traffic for safety.
		// To enable accessibility of external network for builder/function pod, set to 'true'.
		// (Optional) defaults to 'false'
		// +optional
		AllowAccessToExternalNetwork bool `json:"allowAccessToExternalNetwork,omitempty"`

		// The request and limit CPU/MEM resource setting for poolmanager to set up pods in the pre-warm pool.
		// (Optional) defaults to no limitation.
		// +optional
		Resources apiv1.ResourceRequirements `json:"resources"`

		// The initial pool size for environment
		// +optional
		// +kubebuilder:validation:Minimum=0
		Poolsize int `json:"poolsize,omitempty"`

		// The grace time for pod to perform connection draining before termination. The unit is in seconds.
		// (Optional) defaults to 360 seconds
		// +optional
		// +kubebuilder:validation:Minimum=0
		TerminationGracePeriod int64 `json:"terminationGracePeriod,omitempty"`

		// KeepArchive is used by fetcher to determine if the extracted archive
		// or unarchived file should be placed, which is then used by specialize handler.
		// (This is mainly for the JVM environment because .jar is one kind of zip archive.)
		// +optional
		KeepArchive bool `json:"keeparchive"`

		// ImagePullSecret is the secret for Kubernetes to pull an image from a
		// private registry.
		// +optional
		ImagePullSecret string `json:"imagepullsecret"`
	}
	// AllowedFunctionsPerContainer defaults to 'single'. Related to Fission Workflows
	AllowedFunctionsPerContainer string

	//
	// Triggers
	//

	// HTTPTriggerSpec is for router to expose user functions at the given URL path.
	// +kubebuilder:validation:XValidation:rule="self.relativeurl != '' || (has(self.prefix) && self.prefix != '')",message="HTTPTriggerSpec: at least one of relativeurl or prefix must be set"
	// +kubebuilder:validation:XValidation:rule="self.relativeurl == '' || (self.relativeurl.startsWith('/') && self.relativeurl != '/' && !self.relativeurl.matches('(^|/)[.][.](/|$)') && !(self.relativeurl in ['/router-healthz','/readyz','/_version','/auth/login']) && !self.relativeurl.startsWith('/fission-function/'))",message="HTTPTriggerSpec.relativeurl must start with '/', not be '/', not contain '..' path segments, not collide with a router-owned path (/router-healthz, /readyz, /_version, /auth/login), and not start with /fission-function/"
	// +kubebuilder:validation:XValidation:rule="!has(self.prefix) || self.prefix == '' || (self.prefix.startsWith('/') && self.prefix != '/' && !self.prefix.matches('(^|/)[.][.](/|$)') && !(self.prefix in ['/router-healthz','/readyz','/_version','/auth/login']) && !self.prefix.startsWith('/fission-function/'))",message="HTTPTriggerSpec.prefix must start with '/', not be '/', not contain '..' path segments, not collide with a router-owned path (/router-healthz, /readyz, /_version, /auth/login), and not start with /fission-function/"
	HTTPTriggerSpec struct {
		// TODO: remove this field since we have IngressConfig already
		// Deprecated: the original idea of this field is not for setting Ingress.
		// Since we have IngressConfig now, remove Host after couple releases.
		// +optional
		// +kubebuilder:validation:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?$`
		Host string `json:"host"`

		// RelativeURL is the exposed URL for external client to access a function with.
		// +optional
		RelativeURL string `json:"relativeurl"`

		// Prefix with which functions are exposed.
		// NOTE: Prefix takes precedence over URL/RelativeURL.
		// Note that it does not treat slashes specially ("/foobar/" will be matched by
		// the prefix "/foobar").
		// +optional
		Prefix *string `json:"prefix,omitempty"`

		// When function is exposed with Prefix based path,
		// keepPrefix decides whether to keep or trim prefix in URL while invoking function.
		// +optional
		KeepPrefix bool `json:"keepPrefix,omitempty"`

		// Use Methods instead of Method. This field is going to be deprecated in a future release
		// HTTP method to access a function.
		// +optional
		// +kubebuilder:validation:Enum="";GET;HEAD;POST;PUT;PATCH;DELETE;CONNECT;OPTIONS;TRACE
		Method string `json:"method"`

		// HTTP methods to access a function
		// +optional
		// +listType=set
		// +kubebuilder:validation:items:Enum=GET;HEAD;POST;PUT;PATCH;DELETE;CONNECT;OPTIONS;TRACE
		Methods []string `json:"methods,omitempty"`

		// InvocationMode, when "async", forces every request to this trigger into
		// RFC-0024 asynchronous invocation even without the X-Fission-Invoke-Mode
		// header (webhooks from third parties cannot set headers). "" (the default)
		// leaves the per-request header in control.
		// +optional
		// +kubebuilder:validation:Enum="";async
		InvocationMode string `json:"invocationMode,omitempty"`

		// FunctionReference is a reference to the target function.
		FunctionReference FunctionReference `json:"functionref"`

		// If CreateIngress is true, router will create an ingress definition.
		// Deprecated: the Kubernetes Ingress API is frozen. Use RouteConfig
		// (with Provider "gateway") to expose functions through the Gateway API
		// instead. CreateIngress + IngressConfig keep working for the
		// deprecation window but will be removed in a future release.
		// +optional
		CreateIngress bool `json:"createingress"`

		// TODO: make IngressConfig an independent Fission resource
		// IngressConfig for router to set up Ingress.
		// Deprecated: superseded by RouteConfig. See CreateIngress.
		// +optional
		IngressConfig IngressConfig `json:"ingressconfig"`

		// RouteConfig declares how the router exposes this trigger through an
		// external route provider (Ingress or the Gateway API). It is the
		// provider-neutral successor to CreateIngress + IngressConfig: when set
		// it takes precedence over those fields. Leave nil to expose the
		// function only through the router's own URL.
		// +optional
		RouteConfig *RouteConfig `json:"routeConfig,omitempty"`

		// CorsConfig configures CORS response headers for browser
		// callers of this trigger. When nil, the router emits no
		// Access-Control-* headers and the browser's Same-Origin
		// Policy enforces cluster isolation from cross-origin pages
		// (the deny-by-default behaviour). Set this field to
		// allowlist specific origins for SPAs that legitimately
		// call this trigger cross-origin.
		// +optional
		CorsConfig *HTTPTriggerCorsConfig `json:"corsConfig,omitempty"`
	}

	// HTTPTriggerCorsConfig is the per-HTTPTrigger CORS allowlist.
	// It is consumed by the router public listener to attach a CORS
	// middleware to the trigger's route. Triggers without a CorsConfig
	// receive no Access-Control-* response headers and therefore deny
	// cross-origin browser reads at the Same-Origin Policy layer.
	// +kubebuilder:validation:XValidation:rule="!(has(self.allowCredentials) && self.allowCredentials && has(self.allowOrigins) && '*' in self.allowOrigins)",message="corsConfig.allowOrigins=[\"*\"] cannot be combined with allowCredentials=true; browsers refuse the response"
	HTTPTriggerCorsConfig struct {
		// AllowOrigins is the list of allowed origins (scheme + host +
		// port). Use ["*"] to allow any origin. Mixing "*" with
		// AllowCredentials=true is a configuration error and is
		// rejected by validation; browsers refuse the response in that
		// combination.
		// +optional
		// +listType=set
		AllowOrigins []string `json:"allowOrigins,omitempty"`

		// AllowMethods is the list of HTTP methods echoed in the
		// Access-Control-Allow-Methods preflight response. When empty
		// the trigger's existing Methods field is used.
		// +optional
		// +listType=set
		AllowMethods []string `json:"allowMethods,omitempty"`

		// AllowHeaders is the list of request headers the browser is
		// allowed to send, echoed in Access-Control-Allow-Headers.
		// +optional
		// +listType=set
		AllowHeaders []string `json:"allowHeaders,omitempty"`

		// ExposeHeaders is the list of response headers exposed to
		// the browser, set in Access-Control-Expose-Headers.
		// +optional
		// +listType=set
		ExposeHeaders []string `json:"exposeHeaders,omitempty"`

		// AllowCredentials sets Access-Control-Allow-Credentials.
		// When true, AllowOrigins MUST NOT contain "*".
		// +optional
		AllowCredentials bool `json:"allowCredentials,omitempty"`

		// MaxAge is the preflight cache lifetime as parsed by
		// time.ParseDuration. Empty means the header is omitted.
		// +optional
		MaxAge string `json:"maxAge,omitempty"`
	}

	// IngressConfig is for router to set up Ingress.
	// Deprecated: superseded by RouteConfig. The Kubernetes Ingress API is
	// frozen; use RouteConfig with Provider "gateway" for new triggers.
	// +kubebuilder:validation:XValidation:rule="!has(self.path) || self.path == '' || self.path.startsWith('/')",message="ingressconfig.path must be an absolute path (start with '/')"
	// +kubebuilder:validation:XValidation:rule="!has(self.host) || self.host == '' || self.host == '*' || self.host.matches(r'^(\\*\\.)?[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$')",message="ingressconfig.host must be empty, '*', a valid DNS1123 subdomain, or a wildcard DNS1123 subdomain (e.g. *.example.com)"
	IngressConfig struct {
		// Annotations will be added to metadata when creating Ingress.
		// +optional
		// +nullable
		Annotations map[string]string `json:"annotations"`

		// Path is for path matching. The format of path
		// depends on what ingress controller you used.
		// +optional
		Path string `json:"path"`

		// Host is for ingress controller to apply rules. If
		// host is empty or "*", the rule applies to all
		// inbound HTTP traffic.
		// +optional
		Host string `json:"host"`

		// TLS is for user to specify a Secret that contains
		// TLS key and certificate. The domain name in the
		// key and crt must match the value of Host field.
		// +optional
		TLS string `json:"tls"`
	}

	// RouteConfig declares how the router exposes an HTTPTrigger through an
	// external route provider. It is the provider-neutral successor to the
	// deprecated CreateIngress + IngressConfig fields: the router routes it to
	// the matching RouteProvider based on Provider.
	// +kubebuilder:validation:XValidation:rule="!has(self.path) || self.path == '' || self.path.startsWith('/')",message="routeConfig.path must be an absolute path (start with '/')"
	// +kubebuilder:validation:XValidation:rule="self.provider != 'gateway' || (has(self.gateway) && size(self.gateway.parentRefs) > 0)",message="routeConfig.gateway.parentRefs must list at least one Gateway when provider is 'gateway' (unless the router is configured with a default Gateway)"
	// +kubebuilder:validation:XValidation:rule="self.provider != 'gateway' || !has(self.tls) || self.tls == ''",message="routeConfig.tls applies to the ingress provider only; gateway TLS is configured on the Gateway listener"
	RouteConfig struct {
		// Provider selects the route provider that reconciles this trigger's
		// external route. "ingress" creates a networking.k8s.io Ingress (the
		// deprecated path); "gateway" creates a gateway.networking.k8s.io
		// HTTPRoute attached to an operator-managed Gateway. The "gateway"
		// provider must be enabled on the router (GATEWAY_API_ENABLED).
		// +kubebuilder:validation:Enum=ingress;gateway
		Provider RouteProviderType `json:"provider"`

		// Hostnames the route matches. For the gateway provider these become
		// the HTTPRoute hostnames; for the ingress provider only the first is
		// used as the Ingress rule host. Empty matches all hosts.
		// +optional
		// +listType=set
		Hostnames []string `json:"hostnames,omitempty"`

		// Path is the request path the route matches (must be absolute, start
		// with '/'). Defaults to "/" when empty.
		// +optional
		Path string `json:"path,omitempty"`

		// Annotations are added to the generated route object (Ingress or
		// HTTPRoute). Use these for implementation-specific configuration
		// understood by your Ingress controller or Gateway implementation.
		// +optional
		// +nullable
		Annotations map[string]string `json:"annotations,omitempty"`

		// TLS names a Secret holding the TLS key and certificate. It applies to
		// the ingress provider only; with the gateway provider TLS termination
		// is configured on the Gateway listener and this field is ignored.
		// +optional
		TLS string `json:"tls,omitempty"`

		// Gateway holds Gateway-API-specific configuration. Required (at least
		// one parentRef) when Provider is "gateway", unless the router is
		// configured with a default Gateway parentRef.
		// +optional
		Gateway *GatewayRouteConfig `json:"gateway,omitempty"`
	}

	// GatewayRouteConfig is the Gateway-API-specific portion of a RouteConfig.
	GatewayRouteConfig struct {
		// ParentRefs are the Gateways the generated HTTPRoute attaches to. The
		// referenced Gateways are owned by the cluster operator (Fission does
		// not create Gateways or GatewayClasses). A cross-namespace parentRef
		// requires a ReferenceGrant in the Gateway's namespace.
		// +optional
		// +listType=atomic
		ParentRefs []GatewayParentRef `json:"parentRefs,omitempty"`
	}

	// GatewayParentRef references a Gateway (and optionally a specific listener)
	// that the generated HTTPRoute attaches to. It mirrors the subset of
	// gateway.networking.k8s.io ParentReference that Fission needs.
	GatewayParentRef struct {
		// Name of the parent Gateway.
		Name string `json:"name"`

		// Namespace of the parent Gateway. Defaults to the router's namespace
		// when empty. A non-empty, different namespace needs a ReferenceGrant.
		// +optional
		Namespace string `json:"namespace,omitempty"`

		// SectionName selects a specific listener on the Gateway. Empty attaches
		// to all compatible listeners.
		// +optional
		SectionName string `json:"sectionName,omitempty"`

		// Port narrows attachment to a specific Gateway listener port.
		// +optional
		// +kubebuilder:validation:Minimum=1
		// +kubebuilder:validation:Maximum=65535
		Port int32 `json:"port,omitempty"`
	}

	// KubernetesWatchTriggerSpec defines spec of KuberenetesWatchTrigger
	KubernetesWatchTriggerSpec struct {
		// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
		// +kubebuilder:validation:MaxLength=63
		Namespace string `json:"namespace"`

		// Type of resource to watch (Pod, Service, etc.)
		// +kubebuilder:validation:XValidation:rule="self.upperAscii() in ['POD','SERVICE','REPLICATIONCONTROLLER','JOB']",message="spec.type must be one of POD, SERVICE, REPLICATIONCONTROLLER, JOB (case-insensitive)"
		Type string `json:"type"`

		// Resource labels
		// +optional
		LabelSelector map[string]string `json:"labelselector"`

		// The reference to a function for kubewatcher to invoke with
		// when receiving events.
		FunctionReference FunctionReference `json:"functionref"`
	}

	// MessageQueueType refers to Type of message queue
	MessageQueueType string

	// MessageQueueTriggerSpec defines a binding from a topic in a
	// message queue to a function.
	MessageQueueTriggerSpec struct {
		// The reference to a function for message queue trigger to invoke with
		// when receiving messages from subscribed topic.
		// +optional
		FunctionReference FunctionReference `json:"functionref"`

		// Type of message queue (NATS, Kafka, AzureQueue)
		// +optional
		MessageQueueType MessageQueueType `json:"messageQueueType"`

		// Subscribed topic
		Topic string `json:"topic"`

		// Topic for message queue trigger to sent response from function.
		// +optional
		ResponseTopic string `json:"respTopic,omitempty"`

		// Topic to collect error response sent from function
		// +optional
		ErrorTopic string `json:"errorTopic"`

		// Maximum times for message queue trigger to retry
		// +optional
		MaxRetries int `json:"maxRetries"`

		// Content type of payload
		// +optional
		ContentType string `json:"contentType"`

		// The period to check each trigger source on every ScaledObject, and scale the deployment up or down accordingly
		// +optional
		PollingInterval *int32 `json:"pollingInterval,omitempty"`

		// The period to wait after the last trigger reported active before scaling the deployment back to 0
		// +optional
		CooldownPeriod *int32 `json:"cooldownPeriod,omitempty"`

		// Minimum number of replicas KEDA will scale the deployment down to
		// +optional
		MinReplicaCount *int32 `json:"minReplicaCount,omitempty"`

		// Maximum number of replicas KEDA will scale the deployment up to
		// +optional
		MaxReplicaCount *int32 `json:"maxReplicaCount,omitempty"`

		// ScalerTrigger fields
		// +optional
		Metadata map[string]string `json:"metadata"`

		// Secret name
		// +optional
		Secret string `json:"secret,omitempty"`

		// Kind of Message Queue Trigger to be created, by default its fission
		// +optional
		MqtKind string `json:"mqtkind,omitempty"`

		// (Optional) Podspec allows modification of deployed runtime pod with Kubernetes PodSpec
		// The merging logic is briefly described below and detailed MergePodSpec function
		// - Volumes mounts and env variables for function and fetcher container are appended
		// - All additional containers and init containers are appended
		// - Volume definitions are appended
		// - Lists such as tolerations, ImagePullSecrets, HostAliases are appended
		// - Structs are merged and variables from pod spec take precedence
		// +optional
		PodSpec *apiv1.PodSpec `json:"podspec,omitempty"`
	}

	// TimeTriggerSpec invokes the specific function at a time or
	// times specified by a cron string.
	TimeTriggerSpec struct {
		// Cron schedule
		Cron string `json:"cron"`

		// The reference to function. Alias is read from the embedded
		// FunctionReference.Alias (RFC-0025) — TimeTriggerSpec has no field of
		// its own for it, so there is exactly one JSON path (spec.functionref.alias)
		// and one Go path (spec.Alias, promoted) for the concept, never two
		// competing ones. The timer publisher (a later RFC-0025 task) reads it
		// the same way timer.go:80 already reads the promoted spec.Name today.
		FunctionReference `json:"functionref"`

		// HTTP Method for trigger, ex : GET, POST, PUT, DELETE, HEAD (default: "POST")
		// +kubebuilder:default:="POST"
		// +optional
		Method string `json:"method,omitempty"`

		// Subpath to trigger a specific route if function
		// internally supports routing, (default: "/")
		// +kubebuilder:default:="/"
		// +optional
		Subpath string `json:"subpath,omitempty"`
	}
	// FailureType refers to the type of failure
	FailureType string

	// CanaryConfigSpec defines the canary configuration spec
	CanaryConfigSpec struct {
		// HTTP trigger that this config references
		Trigger string `json:"trigger"`

		// New version of the function
		NewFunction string `json:"newfunction"`

		// Old stable version of the function
		OldFunction string `json:"oldfunction"`

		// Weight increment step for function
		// +optional
		WeightIncrement int `json:"weightincrement"`

		// Weight increment interval, string representation of time.Duration, ex : 1m, 2h, 2d (default: "2m")
		// +optional
		WeightIncrementDuration string `json:"duration"`

		// Threshold in percentage beyond which the new version of the function is considered unstable
		// +optional
		FailureThreshold int `json:"failurethreshold"`
		// +optional
		FailureType FailureType `json:"failureType"`
	}

	// CanaryConfigStatus represents canary config status
	CanaryConfigStatus struct {
		// +optional
		Status string `json:"status,omitempty"`

		// Conditions represent the latest observations of the canary's state.
		// +optional
		// +patchMergeKey=type
		// +patchStrategy=merge
		// +listType=map
		// +listMapKey=type
		Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	}

	// FunctionStatus describes the observed state of a Function.
	FunctionStatus struct {
		// ObservedGeneration reflects the .metadata.generation that the
		// controller observed when it last updated the status.
		// +optional
		ObservedGeneration int64 `json:"observedGeneration,omitempty"`

		// ProvisionedReady is the number of warm specialized pods the provisioner
		// is currently maintaining for this function (RFC-0026). Only meaningful
		// when Spec.ProvisionedConcurrency is non-nil. Reported by the executor's
		// provisioner on each reconcile pass.
		// +optional
		ProvisionedReady int `json:"provisionedReady,omitempty"`

		// ProvisionedTarget is the effective target the provisioner is currently
		// aiming for (base Target, or a schedule-window override in PR 2). Lets
		// `fission fn get` show "3/5 provisioned pods ready".
		// +optional
		ProvisionedTarget int `json:"provisionedTarget,omitempty"`

		// ProvisionedSpecTarget is the raw Target from spec (before the namespace
		// cap clamp). When ProvisionedSpecTarget > ProvisionedTarget, the
		// provisioner clamped the target to the namespace cap
		// (executor.provisionedConcurrency.maxPerFunction) and the Provisioned
		// condition carries reason ProvisionedClamped. Lets `fission fn get`
		// show the spec-vs-effective divergence.
		// +optional
		ProvisionedSpecTarget int `json:"provisionedSpecTarget,omitempty"`

		// Conditions represent the latest observations of the function's state.
		// +optional
		// +patchMergeKey=type
		// +patchStrategy=merge
		// +listType=map
		// +listMapKey=type
		Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	}

	// EnvironmentStatus describes the observed state of an Environment.
	EnvironmentStatus struct {
		// +optional
		ObservedGeneration int64 `json:"observedGeneration,omitempty"`

		// +optional
		// +patchMergeKey=type
		// +patchStrategy=merge
		// +listType=map
		// +listMapKey=type
		Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	}

	// HTTPTriggerStatus describes the observed state of an HTTPTrigger.
	HTTPTriggerStatus struct {
		// +optional
		ObservedGeneration int64 `json:"observedGeneration,omitempty"`

		// +optional
		// +patchMergeKey=type
		// +patchStrategy=merge
		// +listType=map
		// +listMapKey=type
		Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	}

	// KubernetesWatchTriggerStatus describes the observed state of a KubernetesWatchTrigger.
	KubernetesWatchTriggerStatus struct {
		// +optional
		ObservedGeneration int64 `json:"observedGeneration,omitempty"`

		// +optional
		// +patchMergeKey=type
		// +patchStrategy=merge
		// +listType=map
		// +listMapKey=type
		Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	}

	// TimeTriggerStatus describes the observed state of a TimeTrigger.
	TimeTriggerStatus struct {
		// +optional
		ObservedGeneration int64 `json:"observedGeneration,omitempty"`

		// +optional
		// +patchMergeKey=type
		// +patchStrategy=merge
		// +listType=map
		// +listMapKey=type
		Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	}

	// MessageQueueTriggerStatus describes the observed state of a MessageQueueTrigger.
	MessageQueueTriggerStatus struct {
		// +optional
		ObservedGeneration int64 `json:"observedGeneration,omitempty"`

		// +optional
		// +patchMergeKey=type
		// +patchStrategy=merge
		// +listType=map
		// +listMapKey=type
		Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	}

	// AuthLogin defines the body for router login
	AuthLogin struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	// RouterAuthToken defines the authorization token for accessing router
	RouterAuthToken struct {
		AccessToken string `json:"accesstoken"`
		TokenType   string `json:"tokentype"`
	}
)

// IsEmpty checks if the archive byte and litreal are of length 0
func (a Archive) IsEmpty() bool {
	return len(a.Literal) == 0 && len(a.URL) == 0 && a.OCI == nil
}

func (fn Function) GetConcurrency() int {
	if fn.Spec.Concurrency == 0 {
		return DefaultConcurrency
	}
	return fn.Spec.Concurrency
}

func (fn Function) GetRetainPods() int {
	return fn.Spec.RetainPods
}

func (fn Function) GetRequestPerPod() int {
	if fn.Spec.RequestsPerPod == 0 {
		return DefaultRequestsPerPod
	}
	return fn.Spec.RequestsPerPod
}

// StrictConcurrencyEnforcement reports whether the function opted out of
// router-local admission (RFC-0002) via the
// fission.io/concurrency-enforcement: strict annotation: every request then
// goes through the executor's PoolCache exactly as before the EndpointSlice
// data plane, giving exact global per-pod concurrency accounting at the cost
// of the warm-path RPCs.
func (fn Function) StrictConcurrencyEnforcement() bool {
	return fn.Annotations[ConcurrencyEnforcementAnnotation] == ConcurrencyEnforcementStrict
}

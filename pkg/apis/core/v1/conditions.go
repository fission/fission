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
//
// Phase 1 (this file) defines only the constants — controllers begin
// writing them in subsequent phases. See rfc/0003-crd-modernization.md.
const (
	// Function conditions
	FunctionConditionReady        = "Ready"
	FunctionConditionPackageReady = "PackageReady"
	FunctionConditionEnvReady     = "EnvironmentReady"
	FunctionConditionProgressing  = "Progressing"

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

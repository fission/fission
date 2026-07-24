// SPDX-FileCopyrightText: The Kubernetes Authors
//
// SPDX-License-Identifier: Apache-2.0

// The file comes from Kubernetes code-generator repo with custom modification.
// https://github.com/kubernetes/code-generator/blob/0826954c61ed88ac5d75e771ade6aae646ca5268/_examples/HyphenGroup/apis/example/v1/register.go

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// addKnownTypes registers the manually written types with the scheme. The
// registration of the generated functions takes place in the generated files.
// The separation makes the code compile even when the generated files are missing.
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&Function{},
		&FunctionList{},
		&Environment{},
		&EnvironmentList{},
		&HTTPTrigger{},
		&HTTPTriggerList{},
		&KubernetesWatchTrigger{},
		&KubernetesWatchTriggerList{},
		&TimeTrigger{},
		&TimeTriggerList{},
		&MessageQueueTrigger{},
		&MessageQueueTriggerList{},
		&Package{},
		&PackageList{},
		&CanaryConfig{},
		&CanaryConfigList{},
		&FissionTenant{},
		&FissionTenantList{},
		&Workflow{},
		&WorkflowList{},
		&WorkflowRun{},
		&WorkflowRunList{},
		&FunctionVersion{},
		&FunctionVersionList{},
		&FunctionAlias{},
		&FunctionAliasList{})
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}

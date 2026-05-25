// SPDX-FileCopyrightText: The Kubernetes Authors
//
// SPDX-License-Identifier: Apache-2.0

// The file comes from Kubernetes code-generator repo with custom modification.
// https://github.com/kubernetes/code-generator/blob/0826954c61ed88ac5d75e771ade6aae646ca5268/_examples/HyphenGroup/apis/example/v1/register.go

package v1

func init() {
	// We only register manually written functions here. The registration of the
	// generated functions takes place in the generated files. The separation
	// makes the code compile even when the generated files are missing.
	SchemeBuilder.Register(&Function{},
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
		&CanaryConfigList{})
}

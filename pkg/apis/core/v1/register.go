/*
Copyright 2019 The Kubernetes Authors.
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

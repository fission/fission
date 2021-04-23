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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// In order not to break the backward compatibility, keep coreV1 types stay
// at "fission.io" group instead of moving them to "core.fission.io".
// If the value of group is different from the one we register, the
// CRD client will not be able to get anything from the API server.
var SchemeGroupVersion = schema.GroupVersion{Group: "fission.io", Version: "v1"}

var (
	// TODO: move SchemeBuilder with zz_generated.deepcopy.go to k8s.io/api.
	// localSchemeBuilder and AddToScheme will stay in k8s.io/kubernetes.
	SchemeBuilder      runtime.SchemeBuilder
	localSchemeBuilder = &SchemeBuilder
	AddToScheme        = localSchemeBuilder.AddToScheme
)

func init() {
	// We only register manually written functions here. The registration of the
	// generated functions takes place in the generated files. The separation
	// makes the code compile even when the generated files are missing.
	localSchemeBuilder.Register(addKnownTypes)
}

// Resource takes an unqualified resource and returns a Group qualified GroupResource
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

// Adds the list of known types to the given scheme.
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(
		SchemeGroupVersion,
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
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}

/*
Copyright 2018 The Fission Authors.

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

// This file tells deepcopy-gen to generate deepcopy methods for all structs in the package.
// For more details, please visit https://blog.openshift.com/kubernetes-deep-dive-code-generation-customresources/

// +k8s:deepcopy-gen=package
// +k8s:defaulter-gen=TypeMeta
// +groupName=fission.io
// +groupGoName=core
//
// In order not to break the backward compatibility, keep coreV1 types stay
// at "fission.io" group instead of moving them to "core.fission.io".
// If the value of group is different from the one we register, the
// CRD client will not be able to get anything from the API server.
package v1

const (
	CRD_VERSION          = "fission.io/v1"
	CRD_NAME_ENVIRONMENT = "Environment"
)

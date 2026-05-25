// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// This file tells deepcopy-gen to generate deepcopy methods for all structs in the package.
// For more details, please visit https://blog.openshift.com/kubernetes-deep-dive-code-generation-customresources/

// +k8s:deepcopy-gen=package
// +k8s:defaulter-gen=TypeMeta
// +groupName=fission.io
// +groupGoName=core
package v1

const (
	CRD_VERSION          = "fission.io/v1"
	CRD_NAME_ENVIRONMENT = "Environment"
)

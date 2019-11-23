/*
Copyright 2019 The Fission Authors.

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

package util

import (
	apiv1 "k8s.io/api/core/v1"
)

// ApplyImagePullSecret applies image pull secret to the give pod spec.
// It's intentional not to check the existence of secret here, Kubernetes
// will set Pod status to "ImagePullBackOff" once kubelet failed to pull
// image so that users will know what's happening.
func ApplyImagePullSecret(secret string, podspec apiv1.PodSpec) *apiv1.PodSpec {
	podspec.ImagePullSecrets = []apiv1.LocalObjectReference{{Name: secret}}
	return &podspec
}

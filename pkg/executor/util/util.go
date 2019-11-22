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
	"github.com/pkg/errors"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ApplyImagePullSecret applies image pull secret to the give pod spec. An error will be returned if failed to get secret.
func ApplyImagePullSecret(client *kubernetes.Clientset, secret string, secretNS string, podspec apiv1.PodSpec) (*apiv1.PodSpec, error) {
	if len(secret) > 0 && client != nil {
		_, err := client.CoreV1().Secrets(secretNS).Get(secret, metav1.GetOptions{})
		if err != nil {
			err = errors.Wrapf(err, "unable to get image pull secret '%v' under namespace '%v'",
				secret, secretNS)
			return nil, err
		}
		podspec.ImagePullSecrets = []apiv1.LocalObjectReference{{Name: secret}}
	}
	return &podspec, nil
}

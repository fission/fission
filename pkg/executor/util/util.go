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
	"context"
	"errors"
	"sync"
	"time"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// ApplyImagePullSecret applies image pull secret to the give pod spec.
// It's intentional not to check the existence of secret here.
// First, Kubernetes will set Pod status to "ImagePullBackOff" once
// kubelet failed to pull image so that users will know what's happening.
// Second, Fission no longer need to handle "secret not found" error
// when creating the environment deployment since kubelet will retry to
// pull image until successes.
func ApplyImagePullSecret(secret string, podspec apiv1.PodSpec) *apiv1.PodSpec {
	if len(secret) > 0 {
		podspec.ImagePullSecrets = []apiv1.LocalObjectReference{{Name: secret}}
	}
	return &podspec
}

// WaitTimeout starts a wait group with timeout
func WaitTimeout(wg *sync.WaitGroup, timeout time.Duration) {
	waitCh := make(chan struct{})
	go func() {
		defer close(waitCh)
		wg.Wait()
	}()
	select {
	case <-waitCh:
	case <-time.After(timeout):
	}
}

// ConvertConfigSecrets returns envFromSource which can be passed directly into the pod spec
func ConvertConfigSecrets(ctx context.Context, fn *fv1.Function, kc kubernetes.Interface) ([]apiv1.EnvFromSource, error) {

	cmList := fn.Spec.ConfigMaps
	secList := fn.Spec.Secrets
	cmEnvSources := make([]*apiv1.ConfigMapEnvSource, 0)
	secEnvSources := make([]*apiv1.SecretEnvSource, 0)
	for _, cm := range cmList {
		if cm.Namespace != fn.Namespace {
			return nil, errors.New("function should not reference config map of different namespace")
		}
		_, err := kc.CoreV1().ConfigMaps(cm.Namespace).Get(ctx, cm.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}

		cmEnvSource := &apiv1.ConfigMapEnvSource{
			LocalObjectReference: apiv1.LocalObjectReference{Name: cm.Name},
		}

		cmEnvSources = append(cmEnvSources, cmEnvSource)
	}

	for _, sec := range secList {
		if sec.Namespace != fn.Namespace {
			return nil, errors.New("function should not reference secret of different namespace")
		}
		_, err := kc.CoreV1().Secrets(sec.Namespace).Get(ctx, sec.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}

		secEnvSource := &apiv1.SecretEnvSource{
			LocalObjectReference: apiv1.LocalObjectReference{Name: sec.Name},
		}

		secEnvSources = append(secEnvSources, secEnvSource)
	}

	envFromSources := make([]apiv1.EnvFromSource, 0)
	for _, cmEnvSource := range cmEnvSources {
		envFromSource := apiv1.EnvFromSource{

			ConfigMapRef: cmEnvSource,
		}
		envFromSources = append(envFromSources, envFromSource)
	}

	for _, secEnvSource := range secEnvSources {
		envFromSource := apiv1.EnvFromSource{

			SecretRef: secEnvSource,
		}
		envFromSources = append(envFromSources, envFromSource)
	}
	return envFromSources, nil
}

func GetSpecConfigMap(ctx context.Context, kubeClient kubernetes.Interface, podSpec apiv1.PodSpec, podSpecConfigMap *apiv1.ConfigMap) (*apiv1.PodSpec, error) {

	if podSpecConfigMap == nil {
		return nil, nil
	}

	var additionalSpec apiv1.PodSpec

	err := yaml.Unmarshal([]byte(podSpecConfigMap.Data["spec"]), &additionalSpec)
	if err != nil {
		return nil, err
	}

	updatedPodSpec, err := MergePodSpec(&podSpec, &additionalSpec)
	if err != nil {
		return nil, err
	}

	return updatedPodSpec, nil
}

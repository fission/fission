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

package newdeploy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

const (
	saTokenMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"
	fetcherSAVolume  = "fission-fetcher-sa-token"
	fetcherCName     = "fetcher"
	envCName         = "newdeploy-test-env"
)

// newTestNewDeploy returns a minimal NewDeploy wired up for unit tests of
// getDeploymentSpec.
func newTestNewDeploy(t *testing.T) *NewDeploy {
	t.Helper()
	cfg, err := fetcherConfig.MakeFetcherConfig("/userfunc")
	require.NoError(t, err)
	return &NewDeploy{
		logger:           loggerfactory.GetLogger(),
		kubernetesClient: fake.NewSimpleClientset(),
		fetcherConfig:    cfg,
	}
}

func newTestNewDeployEnv() *fv1.Environment {
	return &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envCName,
			Namespace: "default",
		},
		Spec: fv1.EnvironmentSpec{
			Version: 1,
			Runtime: fv1.Runtime{
				Image: "fission/test-env:latest",
			},
		},
	}
}

func newTestNewDeployFunction() *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "newdeploy-test-fn",
			Namespace: "default",
		},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{
				Name:      envCName,
				Namespace: "default",
			},
			Package: fv1.FunctionPackageRef{
				PackageRef: fv1.PackageRef{
					Namespace:       "default",
					Name:            "pkg-1",
					ResourceVersion: "1",
				},
			},
			InvokeStrategy: fv1.InvokeStrategy{
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType: fv1.ExecutorTypeNewdeploy,
				},
			},
		},
	}
}

// TestNewDeployPodSpecDoesNotAutomountTokenInUserContainer asserts the
// security-advisory-5 invariant for the new-deploy backend: the
// fission-fetcher SA token is only mounted inside the fetcher container,
// never in the user-code container.
func TestNewDeployPodSpecDoesNotAutomountTokenInUserContainer(t *testing.T) {
	deploy := newTestNewDeploy(t)
	env := newTestNewDeployEnv()
	fn := newTestNewDeployFunction()
	ctx := t.Context()

	replicas := int32(1)
	deployment, err := deploy.getDeploymentSpec(
		ctx, fn, env, &replicas,
		"newdeploy-test-fn",
		"default",
		map[string]string{"app": "newdeploy-test"},
		map[string]string{},
	)
	require.NoError(t, err)
	pod := deployment.Spec.Template

	require.NotNil(t, pod.Spec.AutomountServiceAccountToken,
		"pod-level AutomountServiceAccountToken must be explicitly set, not nil")
	assert.False(t, *pod.Spec.AutomountServiceAccountToken,
		"pod-level AutomountServiceAccountToken must be false to keep the user container clean")
	assert.Equal(t, fv1.FissionFetcherSA, pod.Spec.ServiceAccountName)

	var projected *apiv1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == fetcherSAVolume {
			projected = &pod.Spec.Volumes[i]
			break
		}
	}
	require.NotNil(t, projected, "projected SA token volume %q must exist", fetcherSAVolume)
	require.NotNil(t, projected.Projected, "%q must be a projected volume", fetcherSAVolume)
	var hasToken, hasCA, hasNS bool
	for _, src := range projected.Projected.Sources {
		switch {
		case src.ServiceAccountToken != nil:
			hasToken = true
			assert.Equal(t, "token", src.ServiceAccountToken.Path)
		case src.ConfigMap != nil:
			hasCA = src.ConfigMap.Name == "kube-root-ca.crt"
		case src.DownwardAPI != nil:
			hasNS = true
		}
	}
	assert.True(t, hasToken, "projected volume must include a ServiceAccountToken source")
	assert.True(t, hasCA, "projected volume must include the kube-root-ca.crt ConfigMap")
	assert.True(t, hasNS, "projected volume must include a namespace DownwardAPI source")

	var fetcher, user *apiv1.Container
	for i := range pod.Spec.Containers {
		switch pod.Spec.Containers[i].Name {
		case fetcherCName:
			fetcher = &pod.Spec.Containers[i]
		case envCName:
			user = &pod.Spec.Containers[i]
		}
	}
	require.NotNil(t, fetcher, "fetcher container must be present")
	require.NotNil(t, user, "user (env) container must be present")

	hasProjectedTokenMount := false
	for _, vm := range fetcher.VolumeMounts {
		if vm.MountPath == saTokenMountPath {
			hasProjectedTokenMount = true
			assert.Equal(t, fetcherSAVolume, vm.Name,
				"fetcher SA token mount must be backed by the projected volume")
			assert.True(t, vm.ReadOnly, "fetcher SA token mount must be read-only")
		}
	}
	assert.True(t, hasProjectedTokenMount,
		"fetcher must mount its own SA token via projected volume")

	for _, vm := range user.VolumeMounts {
		assert.NotEqual(t, saTokenMountPath, vm.MountPath,
			"user container must not have any volume mount at the SA token path")
	}
}

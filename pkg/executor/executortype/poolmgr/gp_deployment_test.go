/*
Copyright 2016 The Fission Authors.

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

package poolmgr

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

const (
	saTokenMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"
	fetcherSAVolume  = "fission-fetcher-sa-token"
	fetcherCName     = "fetcher"
	envContainerName = "test-env"
)

func TestGetPoolName(t *testing.T) {
	tests := []struct {
		name string
		env  *fv1.Environment
		want string
	}{
		{
			"Under character limit",
			&fv1.Environment{
				TypeMeta: metav1.TypeMeta{
					Kind:       fv1.CRD_NAME_ENVIRONMENT,
					APIVersion: fv1.CRD_VERSION,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					Namespace:       "testns",
					ResourceVersion: "2517",
				},
			},
			"poolmgr-test-testns-2517",
		},
		{
			"Over character limit",
			&fv1.Environment{
				TypeMeta: metav1.TypeMeta{
					Kind:       fv1.CRD_NAME_ENVIRONMENT,
					APIVersion: fv1.CRD_VERSION,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:            "justtryingtoincreasethenumberofcharactersinthisstring",
					Namespace:       "checkingifthegetpoolfunctionworkswithcharactersmorethan18",
					ResourceVersion: "2518",
				},
			},
			"poolmgr-justtryingtoincrea-checkingifthegetpo-2518",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getPoolName(tt.env); got != tt.want {
				t.Errorf("getPoolName() = %s, want = %s len(getPoolName()) = %x len(want) = %x", got, tt.want, len(got), len(tt.want))
			} else {
				fmt.Printf("getPoolName() = %s,length of string = %x", got, len(got))
			}
		})
	}
}

// newTestGenericPool returns a minimal GenericPool wired up just enough to
// exercise genDeploymentSpec in unit tests.
func newTestGenericPool(t *testing.T) *GenericPool {
	t.Helper()
	cfg, err := fetcherConfig.MakeFetcherConfig("/userfunc")
	require.NoError(t, err)
	return &GenericPool{
		logger:        loggerfactory.GetLogger(),
		fetcherConfig: cfg,
	}
}

func newTestEnv() *fv1.Environment {
	return &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envContainerName,
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

// TestGenericPoolPodSpecDoesNotAutomountTokenInUserContainer asserts the
// security-advisory-5 invariant: the fission-fetcher SA token is only
// mounted inside the fetcher container, never in the user-code container.
func TestGenericPoolPodSpecDoesNotAutomountTokenInUserContainer(t *testing.T) {
	gp := newTestGenericPool(t)
	env := newTestEnv()

	deploymentSpec, err := gp.genDeploymentSpec(env)
	require.NoError(t, err)
	pod := deploymentSpec.Template

	// Pod-level AutomountServiceAccountToken must be explicitly false to
	// suppress the implicit /var/run/secrets/kubernetes.io/serviceaccount
	// mount that Kubernetes would otherwise inject into every container.
	require.NotNil(t, pod.Spec.AutomountServiceAccountToken,
		"pod-level AutomountServiceAccountToken must be explicitly set, not nil")
	assert.False(t, *pod.Spec.AutomountServiceAccountToken,
		"pod-level AutomountServiceAccountToken must be false to keep the user container clean")

	// The pod should still run as the fission-fetcher service account so
	// the fetcher container can talk to Kubernetes through its own
	// projected token.
	assert.Equal(t, fv1.FissionFetcherSA, pod.Spec.ServiceAccountName)

	// A projected volume named fission-fetcher-sa-token must exist on the
	// pod and contain the SA token + ca.crt + namespace projections.
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

	// Locate the fetcher and user containers.
	var fetcher, user *apiv1.Container
	for i := range pod.Spec.Containers {
		switch pod.Spec.Containers[i].Name {
		case fetcherCName:
			fetcher = &pod.Spec.Containers[i]
		case envContainerName:
			user = &pod.Spec.Containers[i]
		}
	}
	require.NotNil(t, fetcher, "fetcher container must be present")
	require.NotNil(t, user, "user (env) container must be present")

	// Fetcher must mount the projected SA token at the canonical k8s path.
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

	// User container must have NO mount at the SA token path. With pod-level
	// AutomountServiceAccountToken=false Kubernetes also stops injecting
	// the implicit mount, so this list should be empty for that path.
	for _, vm := range user.VolumeMounts {
		assert.NotEqual(t, saTokenMountPath, vm.MountPath,
			"user container must not have any volume mount at the SA token path")
	}
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// envBuilderContainerName is the name AddFetcherToPodSpec is called with in
// envwatcher.createBuilderDeployment for the user-supplied builder container.
const envBuilderContainerName = "builder"

// newTestEnvironmentWatcher returns an environmentWatcher wired up just enough
// to exercise createBuilderDeployment in unit tests. The k8s client is a
// fake so Create() is a no-op against in-memory state; createBuilderDeployment
// still returns the constructed *appsv1.Deployment which is what the assertions
// inspect.
func newTestEnvironmentWatcher(t *testing.T) *environmentWatcher {
	t.Helper()
	cfg, err := fetcherConfig.MakeFetcherConfig("/packages")
	require.NoError(t, err)
	return &environmentWatcher{
		logger:           loggerfactory.GetLogger(),
		kubernetesClient: k8sfake.NewSimpleClientset(),
		nsResolver:       utils.DefaultNSResolver(),
		fetcherConfig:    cfg,
		cache:            map[types.UID]*builderInfo{},
	}
}

func newTestBuilderEnv() *fv1.Environment {
	return &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "py",
			Namespace: "default",
		},
		Spec: fv1.EnvironmentSpec{
			Version: 2,
			Runtime: fv1.Runtime{
				Image: "fission/python-env:latest",
			},
			Builder: fv1.Builder{
				Image: "fission/python-builder:latest",
			},
		},
	}
}

// TestBuilderPodSpecDoesNotAutomountTokenInBuilderContainer pins the
// security-advisory-8wcj invariant: the fission-builder SA token is only
// mounted inside the fetcher sidecar, never in the user-supplied builder
// container. See GHSA-8wcj-mfrc-jx5q.
func TestBuilderPodSpecDoesNotAutomountTokenInBuilderContainer(t *testing.T) {
	envw := newTestEnvironmentWatcher(t)
	env := newTestBuilderEnv()

	deployment, err := envw.createBuilderDeployment(context.Background(), env, "default")
	require.NoError(t, err)
	pod := deployment.Spec.Template

	// Pod-level AutomountServiceAccountToken must be explicitly false.
	require.NotNil(t, pod.Spec.AutomountServiceAccountToken,
		"pod-level AutomountServiceAccountToken must be set, not nil")
	assert.False(t, *pod.Spec.AutomountServiceAccountToken,
		"pod-level AutomountServiceAccountToken must be false")

	// Pod still runs as fission-builder so the fetcher container can use
	// its projected token to talk to the API server.
	assert.Equal(t, fv1.FissionBuilderSA, pod.Spec.ServiceAccountName)

	// Projected SA-token volume must exist.
	var projected *apiv1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == util.FetcherSATokenVolumeName {
			projected = &pod.Spec.Volumes[i]
			break
		}
	}
	require.NotNil(t, projected, "projected SA token volume %q must exist", util.FetcherSATokenVolumeName)
	require.NotNil(t, projected.Projected, "%q must be a projected volume", util.FetcherSATokenVolumeName)

	// Locate fetcher + builder containers.
	var fetcher, builder *apiv1.Container
	for i := range pod.Spec.Containers {
		switch pod.Spec.Containers[i].Name {
		case util.FetcherContainerName:
			fetcher = &pod.Spec.Containers[i]
		case envBuilderContainerName:
			builder = &pod.Spec.Containers[i]
		}
	}
	require.NotNil(t, fetcher, "fetcher container must be present")
	require.NotNil(t, builder, "builder container must be present")

	// Fetcher must mount the projected SA token at the canonical k8s path.
	fetcherHasMount := false
	for _, vm := range fetcher.VolumeMounts {
		if vm.MountPath == util.FetcherSATokenMountPath {
			fetcherHasMount = true
			assert.Equal(t, util.FetcherSATokenVolumeName, vm.Name,
				"fetcher SA-token mount must be backed by the projected volume")
			assert.True(t, vm.ReadOnly, "fetcher SA-token mount must be read-only")
		}
	}
	assert.True(t, fetcherHasMount, "fetcher must mount its own SA token")

	// Builder (user) container must have NO mount at the SA-token path.
	for _, vm := range builder.VolumeMounts {
		assert.NotEqual(t, util.FetcherSATokenMountPath, vm.MountPath,
			"builder container must not have any mount at the SA token path")
	}
}

// TestBuilderPodSpecPatchCannotReEnableAutomount asserts that an envw with a
// podSpecPatch that sets AutomountServiceAccountToken=true cannot override the
// invariant. The re-clamp after MergePodSpec is what blocks this. See
// GHSA-8wcj-mfrc-jx5q.
func TestBuilderPodSpecPatchCannotReEnableAutomount(t *testing.T) {
	envw := newTestEnvironmentWatcher(t)
	envw.podSpecPatch = &apiv1.PodSpec{
		AutomountServiceAccountToken: new(true),
	}
	env := newTestBuilderEnv()

	deployment, err := envw.createBuilderDeployment(context.Background(), env, "default")
	require.NoError(t, err)
	pod := deployment.Spec.Template

	require.NotNil(t, pod.Spec.AutomountServiceAccountToken)
	assert.False(t, *pod.Spec.AutomountServiceAccountToken,
		"envw.podSpecPatch must not be able to re-enable auto-mount")
}

// TestBuilderEnvBuilderPodSpecCannotReEnableAutomount asserts that an
// env.Spec.Builder.PodSpec with AutomountServiceAccountToken=true cannot
// override the invariant.
func TestBuilderEnvBuilderPodSpecCannotReEnableAutomount(t *testing.T) {
	envw := newTestEnvironmentWatcher(t)
	env := newTestBuilderEnv()
	env.Spec.Builder.PodSpec = &apiv1.PodSpec{
		AutomountServiceAccountToken: new(true),
	}

	deployment, err := envw.createBuilderDeployment(context.Background(), env, "default")
	require.NoError(t, err)
	pod := deployment.Spec.Template

	require.NotNil(t, pod.Spec.AutomountServiceAccountToken)
	assert.False(t, *pod.Spec.AutomountServiceAccountToken,
		"env.Spec.Builder.PodSpec must not be able to re-enable auto-mount")
}

// TestBuilderEnvBuilderPodSpecCannotIntroduceDuplicateSATokenMount pins the
// invariant from PR #3366 (Copilot Round-3) for the buildermgr path: an env
// author who supplies env.Spec.Builder.PodSpec.Containers = [{name: "fetcher",
// volumeMounts: [{mountPath: <SA token path>}]}] must not cause the final
// fetcher container to end up with two mounts at the SA-token path.
func TestBuilderEnvBuilderPodSpecCannotIntroduceDuplicateSATokenMount(t *testing.T) {
	envw := newTestEnvironmentWatcher(t)
	env := newTestBuilderEnv()
	env.Spec.Builder.PodSpec = &apiv1.PodSpec{
		Containers: []apiv1.Container{
			{
				Name: util.FetcherContainerName,
				VolumeMounts: []apiv1.VolumeMount{
					{
						Name:      "evil-sa-mount",
						MountPath: util.FetcherSATokenMountPath,
					},
				},
			},
		},
	}

	deployment, err := envw.createBuilderDeployment(context.Background(), env, "default")
	require.NoError(t, err)
	pod := deployment.Spec.Template

	var fetcher *apiv1.Container
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == util.FetcherContainerName {
			fetcher = &pod.Spec.Containers[i]
			break
		}
	}
	require.NotNil(t, fetcher, "fetcher container must be present")

	mountsAtSAPath := 0
	var mountVolumeName string
	for _, vm := range fetcher.VolumeMounts {
		if vm.MountPath == util.FetcherSATokenMountPath {
			mountsAtSAPath++
			mountVolumeName = vm.Name
		}
	}
	assert.Equal(t, 1, mountsAtSAPath,
		"fetcher must have exactly one mount at the SA-token path, not duplicates")
	assert.Equal(t, util.FetcherSATokenVolumeName, mountVolumeName,
		"the sole mount at the SA-token path must be the projected volume, not the user-supplied one")
}

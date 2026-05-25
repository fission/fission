// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

const envCName = "newdeploy-test-env"

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
		if pod.Spec.Volumes[i].Name == util.FetcherSATokenVolumeName {
			projected = &pod.Spec.Volumes[i]
			break
		}
	}
	require.NotNil(t, projected, "projected SA token volume %q must exist", util.FetcherSATokenVolumeName)
	require.NotNil(t, projected.Projected, "%q must be a projected volume", util.FetcherSATokenVolumeName)
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
		case util.FetcherContainerName:
			fetcher = &pod.Spec.Containers[i]
		case envCName:
			user = &pod.Spec.Containers[i]
		}
	}
	require.NotNil(t, fetcher, "fetcher container must be present")
	require.NotNil(t, user, "user (env) container must be present")

	hasProjectedTokenMount := false
	for _, vm := range fetcher.VolumeMounts {
		if vm.MountPath == util.FetcherSATokenMountPath {
			hasProjectedTokenMount = true
			assert.Equal(t, util.FetcherSATokenVolumeName, vm.Name,
				"fetcher SA token mount must be backed by the projected volume")
			assert.True(t, vm.ReadOnly, "fetcher SA token mount must be read-only")
		}
	}
	assert.True(t, hasProjectedTokenMount,
		"fetcher must mount its own SA token via projected volume")

	for _, vm := range user.VolumeMounts {
		assert.NotEqual(t, util.FetcherSATokenMountPath, vm.MountPath,
			"user container must not have any volume mount at the SA token path")
	}
}

// TestNewDeployPodSpecRuntimePodSpecCannotReEnableAutomount asserts that an
// environment whose Spec.Runtime.PodSpec sets AutomountServiceAccountToken=true
// cannot override the security-advisory-5 invariant on the new-deploy
// backend. See GHSA-85g2-pmrx-r49q.
func TestNewDeployPodSpecRuntimePodSpecCannotReEnableAutomount(t *testing.T) {
	deploy := newTestNewDeploy(t)
	env := newTestNewDeployEnv()
	env.Spec.Runtime.PodSpec = &apiv1.PodSpec{
		AutomountServiceAccountToken: new(true),
	}
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
		"env.Spec.Runtime.PodSpec must not be able to re-enable auto-mount of the SA token")
}

// TestNewDeployPodSpecRuntimePodSpecCannotIntroduceDuplicateSATokenMount
// pins the Copilot-flagged invariant from PR #3366: an env author who supplies
// env.Spec.Runtime.PodSpec.Containers = [{name: "fetcher", volumeMounts:
// [{mountPath: <SA token path>}]}] must not cause the final fetcher container
// to end up with two mounts at the SA token path, which kubelet would reject
// as a duplicate. The mount-enforcement helper must run AFTER the merge so
// any user-supplied mount at that path is stripped before the projected
// volume is added back. See GHSA-85g2-pmrx-r49q.
func TestNewDeployPodSpecRuntimePodSpecCannotIntroduceDuplicateSATokenMount(t *testing.T) {
	deploy := newTestNewDeploy(t)
	env := newTestNewDeployEnv()
	env.Spec.Runtime.PodSpec = &apiv1.PodSpec{
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
		"fetcher container must have exactly one mount at the SA token path; user-supplied mount at the same path must be stripped by MountFetcherSATokenOnFetcher")
	assert.Equal(t, util.FetcherSATokenVolumeName, mountVolumeName,
		"the surviving mount must be backed by the projected SA token volume, not the user-supplied one")
}

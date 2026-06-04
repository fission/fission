// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/executor/util"
	hpautils "github.com/fission/fission/pkg/executor/util/hpa"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/utils"
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

// TestNewDeployFnDeleteCacheMiss verifies that deleting a function whose fsvc
// is absent from the in-memory cache (never specialized, evicted, or executor
// restarted) does not error out — it must fall back to the deterministic
// computed object name and clean up the backing objects instead of leaking
// them.
func TestNewDeployFnDeleteCacheMiss(t *testing.T) {
	logger := loggerfactory.GetLogger()
	deploy := &NewDeploy{
		logger:           logger,
		kubernetesClient: fake.NewSimpleClientset(),
		fsCache:          fscache.MakeFunctionServiceCache(logger),
		nsResolver:       utils.DefaultNSResolver(),
		hpaops:           hpautils.NewHpaOperations(logger, fake.NewSimpleClientset(), "test-instance"),
	}

	fn := newTestNewDeployFunction()
	fn.UID = "abcdef01-2345-6789-abcd-ef0123456789"

	// fsCache is intentionally left empty so GetByFunctionUID misses.
	require.NoError(t, deploy.fnDelete(t.Context(), fn),
		"fnDelete must tolerate a cache miss and clean up by computed name")
}

// TestNewDeployFnCreateDeletionGuard verifies the authoritative re-read at the
// top of fnCreate refuses to create backing objects for a Function that the
// router presented but which is gone or being deleted in the cluster. Without
// this guard an in-flight create can race the delete teardown and re-create the
// Deployment/Service/HPA after teardown removed them, leaking objects whose
// owning Function CR is already gone.
func TestNewDeployFnCreateDeletionGuard(t *testing.T) {
	t.Parallel()

	const liveUID = "abcdef01-2345-6789-abcd-ef0123456789"

	tests := []struct {
		name string
		// stored is the Function in the authoritative store (fake
		// fissionClient); nil means the function is absent.
		stored *fv1.Function
	}{
		{
			name:   "function absent from fissionClient",
			stored: nil,
		},
		{
			name: "function present but being deleted",
			stored: func() *fv1.Function {
				fn := newTestNewDeployFunction()
				fn.UID = liveUID
				fn.DeletionTimestamp = &metav1.Time{Time: time.Now()}
				fn.Finalizers = []string{"fission.io/test"}
				return fn
			}(),
		},
		{
			name: "function present but with a different UID",
			stored: func() *fv1.Function {
				fn := newTestNewDeployFunction()
				fn.UID = "00000000-0000-0000-0000-000000000000"
				return fn
			}(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger := loggerfactory.GetLogger()
			kubeClient := fake.NewSimpleClientset()
			fissionClient := fissionfake.NewSimpleClientset()
			if tc.stored != nil {
				fissionClient = fissionfake.NewSimpleClientset(tc.stored)
			}
			deploy := &NewDeploy{
				logger:           logger,
				kubernetesClient: kubeClient,
				fissionClient:    fissionClient,
				fsCache:          fscache.MakeFunctionServiceCache(logger),
				nsResolver:       utils.DefaultNSResolver(),
				hpaops:           hpautils.NewHpaOperations(logger, kubeClient, "test-instance"),
			}

			// The Function the router presents looks alive (no DeletionTimestamp).
			fn := newTestNewDeployFunction()
			fn.UID = liveUID

			_, err := deploy.fnCreate(t.Context(), fn)
			require.Error(t, err, "fnCreate must refuse a gone/deleting function")
			assert.True(t, ferror.IsNotFound(err),
				"guard must surface a ferror NotFound, got: %v", err)

			deploys, listErr := kubeClient.AppsV1().Deployments(metav1.NamespaceAll).List(t.Context(), metav1.ListOptions{})
			require.NoError(t, listErr)
			assert.Empty(t, deploys.Items,
				"no Deployment must be created when the function is gone/deleting")
		})
	}
}

// TestNewDeployFnCreateGuardPass verifies the guard lets a live, matching
// Function proceed past the re-read. We do not stand up a full happy-path
// (Environment, package, pods); proving the guard passed is enough: fnCreate
// fails later on the missing Environment with a non-NotFound error (a k8s
// NotFound from the apiserver lookup, which is not a ferror NotFound).
func TestNewDeployFnCreateGuardPass(t *testing.T) {
	t.Parallel()

	live := newTestNewDeployFunction()
	live.UID = "abcdef01-2345-6789-abcd-ef0123456789"

	logger := loggerfactory.GetLogger()
	kubeClient := fake.NewSimpleClientset()
	deploy := &NewDeploy{
		logger:           logger,
		kubernetesClient: kubeClient,
		fissionClient:    fissionfake.NewSimpleClientset(live),
		fsCache:          fscache.MakeFunctionServiceCache(logger),
		nsResolver:       utils.DefaultNSResolver(),
		hpaops:           hpautils.NewHpaOperations(logger, kubeClient, "test-instance"),
	}

	fn := newTestNewDeployFunction()
	fn.UID = live.UID

	_, err := deploy.fnCreate(t.Context(), fn)
	require.Error(t, err, "expected fnCreate to fail past the guard on the missing Environment")
	assert.False(t, ferror.IsNotFound(err),
		"guard must have passed; the error should come from the missing Environment lookup, not the guard. got: %v", err)
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

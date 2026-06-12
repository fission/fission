// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// TestGenDeploymentSpecPreStopLifecycle asserts that genDeploymentSpec sets a
// kubelet-native Sleep preStop hook on the runtime container for a positive
// TerminationGracePeriod, and sets nil Lifecycle when the grace period is 0
// (no drain window needed, and Kubernetes rejects Sleep.Seconds < 1).
func TestGenDeploymentSpecPreStopLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("positive grace period uses native sleep", func(t *testing.T) {
		t.Parallel()
		gp := newTestGenericPool(t)
		env := newTestEnv()
		env.Spec.TerminationGracePeriod = 360

		deploymentSpec, err := gp.genDeploymentSpec(env)
		require.NoError(t, err)

		// Locate the runtime (user) container — it carries the preStop hook.
		var runtimeContainer *apiv1.Container
		for i := range deploymentSpec.Template.Spec.Containers {
			if deploymentSpec.Template.Spec.Containers[i].Name == envContainerName {
				runtimeContainer = &deploymentSpec.Template.Spec.Containers[i]
				break
			}
		}
		require.NotNil(t, runtimeContainer, "runtime container must be present in the deployment spec")
		require.NotNil(t, runtimeContainer.Lifecycle, "runtime container must have a Lifecycle set")
		require.NotNil(t, runtimeContainer.Lifecycle.PreStop, "runtime container Lifecycle.PreStop must be set")

		preStop := runtimeContainer.Lifecycle.PreStop
		assert.Nil(t, preStop.Exec, "PreStop.Exec must be nil — no /bin/sleep exec")
		require.NotNil(t, preStop.Sleep, "PreStop.Sleep must be set for kubelet-native drain")
		assert.Equal(t, int64(360), preStop.Sleep.Seconds, "PreStop.Sleep.Seconds must equal the environment TerminationGracePeriod")
	})

	t.Run("zero grace period produces nil lifecycle", func(t *testing.T) {
		t.Parallel()
		gp := newTestGenericPool(t)
		env := newTestEnv()
		env.Spec.TerminationGracePeriod = 0

		deploymentSpec, err := gp.genDeploymentSpec(env)
		require.NoError(t, err)

		var runtimeContainer *apiv1.Container
		for i := range deploymentSpec.Template.Spec.Containers {
			if deploymentSpec.Template.Spec.Containers[i].Name == envContainerName {
				runtimeContainer = &deploymentSpec.Template.Spec.Containers[i]
				break
			}
		}
		require.NotNil(t, runtimeContainer, "runtime container must be present in the deployment spec")
		assert.Nil(t, runtimeContainer.Lifecycle,
			"runtime container Lifecycle must be nil when TerminationGracePeriod is 0 (no drain window, Sleep.Seconds>=1 is required by the API)")
	})
}

const envContainerName = "test-env"

func TestGetPoolName(t *testing.T) {
	longEnv := &fv1.Environment{
		TypeMeta: metav1.TypeMeta{
			Kind:       fv1.CRD_NAME_ENVIRONMENT,
			APIVersion: fv1.CRD_VERSION,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            "justtryingtoincreasethenumberofcharactersinthisstring",
			Namespace:       "checkingifthegetpoolfunctionworkswithcharactersmorethan18",
			ResourceVersion: "2518",
		},
	}
	shortEnv := &fv1.Environment{
		TypeMeta: metav1.TypeMeta{
			Kind:       fv1.CRD_NAME_ENVIRONMENT,
			APIVersion: fv1.CRD_VERSION,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test",
			Namespace:       "testns",
			ResourceVersion: "2517",
		},
	}
	tests := []struct {
		name      string
		env       *fv1.Environment
		imageHash string
		want      string
	}{
		{"Under character limit", shortEnv, "", "poolmgr-test-testns-2517"},
		{"Over character limit", longEnv, "", "poolmgr-justtryingtoincrea-checkingifthegetpo-2518"},
		{"Per-image pool suffix", shortEnv, "abcdef0123456789", "poolmgr-test-testns-abcdef01-2517"},
		{"Per-image pool over character limit", longEnv, "abcdef0123456789", "poolmgr-justtryingtoi-checkingifthe-abcdef01-2518"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getPoolName(tt.env, tt.imageHash)
			if got != tt.want {
				t.Errorf("getPoolName() = %s, want = %s len(getPoolName()) = %x len(want) = %x", got, tt.want, len(got), len(tt.want))
			}
			if len(got) > 63 {
				t.Errorf("getPoolName() = %s is %d chars, over the 63-char DNS label limit", got, len(got))
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

	// Locate the fetcher and user containers.
	var fetcher, user *apiv1.Container
	for i := range pod.Spec.Containers {
		switch pod.Spec.Containers[i].Name {
		case util.FetcherContainerName:
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
		if vm.MountPath == util.FetcherSATokenMountPath {
			hasProjectedTokenMount = true
			assert.Equal(t, util.FetcherSATokenVolumeName, vm.Name,
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
		assert.NotEqual(t, util.FetcherSATokenMountPath, vm.MountPath,
			"user container must not have any volume mount at the SA token path")
	}
}

// TestGenericPoolPodSpecRuntimePodSpecCannotReEnableAutomount asserts that an
// environment whose Spec.Runtime.PodSpec sets AutomountServiceAccountToken=true
// cannot override the security-advisory-5 invariant. The merge step must not
// be allowed to re-introduce the implicit SA token mount on the user
// container. See GHSA-85g2-pmrx-r49q.
func TestGenericPoolPodSpecRuntimePodSpecCannotReEnableAutomount(t *testing.T) {
	gp := newTestGenericPool(t)
	env := newTestEnv()
	env.Spec.Runtime.PodSpec = &apiv1.PodSpec{
		AutomountServiceAccountToken: new(true),
	}

	deploymentSpec, err := gp.genDeploymentSpec(env)
	require.NoError(t, err)
	pod := deploymentSpec.Template

	require.NotNil(t, pod.Spec.AutomountServiceAccountToken,
		"pod-level AutomountServiceAccountToken must be explicitly set, not nil")
	assert.False(t, *pod.Spec.AutomountServiceAccountToken,
		"env.Spec.Runtime.PodSpec must not be able to re-enable auto-mount of the SA token")
}

// TestGenericPoolPodSpecRuntimePodSpecCannotIntroduceDuplicateSATokenMount
// pins the Copilot-flagged invariant from PR #3366: an env author who supplies
// env.Spec.Runtime.PodSpec.Containers = [{name: "fetcher", volumeMounts:
// [{mountPath: <SA token path>}]}] must not cause the final fetcher container
// to end up with two mounts at the SA token path, which kubelet would reject
// as a duplicate. The mount-enforcement helper must run AFTER the merge so
// any user-supplied mount at that path is stripped before the projected
// volume is added back. See GHSA-85g2-pmrx-r49q.
func TestGenericPoolPodSpecRuntimePodSpecCannotIntroduceDuplicateSATokenMount(t *testing.T) {
	gp := newTestGenericPool(t)
	env := newTestEnv()
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

	deploymentSpec, err := gp.genDeploymentSpec(env)
	require.NoError(t, err)
	pod := deploymentSpec.Template

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

// newTestOCIPool returns a GenericPool configured as a per-image image-volume
// pool (RFC-0001 Path B, fetcherless B-direct variant).
func newTestOCIPool(t *testing.T, oci *fv1.OCIArchive) *GenericPool {
	t.Helper()
	gp := newTestGenericPool(t)
	gp.oci = oci
	gp.ociImageHash = ociPoolHash(&ociPoolSpec{archive: oci})
	return gp
}

// newTestOCIFetcherPool returns a GenericPool configured as the
// fetcher-retained Path B variant (RFC-0012 B-fetcher).
func newTestOCIFetcherPool(t *testing.T, oci *fv1.OCIArchive) *GenericPool {
	t.Helper()
	gp := newTestGenericPool(t)
	gp.oci = oci
	gp.ociFetcherVariant = true
	gp.ociImageHash = ociPoolHash(&ociPoolSpec{archive: oci, fetcherVariant: true})
	return gp
}

// TestGenDeploymentSpecOCIImageVolume asserts the shape of a Path B pool pod:
// no fetcher container, the code mounted read-only from an image volume at
// the shared mount path, pull secrets propagated, and the
// AutomountServiceAccountToken=false invariant intact.
func TestGenDeploymentSpecOCIImageVolume(t *testing.T) {
	t.Parallel()
	oci := &fv1.OCIArchive{
		Image:            "registry.example.com/code/hello:v1",
		SubPath:          "app",
		ImagePullSecrets: []apiv1.LocalObjectReference{{Name: "regcred"}},
	}
	gp := newTestOCIPool(t, oci)
	env := newTestEnv()
	env.Spec.Version = 2

	deploymentSpec, err := gp.genDeploymentSpec(env)
	require.NoError(t, err)
	pod := deploymentSpec.Template

	// Exactly one container: the env runtime. No fetcher.
	require.Len(t, pod.Spec.Containers, 1, "Path B pods must not carry a fetcher container")
	user := pod.Spec.Containers[0]
	assert.Equal(t, envContainerName, user.Name)

	// The image volume holds the code.
	var imgVol *apiv1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Image != nil {
			imgVol = &pod.Spec.Volumes[i]
			break
		}
	}
	require.NotNil(t, imgVol, "an image volume must be present")
	assert.Equal(t, oci.Image, imgVol.Image.Reference)
	assert.Equal(t, apiv1.PullIfNotPresent, imgVol.Image.PullPolicy)

	// No fetcher SA token projected volume — there is no fetcher to use it.
	for _, v := range pod.Spec.Volumes {
		assert.NotEqual(t, util.FetcherSATokenVolumeName, v.Name,
			"Path B pods must not carry the fetcher SA token volume")
	}

	// Mounted read-only at the fetcher's store path — the exact path the
	// load request names (LoadReq.FilePath = <sharedMountPath>/deployarchive)
	// — with the sub-path applied.
	var mount *apiv1.VolumeMount
	for i := range user.VolumeMounts {
		if user.VolumeMounts[i].Name == imgVol.Name {
			mount = &user.VolumeMounts[i]
			break
		}
	}
	require.NotNil(t, mount, "user container must mount the image volume")
	assert.Equal(t, gp.fetcherConfig.SharedMountPath()+"/"+fetcherConfig.TargetFilenameDeployArchive, mount.MountPath)
	assert.Equal(t, "app", mount.SubPath)
	assert.True(t, mount.ReadOnly, "image volume mount must be read-only")

	// Pull secrets reach the pod so the kubelet can pull the image volume.
	assert.Contains(t, pod.Spec.ImagePullSecrets, apiv1.LocalObjectReference{Name: "regcred"})

	// Security invariant unchanged.
	require.NotNil(t, pod.Spec.AutomountServiceAccountToken)
	assert.False(t, *pod.Spec.AutomountServiceAccountToken)

	// The pod labels carry the image hash so the reconciler routes warm pods
	// to this pool's queue.
	assert.Equal(t, gp.ociImageHash, pod.Labels[fv1.POOL_OCI_IMAGE_HASH])
	assert.Equal(t, gp.ociImageHash, deploymentSpec.Selector.MatchLabels[fv1.POOL_OCI_IMAGE_HASH])
}

// TestGenDeploymentSpecOCIWithPodSpecPatch asserts the pod-spec invariants are
// re-clamped after every merge on the Path B branch too: a runtime pod spec
// cannot re-enable SA token automount, and the image volume survives.
func TestGenDeploymentSpecOCIWithPodSpecPatch(t *testing.T) {
	t.Parallel()
	gp := newTestOCIPool(t, &fv1.OCIArchive{Image: "registry.example.com/code/hello:v1"})
	env := newTestEnv()
	env.Spec.Version = 2
	env.Spec.Runtime.PodSpec = &apiv1.PodSpec{
		AutomountServiceAccountToken: new(true),
	}

	deploymentSpec, err := gp.genDeploymentSpec(env)
	require.NoError(t, err)
	pod := deploymentSpec.Template

	require.NotNil(t, pod.Spec.AutomountServiceAccountToken)
	assert.False(t, *pod.Spec.AutomountServiceAccountToken,
		"runtime pod spec must not re-enable SA token automount on Path B pods")

	found := false
	for _, v := range pod.Spec.Volumes {
		if v.Image != nil {
			found = true
		}
	}
	assert.True(t, found, "the image volume must survive the pod-spec merge")
}

// TestGenDeploymentSpecNonOCIUnchanged is the parity guard: a plain pool's
// deployment spec keeps the pre-Path-B layout invariants — the fetcher
// container, its SA token projected volume, and no image volume.
func TestGenDeploymentSpecNonOCIUnchanged(t *testing.T) {
	t.Parallel()
	gp := newTestGenericPool(t)
	env := newTestEnv()

	deploymentSpec, err := gp.genDeploymentSpec(env)
	require.NoError(t, err)
	pod := deploymentSpec.Template

	names := make([]string, 0, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	assert.Contains(t, names, util.FetcherContainerName, "plain pools keep the fetcher container")

	hasSATokenVolume, hasImageVolume := false, false
	for _, v := range pod.Spec.Volumes {
		if v.Name == util.FetcherSATokenVolumeName {
			hasSATokenVolume = true
		}
		if v.Image != nil {
			hasImageVolume = true
		}
	}
	assert.True(t, hasSATokenVolume, "plain pools keep the fetcher SA token volume")
	assert.False(t, hasImageVolume, "plain pools must not grow an image volume")
	assert.NotContains(t, pod.Labels, fv1.POOL_OCI_IMAGE_HASH)
}

// TestGenDeploymentSpecOCIFetcherVariant asserts the shape of a B-fetcher
// pool pod (RFC-0012): the fetcher sidecar is retained (with its SA token
// projected volume and re-mount), and the image volume is mounted read-only
// at the fetcher's store path on BOTH containers so the fetcher's
// exists-early-exit skips the pull — newdeploy's shipped Path B pattern.
func TestGenDeploymentSpecOCIFetcherVariant(t *testing.T) {
	t.Parallel()
	oci := &fv1.OCIArchive{
		Image:            "registry.example.com/code/hello:v1",
		ImagePullSecrets: []apiv1.LocalObjectReference{{Name: "regcred"}},
	}
	gp := newTestOCIFetcherPool(t, oci)
	env := newTestEnv()
	env.Spec.Version = 2

	deploymentSpec, err := gp.genDeploymentSpec(env)
	require.NoError(t, err)
	pod := deploymentSpec.Template

	// Two containers: env runtime + fetcher.
	require.Len(t, pod.Spec.Containers, 2, "B-fetcher pods must keep the fetcher sidecar")
	names := []string{pod.Spec.Containers[0].Name, pod.Spec.Containers[1].Name}
	assert.Contains(t, names, envContainerName)
	assert.Contains(t, names, util.FetcherContainerName)

	// The image volume is mounted at the fetcher store path on BOTH
	// containers (the fetcher needs it for the exists-early-exit; the
	// runtime needs it for the load).
	var imgVol *apiv1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Image != nil {
			imgVol = &pod.Spec.Volumes[i]
			break
		}
	}
	require.NotNil(t, imgVol, "an image volume must be present")
	wantPath := gp.fetcherConfig.SharedMountPath() + "/" + fetcherConfig.TargetFilenameDeployArchive
	for _, c := range pod.Spec.Containers {
		var mount *apiv1.VolumeMount
		for i := range c.VolumeMounts {
			if c.VolumeMounts[i].Name == imgVol.Name {
				mount = &c.VolumeMounts[i]
				break
			}
		}
		require.NotNilf(t, mount, "container %s must mount the image volume", c.Name)
		assert.Equal(t, wantPath, mount.MountPath)
		assert.True(t, mount.ReadOnly)
	}

	// The fetcher SA token projected volume IS present (unlike B-direct) and
	// the fetcher container re-mounts it (GHSA-85g2-pmrx-r49q invariant).
	foundSAVol := false
	for _, v := range pod.Spec.Volumes {
		if v.Name == util.FetcherSATokenVolumeName {
			foundSAVol = true
		}
	}
	assert.True(t, foundSAVol, "B-fetcher pods must carry the fetcher SA token volume")

	// SA automount stays off pod-wide.
	require.NotNil(t, pod.Spec.AutomountServiceAccountToken)
	assert.False(t, *pod.Spec.AutomountServiceAccountToken)

	// Pull secrets reach the pod for the kubelet image-volume pull.
	assert.Contains(t, pod.Spec.ImagePullSecrets, apiv1.LocalObjectReference{Name: "regcred"})

	// Pod labels carry the (variant-specific) image hash.
	assert.Equal(t, gp.ociImageHash, pod.Labels[fv1.POOL_OCI_IMAGE_HASH])
}

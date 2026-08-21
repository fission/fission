// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// envBuilderContainerName is the name AddFetcherToPodSpec is called with in
// EnvironmentReconciler.createBuilderDeployment for the user-supplied builder
// container.
const envBuilderContainerName = "builder"

// newTestEnvironmentWatcher returns an EnvironmentReconciler wired up just
// enough to exercise createBuilderDeployment in unit tests. The k8s client is a
// fake so Create() is a no-op against in-memory state; createBuilderDeployment
// still returns the constructed *appsv1.Deployment which is what the assertions
// inspect.
func newTestEnvironmentWatcher(t *testing.T) *EnvironmentReconciler {
	t.Helper()
	cfg, err := fetcherConfig.MakeFetcherConfig("/packages")
	require.NoError(t, err)
	return &EnvironmentReconciler{
		logger:           loggerfactory.GetLogger(),
		kubernetesClient: k8sfake.NewClientset(),
		nsResolver:       utils.DefaultNSResolver(),
		fetcherConfig:    cfg,
	}
}

func newTestBuilderEnv() *fv1.Environment {
	return &fv1.Environment{
		Name:      "py",
		Namespace: "default",
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

// newTestEnvironmentReconciler wires an EnvironmentReconciler with a
// controller-runtime fake client seeded with crObjs (for the primary
// Environment Get) and a fake Kubernetes client seeded with k8sObjs (the
// builder Service/Deployment store).
func newTestEnvironmentReconciler(t *testing.T, k8sObjs []runtime.Object, crObjs ...client.Object) *EnvironmentReconciler {
	t.Helper()
	cfg, err := fetcherConfig.MakeFetcherConfig("/packages")
	require.NoError(t, err)
	return &EnvironmentReconciler{
		logger:           loggerfactory.GetLogger(),
		client:           fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(crObjs...).Build(),
		kubernetesClient: k8sfake.NewClientset(k8sObjs...),
		nsResolver:       utils.DefaultNSResolver(),
		fetcherConfig:    cfg,
	}
}

func envReq(env *fv1.Environment) ctrl.Request {
	return ctrl.Request{Name: env.Name, Namespace: env.Namespace}
}

// TestEnvironmentReconcileCreateIdempotentDelete walks the builder lifecycle:
// a v2 env with a builder image creates the builder Service+Deployment, a
// repeated reconcile is idempotent, and deleting the env tears them down.
func TestEnvironmentReconcileCreateIdempotentDelete(t *testing.T) {
	env := newTestBuilderEnv()
	env.ResourceVersion = "7"
	r := newTestEnvironmentReconciler(t, nil, env)
	ns := r.nsResolver.GetBuilderNS(env.Namespace)
	name := env.Name + "-" + env.ResourceVersion

	_, err := r.Reconcile(t.Context(), envReq(env))
	require.NoError(t, err)
	_, err = r.kubernetesClient.CoreV1().Services(ns).Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err, "builder service must be created")
	_, err = r.kubernetesClient.AppsV1().Deployments(ns).Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err, "builder deployment must be created")

	// Idempotent: a second reconcile neither errors nor duplicates.
	_, err = r.Reconcile(t.Context(), envReq(env))
	require.NoError(t, err)
	svcs, err := r.kubernetesClient.CoreV1().Services(ns).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, svcs.Items, 1, "reconcile must not duplicate the builder service")

	// Delete the env → the NotFound reconcile path tears the builder down.
	require.NoError(t, r.client.Delete(t.Context(), env))
	_, err = r.Reconcile(t.Context(), envReq(env))
	require.NoError(t, err)
	svcs, err = r.kubernetesClient.CoreV1().Services(ns).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, svcs.Items, "builder service must be removed on env delete")
	deps, err := r.kubernetesClient.AppsV1().Deployments(ns).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, deps.Items, "builder deployment must be removed on env delete")
}

// TestEnvironmentReconcileV1IsNoop pins that a v1-interface environment (no
// builder support) creates nothing.
func TestEnvironmentReconcileV1IsNoop(t *testing.T) {
	env := newTestBuilderEnv()
	env.Spec.Version = 1
	r := newTestEnvironmentReconciler(t, nil, env)
	ns := r.nsResolver.GetBuilderNS(env.Namespace)

	_, err := r.Reconcile(t.Context(), envReq(env))
	require.NoError(t, err)
	svcs, err := r.kubernetesClient.CoreV1().Services(ns).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, svcs.Items, "a v1 environment must not create a builder")
}

// TestEnvironmentReconcilePrunesStaleGeneration verifies that builder objects
// left over from a previous Environment generation (a different
// envResourceVersion label) are deleted while the current-generation pair is
// (re)created.
func TestEnvironmentReconcilePrunesStaleGeneration(t *testing.T) {
	env := newTestBuilderEnv()
	env.ResourceVersion = "2"
	r := newTestEnvironmentReconciler(t, nil, env)
	ns := r.nsResolver.GetBuilderNS(env.Namespace)

	staleLabels := map[string]string{
		LABEL_DEPLOYMENT_OWNER:    BUILDER_MGR,
		LABEL_ENV_NAME:            env.Name,
		LABEL_ENV_NAMESPACE:       ns,
		LABEL_ENV_RESOURCEVERSION: "1",
	}
	_, err := r.kubernetesClient.CoreV1().Services(ns).Create(t.Context(),
		&apiv1.Service{Name: env.Name + "-1", Namespace: ns, Labels: staleLabels}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = r.kubernetesClient.AppsV1().Deployments(ns).Create(t.Context(),
		&appsv1.Deployment{Name: env.Name + "-1", Namespace: ns, Labels: staleLabels}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = r.Reconcile(t.Context(), envReq(env))
	require.NoError(t, err)

	_, err = r.kubernetesClient.CoreV1().Services(ns).Get(t.Context(), env.Name+"-1", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "stale-generation builder service must be pruned")
	_, err = r.kubernetesClient.AppsV1().Deployments(ns).Get(t.Context(), env.Name+"-1", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "stale-generation builder deployment must be pruned")
	_, err = r.kubernetesClient.CoreV1().Services(ns).Get(t.Context(), env.Name+"-2", metav1.GetOptions{})
	require.NoError(t, err, "current-generation builder service must exist")
}

// TestEnvironmentReconcileRollsDriftedBuilder is issue #776: builder drift that
// does NOT bump the Environment's ResourceVersion must still roll the builder.
//
// Everything the builder pod is made of that comes from the chart rather than
// the CR — the fetcher image, the builder image-pull policy, the pod-spec patch
// — changes without the Environment being touched. The name and selector are
// keyed on the RV, so they still match, and the reconciler used to find a
// Deployment and do nothing. The builder then ran the previous image until
// someone deleted the Deployment by hand; deleting the POD did nothing, because
// the ReplicaSet recreated it from the same stale template.
func TestEnvironmentReconcileRollsDriftedBuilder(t *testing.T) {
	env := newTestBuilderEnv()
	env.ResourceVersion = "7"
	r := newTestEnvironmentReconciler(t, nil, env)
	ns := r.nsResolver.GetBuilderNS(env.Namespace)

	// First reconcile creates the builder at the current desired spec.
	_, err := r.Reconcile(t.Context(), envReq(env))
	require.NoError(t, err)

	name := env.Name + "-7"
	created, err := r.kubernetesClient.AppsV1().Deployments(ns).Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	originalHash := created.Annotations[builderSpecHashAnnotation]
	require.NotEmpty(t, originalHash, "the created builder must carry a spec hash to compare against later")

	// Drift the live Deployment the way a chart-level change would: the pod
	// template no longer matches what the reconciler would produce, while the
	// Environment — and therefore the name, selector and RV label — is untouched.
	drifted := created.DeepCopy()
	drifted.Spec.Template.Spec.Containers[0].Image = "fission/python-builder:STALE"
	drifted.Annotations[builderSpecHashAnnotation] = "sha256:stale"
	_, err = r.kubernetesClient.AppsV1().Deployments(ns).Update(t.Context(), drifted, metav1.UpdateOptions{})
	require.NoError(t, err)

	_, err = r.Reconcile(t.Context(), envReq(env))
	require.NoError(t, err)

	got, err := r.kubernetesClient.AppsV1().Deployments(ns).Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, env.Spec.Builder.Image, got.Spec.Template.Spec.Containers[0].Image,
		"a drifted builder must be rolled back onto the desired image")
	assert.Equal(t, originalHash, got.Annotations[builderSpecHashAnnotation],
		"the spec hash must be re-stamped so the next reconcile is a no-op")
	assert.Equal(t, name, got.Name,
		"the Deployment must be updated in place: buildPackage resolves <env>-<rv> as a DNS hostname")
}

// TestEnvironmentReconcileDoesNotRollAnUndriftedBuilder is the other half, and
// the direction whose failure mode is silent: a reconciler that rewrote the
// Deployment every pass would roll every builder on every resync forever.
// Comparing the apiserver's view of the PodSpec would do exactly that, since it
// defaults fields we never set — hence hashing our own desired object instead.
func TestEnvironmentReconcileDoesNotRollAnUndriftedBuilder(t *testing.T) {
	env := newTestBuilderEnv()
	env.ResourceVersion = "7"
	r := newTestEnvironmentReconciler(t, nil, env)
	ns := r.nsResolver.GetBuilderNS(env.Namespace)

	_, err := r.Reconcile(t.Context(), envReq(env))
	require.NoError(t, err)
	_, err = r.kubernetesClient.AppsV1().Deployments(ns).Get(t.Context(), env.Name+"-7", metav1.GetOptions{})
	require.NoError(t, err)

	// Count Deployment writes with a reactor rather than comparing
	// ResourceVersion: the fake clientset does not bump RV on Update, so an
	// RV comparison passes even when the reconciler rewrites the object on
	// every pass — the exact regression this test exists to catch.
	var updates int
	fakeCS, ok := r.kubernetesClient.(*k8sfake.Clientset)
	require.True(t, ok, "test reconciler must use the fake clientset")
	fakeCS.PrependReactor("update", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		return false, nil, nil
	})

	for range 3 {
		_, err = r.Reconcile(t.Context(), envReq(env))
		require.NoError(t, err)
	}

	assert.Zero(t, updates,
		"repeated reconciles of an unchanged environment must not write the Deployment at all")
}

// TestEnvironmentReconcileDoesNotRollWhenPodSpecPatched is the case the drift
// check exists for, and the one that breaks a naive hash.
//
// env.Spec.Builder.PodSpec routes the pod template through MergePodSpec, whose
// mergeContainerList round-trips containers through a Go map and emits them in
// map-iteration order — randomized per range, even inside one process. Hashing
// that order directly yields a different hash on every call, so the reconciler
// would "detect drift" forever and roll the builder on every single reconcile:
// the exact churn this feature exists to prevent, arriving from the other side.
func TestEnvironmentReconcileDoesNotRollWhenPodSpecPatched(t *testing.T) {
	env := newTestBuilderEnv()
	env.ResourceVersion = "7"
	// Container-free on purpose: mergeContainerList runs regardless of whether
	// the patch names any container, so an unrelated patch is enough.
	env.Spec.Builder.PodSpec = &apiv1.PodSpec{
		NodeSelector: map[string]string{"disktype": "ssd"},
	}
	r := newTestEnvironmentReconciler(t, nil, env)

	_, err := r.Reconcile(t.Context(), envReq(env))
	require.NoError(t, err)

	var updates int
	fakeCS, ok := r.kubernetesClient.(*k8sfake.Clientset)
	require.True(t, ok)
	fakeCS.PrependReactor("update", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		return false, nil, nil
	})

	// Several passes: one map iteration can coincidentally match, a run of them
	// will not.
	for range 8 {
		_, err = r.Reconcile(t.Context(), envReq(env))
		require.NoError(t, err)
	}

	assert.Zero(t, updates,
		"a builder whose environment sets Builder.PodSpec must not be rolled by repeated reconciles")
}

// TestBuilderSpecHashIsStableAcrossCalls isolates the same invariant without a
// reconcile, so a failure points at the hash rather than at the controller.
func TestBuilderSpecHashIsStableAcrossCalls(t *testing.T) {
	env := newTestBuilderEnv()
	env.ResourceVersion = "7"
	env.Spec.Builder.PodSpec = &apiv1.PodSpec{
		NodeSelector: map[string]string{"disktype": "ssd"},
	}
	r := newTestEnvironmentReconciler(t, nil, env)
	ns := r.nsResolver.GetBuilderNS(env.Namespace)

	first, err := r.genBuilderDeployment(env, ns)
	require.NoError(t, err)
	want := first.Annotations[builderSpecHashAnnotation]
	require.NotEmpty(t, want)

	for i := range 20 {
		again, err := r.genBuilderDeployment(env, ns)
		require.NoError(t, err)
		require.Equalf(t, want, again.Annotations[builderSpecHashAnnotation],
			"hash must not depend on map iteration order (differed on call %d)", i+2)
	}
}

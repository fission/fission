// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/utils"
)

// specializedAdoptPod builds a ready, specialized (managed=false) poolmgr pod
// carrying the labels/annotations adoptSpecializedPods reads to re-register a
// function service into the fsCache after an executor restart. generation, if
// non-empty, is stamped as the fv1.FUNCTION_GENERATION label (set on real pods
// at specialization time — gp_pod.go's specializedPodLabels).
func specializedAdoptPod(name, ns, fnUID, generation string) *apiv1.Pod {
	labels := map[string]string{
		fv1.EXECUTOR_TYPE:         string(fv1.ExecutorTypePoolmgr),
		"managed":                 "false",
		fv1.FUNCTION_NAME:         "fn1",
		fv1.FUNCTION_NAMESPACE:    ns,
		fv1.FUNCTION_UID:          fnUID,
		fv1.ENVIRONMENT_NAME:      "env1",
		fv1.ENVIRONMENT_NAMESPACE: ns,
	}
	if generation != "" {
		labels[fv1.FUNCTION_GENERATION] = generation
	}
	return &apiv1.Pod{
		Name:      name,
		Namespace: ns,
		Labels:    labels,
		Annotations: map[string]string{
			fv1.FUNCTION_RESOURCE_VERSION: "100",
			fv1.ANNOTATION_SVC_HOST:       "10.9.9.9:8888",
		},
		Status: apiv1.PodStatus{
			PodIP:             "10.9.9.9",
			ContainerStatuses: []apiv1.ContainerStatus{{Ready: true}},
		},
	}
}

// runAdopt wires a GenericPoolManager against a fake kubernetesClient seeded
// with pod (and a fake fissionClient seeded with fissionObjs, for the
// missing-generation-label fallback that fetches the live Function), runs
// adoptSpecializedPods to completion, and returns the fsCache it populated.
func runAdopt(t *testing.T, pod *apiv1.Pod, fissionObjs ...runtime.Object) *fscache.FunctionServiceCache {
	t.Helper()
	env := fv1.Environment{Name: "env1", Namespace: pod.Namespace}
	return runAdoptWithEnv(t, pod, env, fissionObjs...).fsCache
}

// runAdoptWithEnv is runAdopt with a caller-supplied Environment (for the
// UID/runtime-hash guards) and the manager returned for stamp assertions.
func runAdoptWithEnv(t *testing.T, pod *apiv1.Pod, env fv1.Environment, fissionObjs ...runtime.Object) *GenericPoolManager {
	t.Helper()
	gpm := &GenericPoolManager{
		logger:           logr.Discard(),
		kubernetesClient: k8sfake.NewSimpleClientset(pod),
		fissionClient:    fissionfake.NewSimpleClientset(fissionObjs...),
		nsResolver:       utils.DefaultNSResolver(),
		instanceID:       "inst-1",
		fsCache:          fscache.MakeFunctionServiceCache(logr.Discard()),
	}
	envMap := map[string]fv1.Environment{pod.Namespace + "/env1": env}
	var wg sync.WaitGroup
	gpm.adoptSpecializedPods(t.Context(), &wg, envMap)
	wg.Wait()
	return gpm
}

// TestAdoptSpecializedPodsPopulatesGeneration is the regression test for the
// synthetic ObjectMeta adoptSpecializedPods builds from pod labels/annotations:
// it must carry Generation (read from the fv1.FUNCTION_GENERATION pod label)
// so the resulting fsCache entry is keyed the same way (crd.CacheKeyUG =
// UID+Generation) as a live Function's GetByFunction lookup. Pre-migration,
// the synthetic ObjectMeta's zero-value Generation keyed the entry as
// (UID, 0), which no live Function (Generation >= 1) could ever match —
// RefreshFuncPods' GetByFunction would deterministically miss adopted pods,
// leaving stale byFunction/byAddress entries with dead addresses until TTL
// reap.
func TestAdoptSpecializedPodsPopulatesGeneration(t *testing.T) {
	pod := specializedAdoptPod("fn1-pod", "default", "fn-uid-1", "3")
	fsCache := runAdopt(t, pod)

	liveFnMeta := &metav1.ObjectMeta{
		Name:       "fn1",
		Namespace:  "default",
		UID:        "fn-uid-1",
		Generation: 3, // matches the pod's FUNCTION_GENERATION label
	}
	got, err := fsCache.GetByFunction(liveFnMeta)
	require.NoError(t, err, "adopted entry must be retrievable via GetByFunction keyed on the live Function's Generation")
	require.Equal(t, "10.9.9.9:8888", got.Address)
}

// liveFn builds the live Function object the missing-label fallback fetches.
func liveFn(uid string, generation int64) *fv1.Function {
	return &fv1.Function{
		Name:       "fn1",
		Namespace:  "default",
		UID:        k8sTypes.UID(uid),
		Generation: generation,
	}
}

// TestAdoptSpecializedPodsMissingGenerationFallsBack locks the rolling-upgrade
// posture for a pod specialized by a release predating the
// fv1.FUNCTION_GENERATION label: instead of skipping it (which, once the
// rollout completes and no old-code replica remains, turns the upgrade window
// into a cluster-wide cold-start spike), adoptSpecializedPods adopts it
// against the live Function's CURRENT generation so GetByFunction keyed on the
// live Function finds it.
func TestAdoptSpecializedPodsMissingGenerationFallsBack(t *testing.T) {
	pod := specializedAdoptPod("fn1-pod", "default", "fn-uid-1", "")
	fsCache := runAdopt(t, pod, liveFn("fn-uid-1", 5))

	got, err := fsCache.GetByFunction(&liveFn("fn-uid-1", 5).ObjectMeta)
	require.NoError(t, err, "a label-less pod must be adopted against the live Function's current generation")
	require.Equal(t, "10.9.9.9:8888", got.Address)
}

// TestAdoptSpecializedPodsUnparsableGenerationFallsBack mirrors the
// missing-label case for a label present but not a valid int64 — same
// fallback: adopt against the live Function's current generation rather than
// skipping or adopting with a garbage Generation.
func TestAdoptSpecializedPodsUnparsableGenerationFallsBack(t *testing.T) {
	pod := specializedAdoptPod("fn1-pod", "default", "fn-uid-1", "not-a-number")
	fsCache := runAdopt(t, pod, liveFn("fn-uid-1", 5))

	got, err := fsCache.GetByFunction(&liveFn("fn-uid-1", 5).ObjectMeta)
	require.NoError(t, err, "an unparsable label must fall back to the live Function's current generation")
	require.Equal(t, "10.9.9.9:8888", got.Address)
}

// TestAdoptSpecializedPodsMissingGenerationNoLiveFunction: when the label is
// absent AND the live Function cannot be fetched (deleted mid-upgrade), the
// fallback has nothing trustworthy to key on — skip, as before.
func TestAdoptSpecializedPodsMissingGenerationNoLiveFunction(t *testing.T) {
	pod := specializedAdoptPod("fn1-pod", "default", "fn-uid-1", "")
	fsCache := runAdopt(t, pod) // no Function seeded: lookup 404s

	require.Empty(t, fsCache.ListByFunctionUID("fn-uid-1"),
		"a label-less pod whose Function no longer exists must not be adopted")
}

// TestAdoptSpecializedPodsMissingGenerationUIDMismatch: the fallback guards on
// UID — a pod left over from a deleted-and-recreated function of the same name
// must not be adopted against the NEW Function's generation.
func TestAdoptSpecializedPodsMissingGenerationUIDMismatch(t *testing.T) {
	pod := specializedAdoptPod("fn1-pod", "default", "fn-uid-old", "")
	fsCache := runAdopt(t, pod, liveFn("fn-uid-new", 5))

	require.Empty(t, fsCache.ListByFunctionUID("fn-uid-old"),
		"a pod whose function UID differs from the live Function's must not be adopted")
	require.Empty(t, fsCache.ListByFunctionUID("fn-uid-new"),
		"the stale pod must not be registered under the new Function's UID either")
}

// podAfterAdopt reads the pod back from the fake clientset so tests can assert
// on the stamp (the executorInstanceId annotation) independently of the cache.
func podAfterAdopt(t *testing.T, gpm *GenericPoolManager, ns, name string) *apiv1.Pod {
	t.Helper()
	pod, err := gpm.kubernetesClient.CoreV1().Pods(ns).Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	return pod
}

// TestAdoptActiveNotReadyPod pins the readiness-hole fix: a pod that is merely
// not-Ready during the adopt window (a probe blip) must still be adopted —
// stamped AND registered. The fsCache entry is what gives the idle reaper
// ownership, and IsValid gates every hand-out on live readiness anyway.
// Skipping it here meant the post-adopt cleanup deleted a healthy warm pod
// because of a 1-second probe blip.
func TestAdoptActiveNotReadyPod(t *testing.T) {
	pod := specializedAdoptPod("fn1-pod", "default", "fn-uid-1", "3")
	pod.Status.ContainerStatuses = []apiv1.ContainerStatus{{Ready: false}}
	pod.Status.Phase = apiv1.PodRunning

	gpm := runAdoptWithEnv(t, pod, fv1.Environment{Name: "env1", Namespace: "default"})

	after := podAfterAdopt(t, gpm, "default", "fn1-pod")
	require.Equal(t, "inst-1", after.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL],
		"an active-but-not-ready pod must be claimed, or the post-adopt reaper kills it")
	_, err := gpm.fsCache.GetByFunction(&metav1.ObjectMeta{Name: "fn1", Namespace: "default", UID: "fn-uid-1", Generation: 3})
	require.NoError(t, err, "the entry is what gives the idle reaper ownership of the pod")
}

// TestAdoptSkipsTerminalPod: Succeeded/Failed pods are inert; adopting them
// would register dead addresses.
func TestAdoptSkipsTerminalPod(t *testing.T) {
	pod := specializedAdoptPod("fn1-pod", "default", "fn-uid-1", "3")
	pod.Status.Phase = apiv1.PodSucceeded

	gpm := runAdoptWithEnv(t, pod, fv1.Environment{Name: "env1", Namespace: "default"})

	after := podAfterAdopt(t, gpm, "default", "fn1-pod")
	require.Empty(t, after.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL], "a terminal pod must not be claimed")
	_, err := gpm.fsCache.GetByFunction(&metav1.ObjectMeta{Name: "fn1", Namespace: "default", UID: "fn-uid-1", Generation: 3})
	require.Error(t, err)
}

// TestAdoptHalfSpecializedPodNotStamped pins the stamp-after-checks fix: a pod
// killed mid-specialization (no ANNOTATION_SVC_HOST yet) must be left
// UNSTAMPED so the post-adopt cleanup recycles it. Stamping first and then
// failing the checks minted pods that were protected from cleanup but
// invisible to the reaper — leaked until a human noticed.
func TestAdoptHalfSpecializedPodNotStamped(t *testing.T) {
	pod := specializedAdoptPod("fn1-pod", "default", "fn-uid-1", "3")
	delete(pod.Annotations, fv1.ANNOTATION_SVC_HOST)

	gpm := runAdoptWithEnv(t, pod, fv1.Environment{Name: "env1", Namespace: "default"})

	after := podAfterAdopt(t, gpm, "default", "fn1-pod")
	require.Empty(t, after.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL],
		"a pod that fails adoption must stay unstamped — unstamped is what lets cleanup recycle it")
	_, err := gpm.fsCache.GetByFunction(&metav1.ObjectMeta{Name: "fn1", Namespace: "default", UID: "fn-uid-1", Generation: 3})
	require.Error(t, err)
}

// TestAdoptSkipsStaleEnvRuntime: the env's runtime changed while the executor
// was down — this pod's runtime predates it and must be recycled, not adopted.
func TestAdoptSkipsStaleEnvRuntime(t *testing.T) {
	pod := specializedAdoptPod("fn1-pod", "default", "fn-uid-1", "3")
	env := fv1.Environment{
		Name: "env1", Namespace: "default",
		Spec: fv1.EnvironmentSpec{Runtime: fv1.Runtime{Image: "img:v2"}},
	}
	// The pod was born under a DIFFERENT runtime revision of the same env.
	pod.Labels[fv1.ENVIRONMENT_RUNTIME_HASH] = envRuntimeHash(&fv1.Environment{
		Name: "env1", Namespace: "default",
		Spec: fv1.EnvironmentSpec{Runtime: fv1.Runtime{Image: "img:v1"}},
	})
	gpm := runAdoptWithEnv(t, pod, env)

	after := podAfterAdopt(t, gpm, "default", "fn1-pod")
	require.Empty(t, after.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL],
		"a stale-runtime pod must be left for the cleanup pass")
	_, err := gpm.fsCache.GetByFunction(&metav1.ObjectMeta{Name: "fn1", Namespace: "default", UID: "fn-uid-1", Generation: 3})
	require.Error(t, err)
}

// TestAdoptSkipsRecreatedEnv: same name, different UID — the environment was
// deleted and recreated while the executor was down. Adopting would register a
// pod running the OLD environment's runtime against the new CR.
func TestAdoptSkipsRecreatedEnv(t *testing.T) {
	pod := specializedAdoptPod("fn1-pod", "default", "fn-uid-1", "3")
	pod.Labels[fv1.ENVIRONMENT_UID] = "old-env-uid"

	env := fv1.Environment{Name: "env1", Namespace: "default", UID: "new-env-uid"}
	gpm := runAdoptWithEnv(t, pod, env)

	after := podAfterAdopt(t, gpm, "default", "fn1-pod")
	require.Empty(t, after.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL])
	_, err := gpm.fsCache.GetByFunction(&metav1.ObjectMeta{Name: "fn1", Namespace: "default", UID: "fn-uid-1", Generation: 3})
	require.Error(t, err)
}

// TestAdoptMatchingEnvRuntime: the positive control for the two guards —
// matching UID and runtime hash adopt exactly as before.
func TestAdoptMatchingEnvRuntime(t *testing.T) {
	pod := specializedAdoptPod("fn1-pod", "default", "fn-uid-1", "3")
	env := fv1.Environment{
		Name: "env1", Namespace: "default", UID: "env-uid-1",
		Spec: fv1.EnvironmentSpec{Runtime: fv1.Runtime{Image: "img:v2"}},
	}
	pod.Labels[fv1.ENVIRONMENT_UID] = "env-uid-1"
	pod.Labels[fv1.ENVIRONMENT_RUNTIME_HASH] = envRuntimeHash(&env)

	gpm := runAdoptWithEnv(t, pod, env)

	after := podAfterAdopt(t, gpm, "default", "fn1-pod")
	require.Equal(t, "inst-1", after.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL])
	_, err := gpm.fsCache.GetByFunction(&metav1.ObjectMeta{Name: "fn1", Namespace: "default", UID: "fn-uid-1", Generation: 3})
	require.NoError(t, err)
}

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
	k8sfake "k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    labels,
			Annotations: map[string]string{
				fv1.FUNCTION_RESOURCE_VERSION: "100",
				fv1.ANNOTATION_SVC_HOST:       "10.9.9.9:8888",
			},
		},
		Status: apiv1.PodStatus{
			PodIP:             "10.9.9.9",
			ContainerStatuses: []apiv1.ContainerStatus{{Ready: true}},
		},
	}
}

// runAdopt wires a GenericPoolManager against a fake kubernetesClient seeded
// with pod, runs adoptSpecializedPods to completion, and returns the fsCache
// it populated.
func runAdopt(t *testing.T, pod *apiv1.Pod) *fscache.FunctionServiceCache {
	t.Helper()
	gpm := &GenericPoolManager{
		logger:           logr.Discard(),
		kubernetesClient: k8sfake.NewSimpleClientset(pod),
		nsResolver:       utils.DefaultNSResolver(),
		instanceID:       "inst-1",
		fsCache:          fscache.MakeFunctionServiceCache(logr.Discard()),
	}
	envMap := map[string]fv1.Environment{
		pod.Namespace + "/env1": {ObjectMeta: metav1.ObjectMeta{Name: "env1", Namespace: pod.Namespace}},
	}
	var wg sync.WaitGroup
	gpm.adoptSpecializedPods(t.Context(), &wg, envMap)
	wg.Wait()
	return gpm.fsCache
}

// TestAdoptSpecializedPodsPopulatesGeneration is the regression test for the
// coordinator-flagged gap: the synthetic ObjectMeta adoptSpecializedPods
// builds from pod labels/annotations must carry Generation (read from the
// fv1.FUNCTION_GENERATION pod label) so the resulting fsCache entry is keyed
// the same way (crd.CacheKeyUG = UID+Generation) as a live Function's
// GetByFunction lookup. Pre-migration, the synthetic ObjectMeta's zero-value
// Generation keyed the entry as (UID, 0), which no live Function
// (Generation >= 1) could ever match — RefreshFuncPods' GetByFunction would
// deterministically miss adopted pods, leaving stale byFunction/byAddress
// entries with dead addresses until TTL reap.
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

// TestAdoptSpecializedPodsSkipsMissingGeneration locks the chosen error
// posture for a pre-migration pod (rolling-upgrade window) that lacks the
// fv1.FUNCTION_GENERATION label: adoptSpecializedPods must skip adopting it
// — the same posture the surrounding code already uses for any other
// missing required label/annotation (ok1..ok8) — rather than silently
// keying it at Generation 0, which would produce an entry no live Function
// can ever look up.
func TestAdoptSpecializedPodsSkipsMissingGeneration(t *testing.T) {
	pod := specializedAdoptPod("fn1-pod", "default", "fn-uid-1", "")
	fsCache := runAdopt(t, pod)

	_, err := fsCache.GetByFunctionUID("fn-uid-1")
	require.Error(t, err, "a pod without the function-generation label must not be adopted into the cache")
}

// TestAdoptSpecializedPodsSkipsUnparsableGeneration mirrors the missing-label
// case for a label present but not a valid int64 — treated the same way
// (skip, don't adopt with a garbage Generation).
func TestAdoptSpecializedPodsSkipsUnparsableGeneration(t *testing.T) {
	pod := specializedAdoptPod("fn1-pod", "default", "fn-uid-1", "not-a-number")
	fsCache := runAdopt(t, pod)

	_, err := fsCache.GetByFunctionUID("fn-uid-1")
	require.Error(t, err, "a pod with an unparsable function-generation label must not be adopted into the cache")
}

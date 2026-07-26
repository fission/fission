// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"strconv"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/utils"
)

// refreshTestFixture wires a GenericPoolManager whose env pool is pre-seeded
// (so getPool answers from gpm.pools without MakeGenericPool), backed by fake
// clientsets, plus a set of specialized pods for one function.
type refreshTestFixture struct {
	gpm *GenericPoolManager
	fn  fv1.Function
}

// refreshTestPod builds a specialized (managed=false) pod for fn carrying the
// exact labelsForFunction selector set — the labels RefreshFuncPods lists by —
// plus the fv1.FUNCTION_GENERATION label when generation is non-empty (the
// shape choosePod's relabel stamps via specializedPodLabels; empty simulates a
// pre-RFC-0002 legacy pod).
func refreshTestPod(gp *GenericPool, fn *fv1.Function, name, generation string) *apiv1.Pod {
	labels := gp.labelsForFunction(&fn.ObjectMeta)
	if generation != "" {
		labels[fv1.FUNCTION_GENERATION] = generation
	}
	return &apiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: fn.Spec.Environment.Namespace,
			Labels:    labels,
		},
	}
}

// newRefreshFixture seeds pods for the live function at generations 3
// (alias-retained), 4 (old live, unretained), 5 (the new live generation), and
// one legacy pod without a generation label. The live Function is at
// generation 5, i.e. the refresh runs as the reconciler's reaction to the
// gen4 -> gen5 update.
func newRefreshFixture(t *testing.T) *refreshTestFixture {
	t.Helper()

	env := &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "env1", Namespace: "default", UID: "env-uid-1"},
	}
	fn := fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "fn1", Namespace: "default", UID: "fn-uid-1", Generation: 5},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: "env1", Namespace: "default"},
		},
	}

	fsCache := fscache.MakeFunctionServiceCache(logr.Discard())
	gp := &GenericPool{
		logger:  logr.Discard(),
		env:     env,
		fsCache: fsCache,
	}

	pods := []*apiv1.Pod{
		refreshTestPod(gp, &fn, "fn1-gen3", "3"),
		refreshTestPod(gp, &fn, "fn1-gen4", "4"),
		refreshTestPod(gp, &fn, "fn1-gen5", "5"),
		refreshTestPod(gp, &fn, "fn1-legacy", ""),
	}
	objs := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		objs = append(objs, p)
	}

	gpm := &GenericPoolManager{
		logger:           logr.Discard(),
		kubernetesClient: k8sfake.NewSimpleClientset(objs...),
		fissionClient:    fClient.NewSimpleClientset(env),
		fsCache:          fsCache,
		nsResolver:       utils.DefaultNSResolver(),
		pools:            map[string]*GenericPool{poolKey(env.UID, ""): gp},
		requestChannel:   make(chan *request),
	}
	// The pool actor: getPool answers through this channel. The goroutine
	// blocks on channel receive after the test and is reclaimed at process
	// exit (service has no stop signal; Run starts it the same way).
	go gpm.service()

	return &refreshTestFixture{gpm: gpm, fn: fn}
}

// remainingPods returns the names of the function's pods still present.
func (f *refreshTestFixture) remainingPods(t *testing.T) []string {
	t.Helper()
	podList, err := f.gpm.kubernetesClient.CoreV1().Pods("default").List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	names := make([]string, 0, len(podList.Items))
	for _, p := range podList.Items {
		names = append(names, p.Name)
	}
	return names
}

// TestRefreshFuncPodsSparesAliasRetainedGenerations is the C3 regression test:
// the Function reconciler's refresh (gen4 -> gen5 live update) must delete the
// old live generation's pods (gen4), the new generation's per existing
// semantics (gen5), and the legacy label-less pod — but leave the
// alias-retained gen3 pods warm, because destroying them knocks out a pinned
// FunctionVersion's warm capacity on every live deploy (RFC-0025 warm
// rollback).
func TestRefreshFuncPodsSparesAliasRetainedGenerations(t *testing.T) {
	fix := newRefreshFixture(t)

	// The same (UID, generation) predicate shape versionretain.View.Retained
	// provides: an alias pins generation 3 of this function.
	fix.gpm.SetVersionRetain(func(uid k8sTypes.UID, gen int64) bool {
		return uid == fix.fn.UID && gen == 3
	})

	// Drive the real reconciler entry point (funcManager.refreshFuncPods),
	// which threads gpm.retainedFn.
	require.NoError(t, fix.gpm.refreshFuncPods(t.Context(), &fix.fn))

	assert.ElementsMatch(t, []string{"fn1-gen3"}, fix.remainingPods(t),
		"only the alias-retained generation's pod may survive the spec-change refresh")
}

// TestRefreshFuncPodsReconcilerPathNilRetain locks the pre-RFC-0025 fallback:
// with no retain view wired (retainedFn nil, e.g. during boot before
// SetVersionRetain), the reconciler refresh recycles every pod, as before.
func TestRefreshFuncPodsReconcilerPathNilRetain(t *testing.T) {
	fix := newRefreshFixture(t)

	require.NoError(t, fix.gpm.refreshFuncPods(t.Context(), &fix.fn))

	assert.Empty(t, fix.remainingPods(t),
		"with no retain predicate every generation's pods must recycle")
}

// TestRefreshFuncPodsInterfaceRecyclesRetained locks the cms
// (Secret/ConfigMap content change) semantics: the ExecutorType-interface
// RefreshFuncPods recycles alias-retained generations too. Secret/ConfigMap
// contents are not part of a FunctionVersion snapshot and retained pods are
// exempt from the idle reaper, so this refresh is the only path that gets
// rotated data into a pinned version's pods.
func TestRefreshFuncPodsInterfaceRecyclesRetained(t *testing.T) {
	fix := newRefreshFixture(t)

	// Even with the retain view wired, the interface path must not spare.
	fix.gpm.SetVersionRetain(func(uid k8sTypes.UID, gen int64) bool {
		return uid == fix.fn.UID && gen == 3
	})

	require.NoError(t, fix.gpm.RefreshFuncPods(t.Context(), logr.Discard(), fix.fn))

	assert.Empty(t, fix.remainingPods(t),
		"the interface (cms) refresh must recycle alias-retained pods so rotated Secret/ConfigMap data reaches them")
}

// TestRefreshSparesPod pins the pure filter's decision table.
func TestRefreshSparesPod(t *testing.T) {
	t.Parallel()

	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "fn1", Namespace: "default", UID: "fn-uid-1", Generation: 5},
	}
	retainGen3 := func(uid k8sTypes.UID, gen int64) bool {
		return uid == fn.UID && gen == 3
	}
	labelsAt := func(gen int64) map[string]string {
		return map[string]string{fv1.FUNCTION_GENERATION: strconv.FormatInt(gen, 10)}
	}

	tests := []struct {
		name     string
		labels   map[string]string
		retained func(uid k8sTypes.UID, gen int64) bool
		want     bool
	}{
		{name: "retained non-live generation is spared", labels: labelsAt(3), retained: retainGen3, want: true},
		{name: "unretained old generation recycles", labels: labelsAt(4), retained: retainGen3, want: false},
		{name: "live generation recycles even when retained", labels: labelsAt(5),
			retained: func(uid k8sTypes.UID, gen int64) bool { return true }, want: false},
		{name: "nil predicate spares nothing", labels: labelsAt(3), retained: nil, want: false},
		{name: "missing generation label recycles", labels: map[string]string{}, retained: retainGen3, want: false},
		{name: "unparsable generation label recycles",
			labels:   map[string]string{fv1.FUNCTION_GENERATION: "not-a-number"},
			retained: func(uid k8sTypes.UID, gen int64) bool { return true }, want: false},
		{name: "retained generation of another function recycles", labels: labelsAt(3),
			retained: func(uid k8sTypes.UID, gen int64) bool { return uid == "other-uid" && gen == 3 }, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, refreshSparesPod(tc.labels, fn, tc.retained))
		})
	}
}

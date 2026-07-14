// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"testing"
	"time"

	"sync/atomic"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// scheme registers both kubernetes and fission types so the fake crClient
// can list/patch Pods and Functions in the same cache.
func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = fv1.AddToScheme(s)
	return s
}

// defaultCfg matches the env-var defaults in ProvisionerConfigFromEnv.
var defaultCfg = ProvisionerConfig{
	MaxPerFunction:         20,
	MaxInflightPerFunction: 4,
	ReconcileInterval:      30 * time.Second,
}

// newTestProvisioner builds a Provisioner wired with fake clients and the
// given cache objects. gpm is nil — unit tests here do not exercise
// GetFuncSvc (that path needs a real GenericPoolManager; covered by
// integration). kubernetesClient is empty by default; tests that patch
// pods should swap it via newTestProvisionerWithPods.
func newTestProvisioner(t *testing.T, crObjs ...client.Object) *Provisioner {
	t.Helper()
	crClient := crfake.NewClientBuilder().
		WithScheme(scheme()).
		WithObjects(crObjs...).
		Build()
	return NewProvisioner(
		logr.Discard(),
		nil,                          // gpm — not needed for unit tests
		fClient.NewSimpleClientset(), //nolint:staticcheck // simple tracker is fine for status updates in tests
		k8sfake.NewClientset(),
		crClient,
		defaultCfg,
	)
}

// toClientObjects converts []*corev1.Pod to []client.Object for the fake
// crClient builder.
func toClientObjects(pods ...*corev1.Pod) []client.Object {
	out := make([]client.Object, 0, len(pods))
	for _, p := range pods {
		out = append(out, p)
	}
	return out
}

// toRuntimeObjects converts []*corev1.Pod to []runtime.Object for the
// fake kubernetes clientset.
func toRuntimeObjects(pods ...*corev1.Pod) []runtime.Object {
	out := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		out = append(out, p)
	}
	return out
}

// newTestProvisionerWithPods seeds BOTH the crClient (for List) and the
// kubernetesClient (for Patch) with the same pods, so label-patch tests
// can observe the result through either client.
func newTestProvisionerWithPods(t *testing.T, pods ...*corev1.Pod) *Provisioner {
	t.Helper()
	p := newTestProvisioner(t, toClientObjects(pods...)...)
	p.kubernetesClient = k8sfake.NewSimpleClientset(toRuntimeObjects(pods...)...)
	return p
}

// readyPod builds a Pod with the provisioned/served/functionUid labels,
// Running phase, an IP, and a ready container — the shape
// countProvisionedPods expects to count.
func readyPod(name, fnUID string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				fv1.FUNCTION_UID:      fnUID,
				fv1.SERVED_LABEL:      fv1.SERVED_VALUE,
				fv1.PROVISIONED_LABEL: fv1.PROVISIONED_VALUE,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.1",
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true, Name: "fn"},
			},
		},
	}
}

// podWithPhase returns a copy of readyPod's metadata but with the given
// phase and no PodIP/ready containers — used to test filtering.
func podWithPhase(name, fnUID string, phase corev1.PodPhase) *corev1.Pod {
	p := readyPod(name, fnUID)
	p.Status = corev1.PodStatus{Phase: phase}
	return p
}

// podNotReady returns a Running pod whose container is not ready yet.
func podNotReady(name, fnUID string) *corev1.Pod {
	p := readyPod(name, fnUID)
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Ready: false, Name: "fn"}}
	return p
}

// podNoProvisionedLabel has served+functionUid but no provisioned label.
func podNoProvisionedLabel(name, fnUID string) *corev1.Pod {
	p := readyPod(name, fnUID)
	delete(p.Labels, fv1.PROVISIONED_LABEL)
	return p
}

// provisionedFn builds a Function with the given target and poolmgr executor.
func provisionedFn(name string, target int) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default",
			UID: types.UID("fn-uid-" + name), Generation: 1,
		},
		Spec: fv1.FunctionSpec{
			ProvisionedConcurrency: &fv1.ProvisionedConcurrencyConfig{Target: target},
		},
	}
}

// provisionedFnWithUID lets tests pin a specific UID for pod-label matching.
func provisionedFnWithUID(name, uid string, target int) *fv1.Function {
	fn := provisionedFn(name, target)
	fn.UID = types.UID(uid)
	return fn
}

// getPod re-fetches a pod from the fake kubernetes clientset.
func getPod(t *testing.T, p *Provisioner, name string) *corev1.Pod {
	t.Helper()
	got, err := p.kubernetesClient.CoreV1().Pods("default").Get(
		t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	return got
}

// getFnStatus re-fetches a Function's status from the fake fission clientset.
func getFnStatus(t *testing.T, p *Provisioner, name string) fv1.FunctionStatus {
	t.Helper()
	got, err := p.fissionClient.CoreV1().Functions("default").Get(
		t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	return got.Status
}

// ---------------------------------------------------------------------------
// effectiveTarget
// ---------------------------------------------------------------------------

func TestProvisioner_effectiveTarget(t *testing.T) {
	p := newTestProvisioner(t)

	tests := []struct {
		name   string
		target int
		max    int
		want   int
	}{
		{"below cap", 5, 20, 5},
		{"at cap", 20, 20, 20},
		{"above cap clamps", 25, 20, 20},
		{"target=1", 1, 20, 1},
		{"max=1 clamps", 10, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p.config.MaxPerFunction = tt.max
			fn := &fv1.Function{
				Spec: fv1.FunctionSpec{
					ProvisionedConcurrency: &fv1.ProvisionedConcurrencyConfig{Target: tt.target},
				},
			}
			assert.Equal(t, tt.want, p.effectiveTarget(fn))
		})
	}
}

// ---------------------------------------------------------------------------
// ProvisionerConfigFromEnv
// ---------------------------------------------------------------------------

func TestProvisionerConfigFromEnv(t *testing.T) {
	allKeys := []string{
		"EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED",
		"EXECUTOR_PROVISIONED_MAX_PER_FUNCTION",
		"EXECUTOR_PROVISIONED_MAX_INFLIGHT_PER_FUNCTION",
		"EXECUTOR_PROVISIONED_RECONCILE_INTERVAL",
	}

	tests := []struct {
		name   string
		envs   map[string]string
		want   ProvisionerConfig
		wantOk bool
	}{
		{
			"unset = off",
			nil,
			ProvisionerConfig{}, false,
		},
		{
			"false = off",
			map[string]string{"EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED": "false"},
			ProvisionerConfig{}, false,
		},
		{
			"garbage bool = off",
			map[string]string{"EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED": "yes"},
			ProvisionerConfig{}, false,
		},
		{
			"enabled, defaults",
			map[string]string{"EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED": "true"},
			ProvisionerConfig{MaxPerFunction: 20, MaxInflightPerFunction: 4, ReconcileInterval: 30 * time.Second}, true,
		},
		{
			"enabled, overrides",
			map[string]string{
				"EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED":       "true",
				"EXECUTOR_PROVISIONED_MAX_PER_FUNCTION":          "50",
				"EXECUTOR_PROVISIONED_MAX_INFLIGHT_PER_FUNCTION": "8",
				"EXECUTOR_PROVISIONED_RECONCILE_INTERVAL":        "1m",
			},
			ProvisionerConfig{MaxPerFunction: 50, MaxInflightPerFunction: 8, ReconcileInterval: time.Minute}, true,
		},
		{
			"enabled, garbage ints fall back to defaults",
			map[string]string{
				"EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED":       "true",
				"EXECUTOR_PROVISIONED_MAX_PER_FUNCTION":          "abc",
				"EXECUTOR_PROVISIONED_MAX_INFLIGHT_PER_FUNCTION": "",
				"EXECUTOR_PROVISIONED_RECONCILE_INTERVAL":        "notaduration",
			},
			ProvisionerConfig{MaxPerFunction: 20, MaxInflightPerFunction: 4, ReconcileInterval: 30 * time.Second}, true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all keys first so subtests are isolated. t.Setenv with
			// "" leaves the var set-but-empty, which os.Getenv returns as
			// "" — equivalent to "unset" for ParseBool/Atoi/ParseDuration.
			for _, k := range allKeys {
				t.Setenv(k, "")
			}
			for k, v := range tt.envs {
				t.Setenv(k, v)
			}
			got, ok := ProvisionerConfigFromEnv()
			assert.Equal(t, tt.wantOk, ok, "enabled flag")
			assert.Equal(t, tt.want, got, "config")
		})
	}
}

// ---------------------------------------------------------------------------
// countProvisionedPods
// ---------------------------------------------------------------------------

func TestProvisioner_countProvisionedPods(t *testing.T) {
	const uid = "u1"
	fn := provisionedFnWithUID("fn", uid, 3)

	t.Run("counts only ready running provisioned pods for this function", func(t *testing.T) {
		pods := []*corev1.Pod{
			readyPod("a", uid),                        // counts
			podWithPhase("b", uid, corev1.PodPending), // filtered: not running
			podNotReady("c", uid),                     // filtered: not ready
			podNoProvisionedLabel("d", uid),           // filtered: no provisioned label
			readyPod("e", "other-uid"),                // filtered: different function UID
		}
		p := newTestProvisionerWithPods(t, pods...)
		got, err := p.countProvisionedPods(t.Context(), fn)
		require.NoError(t, err)
		assert.Equal(t, 1, got, "only pod 'a' is ready+running+provisioned+this-fn")
	})

	t.Run("zero pods", func(t *testing.T) {
		p := newTestProvisioner(t)
		got, err := p.countProvisionedPods(t.Context(), fn)
		require.NoError(t, err)
		assert.Equal(t, 0, got)
	})

	t.Run("multiple ready pods all counted", func(t *testing.T) {
		pods := []*corev1.Pod{
			readyPod("a", uid),
			readyPod("b", uid),
			readyPod("c", uid),
		}
		p := newTestProvisionerWithPods(t, pods...)
		got, err := p.countProvisionedPods(t.Context(), fn)
		require.NoError(t, err)
		assert.Equal(t, 3, got)
	})
}

// ---------------------------------------------------------------------------
// clearProvisionedLabel
// ---------------------------------------------------------------------------

func TestProvisioner_clearProvisionedLabel(t *testing.T) {
	pod := readyPod("p1", "u1")
	p := newTestProvisionerWithPods(t, pod)

	require.NoError(t, p.clearProvisionedLabel(t.Context(), pod))

	got := getPod(t, p, "p1")
	assert.NotContains(t, got.Labels, fv1.PROVISIONED_LABEL, "provisioned label removed")
	assert.Equal(t, fv1.SERVED_VALUE, got.Labels[fv1.SERVED_LABEL], "served label kept")
	assert.Equal(t, "u1", got.Labels[fv1.FUNCTION_UID], "functionUid label kept")
}

// ---------------------------------------------------------------------------
// clearExcessProvisionedLabels
// ---------------------------------------------------------------------------

func TestProvisioner_clearExcessProvisionedLabels(t *testing.T) {
	const uid = "u1"
	fn := provisionedFnWithUID("fn", uid, 1)

	// Build 3 pods with distinct creation timestamps t1 < t2 < t3.
	t0 := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	mkPod := func(name string, age time.Duration) *corev1.Pod {
		p := readyPod(name, uid)
		p.CreationTimestamp = metav1.NewTime(t0.Add(age))
		return p
	}
	pods := []*corev1.Pod{
		mkPod("oldest", 0),
		mkPod("middle", time.Minute),
		mkPod("newest", 2*time.Minute),
	}

	t.Run("clears oldest excess pods, keeps newest", func(t *testing.T) {
		p := newTestProvisionerWithPods(t, pods...)
		p.clearExcessProvisionedLabels(t.Context(), fn, 2)

		oldest := getPod(t, p, "oldest")
		middle := getPod(t, p, "middle")
		newest := getPod(t, p, "newest")
		assert.NotContains(t, oldest.Labels, fv1.PROVISIONED_LABEL, "oldest cleared")
		assert.NotContains(t, middle.Labels, fv1.PROVISIONED_LABEL, "middle cleared")
		assert.Contains(t, newest.Labels, fv1.PROVISIONED_LABEL, "newest kept")
	})

	t.Run("excess > pod count is a no-op", func(t *testing.T) {
		p := newTestProvisionerWithPods(t, pods...)
		p.clearExcessProvisionedLabels(t.Context(), fn, 10)
		for _, name := range []string{"oldest", "middle", "newest"} {
			got := getPod(t, p, name)
			assert.Contains(t, got.Labels, fv1.PROVISIONED_LABEL, "%s kept", name)
		}
	})

	t.Run("excess=0 clears none", func(t *testing.T) {
		p := newTestProvisionerWithPods(t, pods...)
		p.clearExcessProvisionedLabels(t.Context(), fn, 0)
		for _, name := range []string{"oldest", "middle", "newest"} {
			got := getPod(t, p, name)
			assert.Contains(t, got.Labels, fv1.PROVISIONED_LABEL, "%s kept", name)
		}
	})
}

// ---------------------------------------------------------------------------
// clearAllProvisionedLabels
// ---------------------------------------------------------------------------

func TestProvisioner_clearAllProvisionedLabels(t *testing.T) {
	const uid = "u1"
	fn := provisionedFnWithUID("fn", uid, 0)
	pods := []*corev1.Pod{
		readyPod("a", uid),
		readyPod("b", uid),
	}
	p := newTestProvisionerWithPods(t, pods...)
	p.clearAllProvisionedLabels(t.Context(), fn)

	for _, name := range []string{"a", "b"} {
		got := getPod(t, p, name)
		assert.NotContains(t, got.Labels, fv1.PROVISIONED_LABEL, "%s cleared", name)
	}
}

// ---------------------------------------------------------------------------
// tryAcquire / release
// ---------------------------------------------------------------------------

func TestProvisioner_tryAcquire_release(t *testing.T) {
	p := newTestProvisioner(t)
	p.config.MaxInflightPerFunction = 3
	const uid = types.UID("u1")

	t.Run("admits up to max then rejects", func(t *testing.T) {
		assert.True(t, p.tryAcquire(uid), "1st")
		assert.True(t, p.tryAcquire(uid), "2nd")
		assert.True(t, p.tryAcquire(uid), "3rd")
		assert.False(t, p.tryAcquire(uid), "4th over cap")
	})

	t.Run("release frees a slot", func(t *testing.T) {
		p.release(uid)
		assert.True(t, p.tryAcquire(uid), "slot freed after release")
	})

	t.Run("different UID is independent", func(t *testing.T) {
		const uid2 = types.UID("u2")
		assert.True(t, p.tryAcquire(uid2), "different fn not blocked by u1")
	})

	t.Run("release of unknown UID is a no-op", func(t *testing.T) {
		p.release(types.UID("never-acquired")) // must not panic
	})
}

// ---------------------------------------------------------------------------
// updateFunctionStatus
// ---------------------------------------------------------------------------

func TestProvisioner_updateFunctionStatus(t *testing.T) {
	fn := provisionedFn("fn", 5)
	// Seed both crClient (Get) and fissionClient (UpdateStatus).
	p := newTestProvisioner(t, fn)
	p.fissionClient = fClient.NewSimpleClientset(fn) //nolint:staticcheck

	t.Run("warming: ready < target", func(t *testing.T) {
		require.NoError(t, p.updateFunctionStatus(t.Context(), fn, 2, 5))
		st := getFnStatus(t, p, "fn")
		assert.Equal(t, 2, st.ProvisionedReady)
		assert.Equal(t, 5, st.ProvisionedTarget)
		cond := metaFindCondition(st, fv1.FunctionConditionProvisioned)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, fv1.FunctionReasonProvisionedWarming, cond.Reason)
	})

	t.Run("satisfied: ready >= target", func(t *testing.T) {
		require.NoError(t, p.updateFunctionStatus(t.Context(), fn, 5, 5))
		st := getFnStatus(t, p, "fn")
		assert.Equal(t, 5, st.ProvisionedReady)
		assert.Equal(t, 5, st.ProvisionedTarget)
		cond := metaFindCondition(st, fv1.FunctionConditionProvisioned)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, fv1.FunctionReasonProvisionedSatisfied, cond.Reason)
	})

	t.Run("oversatisfied: ready > target still True", func(t *testing.T) {
		require.NoError(t, p.updateFunctionStatus(t.Context(), fn, 7, 5))
		st := getFnStatus(t, p, "fn")
		cond := metaFindCondition(st, fv1.FunctionConditionProvisioned)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
	})

	t.Run("disabled: target=0 sets False/ProvisionedDisabled", func(t *testing.T) {
		require.NoError(t, p.updateFunctionStatus(t.Context(), fn, 0, 0))
		st := getFnStatus(t, p, "fn")
		assert.Equal(t, 0, st.ProvisionedReady)
		assert.Equal(t, 0, st.ProvisionedTarget)
		cond := metaFindCondition(st, fv1.FunctionConditionProvisioned)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, fv1.FunctionReasonProvisionedDisabled, cond.Reason)
	})
}

func TestProvisioner_UpdateFunctionStatusZero(t *testing.T) {
	fn := provisionedFn("fn", 5)
	p := newTestProvisioner(t, fn)
	p.fissionClient = fClient.NewSimpleClientset(fn) //nolint:staticcheck

	require.NoError(t, p.UpdateFunctionStatusZero(t.Context(), fn))
	st := getFnStatus(t, p, "fn")
	assert.Equal(t, 0, st.ProvisionedReady)
	assert.Equal(t, 0, st.ProvisionedTarget)
	cond := metaFindCondition(st, fv1.FunctionConditionProvisioned)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, fv1.FunctionReasonProvisionedDisabled, cond.Reason)
}

// ---------------------------------------------------------------------------
// StopProvisioning
// ---------------------------------------------------------------------------

func TestProvisioner_StopProvisioning(t *testing.T) {
	const uid = "u1"
	fn := provisionedFnWithUID("fn", uid, 2)
	pods := []*corev1.Pod{readyPod("a", uid), readyPod("b", uid)}
	p := newTestProvisionerWithPods(t, pods...)
	// Pre-seed inflight counter to verify it's deleted.
	_, _ = p.inflight.LoadOrStore(types.UID(uid), new(atomic.Int32))

	p.StopProvisioning(t.Context(), fn)

	for _, name := range []string{"a", "b"} {
		got := getPod(t, p, name)
		assert.NotContains(t, got.Labels, fv1.PROVISIONED_LABEL, "%s cleared", name)
	}
	_, ok := p.inflight.Load(types.UID(uid))
	assert.False(t, ok, "inflight entry deleted")
}

// ---------------------------------------------------------------------------
// reconcileFunction (loop body — branches that don't need GetFuncSvc)
// ---------------------------------------------------------------------------

func TestProvisioner_reconcileFunction(t *testing.T) {
	const uid = "u1"

	t.Run("target=0 clears all labels and zeroes status", func(t *testing.T) {
		fn := provisionedFnWithUID("fn", uid, 0)
		pods := []*corev1.Pod{readyPod("a", uid), readyPod("b", uid)}
		p := newTestProvisionerWithPods(t, pods...)
		// Seed fn in both clients so updateFunctionStatus can Get+Update.
		p.crClient = crfake.NewClientBuilder().WithScheme(scheme()).
			WithObjects(toClientObjects(pods...)...).WithObjects(fn).Build()
		p.fissionClient = fClient.NewSimpleClientset(fn) //nolint:staticcheck
		// effectiveTarget returns min(0, MaxPerFunction)=0.
		p.reconcileFunction(t.Context(), fn)
		for _, name := range []string{"a", "b"} {
			got := getPod(t, p, name)
			assert.NotContains(t, got.Labels, fv1.PROVISIONED_LABEL, "%s cleared", name)
		}
		st := getFnStatus(t, p, "fn")
		assert.Equal(t, 0, st.ProvisionedReady)
		assert.Equal(t, 0, st.ProvisionedTarget)
	})

	t.Run("ready > target clears excess", func(t *testing.T) {
		fn := provisionedFnWithUID("fn", uid, 1)
		t0 := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		mkPod := func(name string, age time.Duration) *corev1.Pod {
			p := readyPod(name, uid)
			p.CreationTimestamp = metav1.NewTime(t0.Add(age))
			return p
		}
		pods := []*corev1.Pod{mkPod("old", 0), mkPod("new", time.Minute)}
		p := newTestProvisionerWithPods(t, pods...)
		p.reconcileFunction(t.Context(), fn)
		old := getPod(t, p, "old")
		newest := getPod(t, p, "new")
		assert.NotContains(t, old.Labels, fv1.PROVISIONED_LABEL, "oldest cleared")
		assert.Contains(t, newest.Labels, fv1.PROVISIONED_LABEL, "newest kept")
	})

	t.Run("ready == target updates status satisfied", func(t *testing.T) {
		fn := provisionedFnWithUID("fn", uid, 2)
		pods := []*corev1.Pod{readyPod("a", uid), readyPod("b", uid)}
		p := newTestProvisionerWithPods(t, pods...)
		// Seed fn in both clients so updateFunctionStatus can Get+Update.
		p.crClient = crfake.NewClientBuilder().WithScheme(scheme()).
			WithObjects(toClientObjects(pods...)...).WithObjects(fn).Build()
		p.fissionClient = fClient.NewSimpleClientset(fn) //nolint:staticcheck
		p.reconcileFunction(t.Context(), fn)
		st := getFnStatus(t, p, "fn")
		assert.Equal(t, 2, st.ProvisionedReady)
		assert.Equal(t, 2, st.ProvisionedTarget)
		cond := metaFindCondition(st, fv1.FunctionConditionProvisioned)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, fv1.FunctionReasonProvisionedSatisfied, cond.Reason)
	})
}

// ---------------------------------------------------------------------------
// filterOptedFunctions
// ---------------------------------------------------------------------------

func TestFilterOptedFunctions(t *testing.T) {
	poolmgrFn := func(name string, target int) fv1.Function {
		fn := provisionedFn(name, target)
		fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypePoolmgr
		return *fn
	}
	newdeployFn := func(name string, target int) fv1.Function {
		fn := provisionedFn(name, target)
		fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = fv1.ExecutorTypeNewdeploy
		return *fn
	}
	plainFn := func(name string) fv1.Function {
		fn := poolmgrFn(name, 0)
		fn.Spec.ProvisionedConcurrency = nil
		return fn
	}

	t.Run("only poolmgr + provisioned functions", func(t *testing.T) {
		list := &fv1.FunctionList{Items: []fv1.Function{
			poolmgrFn("a", 3),
			newdeployFn("b", 3),
			plainFn("c"),
			poolmgrFn("d", 1),
		}}
		got := filterOptedFunctions(list)
		assert.Len(t, got, 2)
		assert.Equal(t, "a", got[0].Name)
		assert.Equal(t, "d", got[1].Name)
	})

	t.Run("empty list", func(t *testing.T) {
		list := &fv1.FunctionList{}
		got := filterOptedFunctions(list)
		assert.Empty(t, got)
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// metaFindCondition returns the condition of the given type from a
// FunctionStatus, or nil if absent. Uses meta.FindCondition but tolerates
// a missing condition list without panicking.
func metaFindCondition(st fv1.FunctionStatus, ct string) *metav1.Condition {
	cond := meta.FindStatusCondition(st.Conditions, ct)
	return cond
}

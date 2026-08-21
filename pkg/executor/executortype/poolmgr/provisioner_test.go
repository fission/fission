// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// fakeGPM is a unit-test stub for funcSvcGetter. It returns a queued
// FuncSvc (or error) per call, optionally blocking to let tests observe
// concurrency.
type fakeGPM struct {
	calls      atomic.Int64
	concurrent atomic.Int64
	peakConc   atomic.Int64
	block      chan struct{} // if non-nil, each call blocks until this is closed
	svc        *fscache.FuncSvc
	err        error
	refs       []corev1.ObjectReference

	// untaps / markFailures count the two PoolCache accounting callbacks the
	// provisioner owes after a GetFuncSvc. GetFuncSvc books a reservation on
	// the caller's behalf (SetSvcValue bumps activeRequests and settles
	// svcWaiting); the router normally settles up over the UnTap RPC, but the
	// provisioner calls GetFuncSvc in-process and must settle up itself.
	// Skipping either one corrupts the executor's concurrency accounting —
	// see the "two disjoint modes" note in CLAUDE.md.
	untaps       atomic.Int64
	markFailures atomic.Int64
	// lastUntapAddr records the svcHost the last UnTapService was called with,
	// so a test can prove the release is keyed on the FuncSvc the call
	// returned rather than on some other address.
	lastUntapAddr atomic.Value
}

func (f *fakeGPM) GetFuncSvc(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	n := f.calls.Add(1)
	cur := f.concurrent.Add(1)
	for {
		peak := f.peakConc.Load()
		if cur <= peak || f.peakConc.CompareAndSwap(peak, cur) {
			break
		}
	}
	defer f.concurrent.Add(-1)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.refs != nil {
		return &fscache.FuncSvc{KubernetesObjects: []corev1.ObjectReference{f.refs[(n-1)%int64(len(f.refs))]}}, f.err
	}
	return f.svc, f.err
}

func (f *fakeGPM) UnTapService(ctx context.Context, fnMeta *metav1.ObjectMeta, svcHost string) {
	f.untaps.Add(1)
	f.lastUntapAddr.Store(svcHost)
}

func (f *fakeGPM) MarkSpecializationFailure(ctx context.Context, fnMeta *metav1.ObjectMeta) {
	f.markFailures.Add(1)
}

// podRef builds an ObjectReference for a pod in the given namespace.
func podRef(name, ns string) corev1.ObjectReference {
	return corev1.ObjectReference{Kind: "Pod", Name: name, Namespace: ns}
}

// newTestProvisionerWithGPM wires a Provisioner with a fake gpm, a fake
// kubernetes clientset pre-seeded with the given pods, and the function
// in both the crClient and fissionClient so updateFunctionStatus works.
func newTestProvisionerWithGPM(t *testing.T, gpm funcSvcGetter, fn *fv1.Function, pods ...*corev1.Pod) *Provisioner {
	t.Helper()
	p := newTestProvisionerWithPods(t, pods...)
	p.gpm = gpm
	if fn != nil {
		p.crClient = crfake.NewClientBuilder().WithScheme(scheme()).
			WithObjects(toClientObjects(pods...)...).WithObjects(fn).Build()
		p.fissionClient = fClient.NewSimpleClientset(fn) //nolint:staticcheck
	}
	return p
}

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

// readyPod builds a Pod with the provisioned/served/functionUid labels
// (generation "1", matching provisionedFn's default fn.Generation),
// Running phase, an IP, and a ready container — the shape
// countProvisionedPods expects to count.
func readyPod(name, fnUID string) *corev1.Pod {
	return readyPodGen(name, fnUID, "1")
}

// readyPodGen is readyPod with an explicit fv1.FUNCTION_GENERATION label, for
// tests exercising the provisioner's generation filter.
func readyPodGen(name, fnUID, generation string) *corev1.Pod {
	return &corev1.Pod{
		Name:      name,
		Namespace: "default",
		Labels: map[string]string{
			fv1.FUNCTION_UID:        fnUID,
			fv1.FUNCTION_GENERATION: generation,
			fv1.SERVED_LABEL:        fv1.SERVED_VALUE,
			fv1.PROVISIONED_LABEL:   fv1.PROVISIONED_VALUE,
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
		Name: name, Namespace: "default",
		UID: types.UID("fn-uid-" + name), Generation: 1,
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
		t.Context(), name, metav1.GetOptions{},
	)
	require.NoError(t, err)
	return got
}

// getFnStatus re-fetches a Function's status from the fake fission clientset.
func getFnStatus(t *testing.T, p *Provisioner, name string) fv1.FunctionStatus {
	t.Helper()
	got, err := p.fissionClient.CoreV1().Functions("default").Get(
		t.Context(), name, metav1.GetOptions{},
	)
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
		name    string
		envs    map[string]string
		want    ProvisionerConfig
		wantOk  bool
		wantErr bool
	}{
		{
			"unset = off",
			nil,
			ProvisionerConfig{},
			false, false,
		},
		{
			"false = off",
			map[string]string{"EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED": "false"},
			ProvisionerConfig{},
			false, false,
		},
		{
			"garbage bool = off with error",
			map[string]string{"EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED": "yes"},
			ProvisionerConfig{},
			false, true,
		},
		{
			"enabled, defaults",
			map[string]string{"EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED": "true"},
			ProvisionerConfig{MaxPerFunction: 20, MaxInflightPerFunction: 4, ReconcileInterval: 30 * time.Second},
			true, false,
		},
		{
			"enabled, overrides",
			map[string]string{
				"EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED":       "true",
				"EXECUTOR_PROVISIONED_MAX_PER_FUNCTION":          "50",
				"EXECUTOR_PROVISIONED_MAX_INFLIGHT_PER_FUNCTION": "8",
				"EXECUTOR_PROVISIONED_RECONCILE_INTERVAL":        "1m",
			},
			ProvisionerConfig{MaxPerFunction: 50, MaxInflightPerFunction: 8, ReconcileInterval: time.Minute},
			true, false,
		},
		{
			"enabled, garbage ints fall back to defaults",
			map[string]string{
				"EXECUTOR_PROVISIONED_CONCURRENCY_ENABLED":       "true",
				"EXECUTOR_PROVISIONED_MAX_PER_FUNCTION":          "abc",
				"EXECUTOR_PROVISIONED_MAX_INFLIGHT_PER_FUNCTION": "",
				"EXECUTOR_PROVISIONED_RECONCILE_INTERVAL":        "notaduration",
			},
			ProvisionerConfig{MaxPerFunction: 20, MaxInflightPerFunction: 4, ReconcileInterval: 30 * time.Second},
			true, false,
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
			got, ok, err := ProvisionerConfigFromEnv()
			assert.Equal(t, tt.wantOk, ok, "enabled flag")
			assert.Equal(t, tt.want, got, "config")
			if tt.wantErr {
				assert.Error(t, err, "expected parse error")
			} else {
				assert.NoError(t, err, "expected no error")
			}
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
// generation transition: after a function update bumps Generation, gen-1
// provisioned pods must stop counting toward the target (countProvisionedPods
// is generation-filtered) but must still be reachable by the label-clearing
// paths (clearProvisionedLabels / disableProvisioning are NOT
// generation-filtered).
// ---------------------------------------------------------------------------

func TestProvisioner_generationTransition(t *testing.T) {
	const uid = "u1"

	// fn is now at generation 2 (e.g. after a spec update); the seeded pods
	// were specialized under generation 1 and still carry that label.
	fnGen2 := provisionedFnWithUID("fn", uid, 3)
	fnGen2.Generation = 2

	genOnePods := []*corev1.Pod{
		readyPodGen("gen1-a", uid, "1"),
		readyPodGen("gen1-b", uid, "1"),
	}

	t.Run("countProvisionedPods ignores stale-generation pods", func(t *testing.T) {
		p := newTestProvisionerWithPods(t, genOnePods...)
		got, err := p.countProvisionedPods(t.Context(), fnGen2)
		require.NoError(t, err)
		assert.Equal(t, 0, got, "gen-1 pods must not count toward the gen-2 target")
	})

	t.Run("countProvisionedPods counts only pods matching the current generation", func(t *testing.T) {
		mixed := []*corev1.Pod{
			readyPodGen("gen1-a", uid, "1"),
			readyPodGen("gen2-a", uid, "2"),
			readyPodGen("gen2-b", uid, "2"),
		}
		p := newTestProvisionerWithPods(t, mixed...)
		got, err := p.countProvisionedPods(t.Context(), fnGen2)
		require.NoError(t, err)
		assert.Equal(t, 2, got, "only the two gen-2 pods count")
	})

	t.Run("clearProvisionedLabels still finds and clears gen-1 pods", func(t *testing.T) {
		p := newTestProvisionerWithPods(t, genOnePods...)
		p.clearProvisionedLabels(t.Context(), fnGen2, -1)
		for _, name := range []string{"gen1-a", "gen1-b"} {
			got := getPod(t, p, name)
			assert.NotContains(t, got.Labels, fv1.PROVISIONED_LABEL, "%s cleared despite generation mismatch", name)
		}
	})

	t.Run("disableProvisioning still finds and clears gen-1 pods", func(t *testing.T) {
		p := newTestProvisionerWithPods(t, genOnePods...)
		p.crClient = crfake.NewClientBuilder().WithScheme(scheme()).
			WithObjects(toClientObjects(genOnePods...)...).WithObjects(fnGen2).Build()
		p.fissionClient = fClient.NewSimpleClientset(fnGen2) //nolint:staticcheck

		p.disableProvisioning(t.Context(), fnGen2)

		for _, name := range []string{"gen1-a", "gen1-b"} {
			got := getPod(t, p, name)
			assert.NotContains(t, got.Labels, fv1.PROVISIONED_LABEL, "%s cleared by disableProvisioning", name)
		}
	})

	t.Run("reconcileFunction on a bumped generation warms the new generation without touching gen-1 labels", func(t *testing.T) {
		// ready(gen-2)=0 < target=3 fires eager specializations rather than
		// treating the gen-1 pods as already satisfying the target; the
		// gen-1 pods' labels are untouched by the warming branch (only the
		// excess/clear branch touches labels).
		block := make(chan struct{})
		defer close(block)
		gpm := &fakeGPM{
			block: block,
			svc: &fscache.FuncSvc{
				KubernetesObjects: []corev1.ObjectReference{podRef("p", "default")},
			},
		}
		p := newTestProvisionerWithGPM(t, gpm, fnGen2, genOnePods...)
		p.config.MaxInflightPerFunction = 4

		p.reconcileFunction(t.Context(), fnGen2)

		require.Eventually(t, func() bool {
			return gpm.calls.Load() == 3
		}, 2*time.Second, 10*time.Millisecond, "delta=3 eager calls fired for the new generation")

		for _, name := range []string{"gen1-a", "gen1-b"} {
			got := getPod(t, p, name)
			assert.Contains(t, got.Labels, fv1.PROVISIONED_LABEL, "%s label untouched by the warming branch", name)
		}
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
		p.clearProvisionedLabels(t.Context(), fn, 2)

		oldest := getPod(t, p, "oldest")
		middle := getPod(t, p, "middle")
		newest := getPod(t, p, "newest")
		assert.NotContains(t, oldest.Labels, fv1.PROVISIONED_LABEL, "oldest cleared")
		assert.NotContains(t, middle.Labels, fv1.PROVISIONED_LABEL, "middle cleared")
		assert.Contains(t, newest.Labels, fv1.PROVISIONED_LABEL, "newest kept")
	})

	t.Run("excess > pod count all pods are cleared", func(t *testing.T) {
		p := newTestProvisionerWithPods(t, pods...)
		p.clearProvisionedLabels(t.Context(), fn, 10)
		for _, name := range []string{"oldest", "middle", "newest"} {
			got := getPod(t, p, name)
			assert.NotContains(t, got.Labels, fv1.PROVISIONED_LABEL, "%s cleared", name)
		}
	})

	t.Run("excess=0 clears none", func(t *testing.T) {
		p := newTestProvisionerWithPods(t, pods...)
		p.clearProvisionedLabels(t.Context(), fn, 0)
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
	p.clearProvisionedLabels(t.Context(), fn, -1)

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
// tryAcquire — concurrent/race (invariant P3: rollback on reject)
// ---------------------------------------------------------------------------

func TestProvisioner_tryAcquire_concurrent(t *testing.T) {
	// Hammer tryAcquire/release from many goroutines. Under -race this
	// catches any non-atomic read/modify of the per-UID counter; the
	// invariant is that count never exceeds MaxInflightPerFunction and
	// never goes negative.
	const uid = types.UID("u1")
	const goroutines = 32
	const iters = 200

	p := newTestProvisioner(t)
	p.config.MaxInflightPerFunction = 4

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iters {
				if p.tryAcquire(uid) {
					// simulate work, then release
					p.release(uid)
				}
			}
		}()
	}
	wg.Wait()

	// After all goroutines finish, the counter must be back to zero.
	v, ok := p.inflight.Load(uid)
	require.True(t, ok, "uid entry should exist")
	count := v.(*atomic.Int32)
	assert.Equal(t, int32(0), count.Load(), "inflight must drain to zero")
}

// ---------------------------------------------------------------------------
// eagerSpecialize
// ---------------------------------------------------------------------------

func TestProvisioner_eagerSpecialize(t *testing.T) {
	const uid = "u1"
	fn := provisionedFnWithUID("fn", uid, 3)

	t.Run("success patches provisioned label on pod", func(t *testing.T) {
		// Seed a pod WITHOUT the provisioned label; eagerSpecialize must
		// patch it in via the fake kubernetes clientset.
		pod := readyPod("warm-pod", uid)
		delete(pod.Labels, fv1.PROVISIONED_LABEL)
		gpm := &fakeGPM{svc: &fscache.FuncSvc{
			Function:          &fn.ObjectMeta,
			Address:           "10.0.0.1:8888",
			KubernetesObjects: []corev1.ObjectReference{podRef("warm-pod", "default")},
		}}
		p := newTestProvisionerWithGPM(t, gpm, fn, pod)

		err := p.eagerSpecialize(t.Context(), fn)
		require.NoError(t, err)

		got := getPod(t, p, "warm-pod")
		assert.Equal(t, fv1.PROVISIONED_VALUE, got.Labels[fv1.PROVISIONED_LABEL],
			"provisioned label must be patched in")
		assert.Equal(t, int64(1), gpm.calls.Load(), "GetFuncSvc called once")

		// GetFuncSvc booked a request slot on this pod (SetSvcValue bumps
		// activeRequests to 1, as if a caller were being served). Nobody is
		// actually using the pod — it is only being pre-warmed — so the
		// provisioner must hand the slot back. Without this the pod sits at
		// activeRequests=1 forever: PoolCache.ListAvailableValue only offers
		// svcs with activeRequests==0 to the idle reaper, so the pod is never
		// reaped once its provisioned label is cleared, and with the default
		// requestsPerPod=1 it is never handed to real traffic either.
		assert.Equal(t, int64(1), gpm.untaps.Load(), "must release the booked slot exactly once")
		assert.Equal(t, "10.0.0.1:8888", gpm.lastUntapAddr.Load(),
			"release must use the address of the FuncSvc that booked the slot")
		// A successful specialization already settled svcWaiting inside
		// SetSvcValue; marking a failure here too would double-decrement.
		assert.Zero(t, gpm.markFailures.Load(), "success path must not mark a specialization failure")
	})

	t.Run("GetFuncSvc error returns early, no patch", func(t *testing.T) {
		pod := readyPod("warm-pod", uid)
		delete(pod.Labels, fv1.PROVISIONED_LABEL)
		gpm := &fakeGPM{err: errors.New("pool exhausted")}
		p := newTestProvisionerWithGPM(t, gpm, fn, pod)

		err := p.eagerSpecialize(t.Context(), fn)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pool exhausted")

		got := getPod(t, p, "warm-pod")
		_, hasLabel := got.Labels[fv1.PROVISIONED_LABEL]
		assert.False(t, hasLabel, "no patch on GetFuncSvc failure")

		// GetFuncSvc never took a svcWaiting reservation in the first place
		// (only ReserveCapacity/GetFuncSvcFromCache do), so there is nothing
		// for MarkSpecializationFailure to settle here. Calling it anyway
		// would steal a concurrent caller's real reservation instead.
		assert.Zero(t, gpm.markFailures.Load(), "GetFuncSvc failure must not mark a specialization failure")
		// Nothing was allotted, so there is no slot to release. GetFuncSvc
		// returns (nil, err) on every error path, so there is no FuncSvc to
		// key an untap on either.
		assert.Zero(t, gpm.untaps.Load(), "nothing allotted, nothing to release")
	})

	t.Run("patch failure (pod missing) does not panic", func(t *testing.T) {
		// No pod in the clientset; the patch will 404. eagerSpecialize
		// logs the error but must not panic and must not return an error
		// (the pod is serving, just not labeled — accepted race).
		gpm := &fakeGPM{svc: &fscache.FuncSvc{
			Function:          &fn.ObjectMeta,
			Address:           "10.0.0.2:8888",
			KubernetesObjects: []corev1.ObjectReference{podRef("ghost-pod", "default")},
		}}
		p := newTestProvisionerWithGPM(t, gpm, fn) // no pods seeded

		err := p.eagerSpecialize(t.Context(), fn)
		// eagerSpecialize returns nil even on patch failure (the error is
		// only logged) — the pod is specialized and serving.
		assert.NoError(t, err)

		// The label patch failing says nothing about the specialization: the
		// pod IS specialized and serving, it just is not reaper-exempt. So the
		// booked slot still has to be released...
		assert.Equal(t, int64(1), gpm.untaps.Load(), "slot must be released even when the label patch fails")
		// ...but svcWaiting was already settled by SetSvcValue when GetFuncSvc
		// succeeded. Marking a failure here decrements it a second time, which
		// under-counts concurrencyUsed and under-enforces the concurrency cap.
		// MarkSpecializationFailure belongs on the GetFuncSvc error path only.
		assert.Zero(t, gpm.markFailures.Load(),
			"a failed label patch is not a failed specialization")
	})

	t.Run("non-pod KubernetesObjects are skipped", func(t *testing.T) {
		// A Service ref (Kind=Service) must not trigger a pod patch.
		gpm := &fakeGPM{svc: &fscache.FuncSvc{
			KubernetesObjects: []corev1.ObjectReference{
				{Kind: "Service", Name: "fn-svc", Namespace: "default"},
			},
		}}
		p := newTestProvisionerWithGPM(t, gpm, fn)

		err := p.eagerSpecialize(t.Context(), fn)
		assert.NoError(t, err, "non-pod refs are skipped, no error")
	})
}

// ---------------------------------------------------------------------------
// fireEagerSpecializations — pacing (invariant P5: MaxInflightPerFunction)
// ---------------------------------------------------------------------------

func TestProvisioner_fireEagerSpecializations_pacing(t *testing.T) {
	const uid = "u1"
	fn := provisionedFnWithUID("fn", uid, 10)

	// fakeGPM blocks every call until we close the channel. This lets us
	// observe peak concurrency while delta goroutines are launched.
	block := make(chan struct{})
	gpm := &fakeGPM{
		block: block,
		svc: &fscache.FuncSvc{
			KubernetesObjects: []corev1.ObjectReference{podRef("p", "default")},
		},
	}

	p := newTestProvisionerWithGPM(t, gpm, fn)
	p.config.MaxInflightPerFunction = 2

	// delta=5 but MaxInflight=2: only 2 calls should be in-flight at once.
	p.fireEagerSpecializations(t.Context(), fn, 5)

	// Wait long enough for the 2 admitted goroutines to enter GetFuncSvc.
	// tryAcquire is synchronous in fireEagerSpecializations' loop, so the
	// 3rd acquire fails immediately and the loop breaks — only 2 calls
	// reach GetFuncSvc.
	require.Eventually(t, func() bool {
		return gpm.calls.Load() == 2
	}, 2*time.Second, 10*time.Millisecond, "exactly MaxInflight calls admitted")
	assert.Equal(t, int64(2), gpm.peakConc.Load(),
		"peak concurrency must not exceed MaxInflightPerFunction")

	// Release the blocked calls; the remaining 3 are NOT queued (the loop
	// broke on the 3rd rejected tryAcquire). Verify no further calls.
	close(block)
	require.Eventually(t, func() bool {
		return gpm.concurrent.Load() == 0
	}, 2*time.Second, 10*time.Millisecond, "all admitted calls drain")
	assert.Equal(t, int64(2), gpm.calls.Load(),
		"rejected acquires must not retry — only MaxInflight calls total")
}

// ---------------------------------------------------------------------------
// reconcileFunction — warming branch (ready < target)
// ---------------------------------------------------------------------------

func TestProvisioner_reconcileFunction_warming(t *testing.T) {
	const uid = "u1"
	fn := provisionedFnWithUID("fn", uid, 3)

	// No provisioned pods yet (ready=0), target=3 → delta=3 eager calls.
	// Use a blocking fakeGPM so we can observe that fireEagerSpecializations
	// was invoked with delta=3 before status is published.
	block := make(chan struct{})
	gpm := &fakeGPM{
		block: block,
		svc: &fscache.FuncSvc{
			KubernetesObjects: []corev1.ObjectReference{podRef("p", "default")},
		},
	}
	p := newTestProvisionerWithGPM(t, gpm, fn)
	p.config.MaxInflightPerFunction = 4 // >= delta so all 3 are admitted

	p.reconcileFunction(t.Context(), fn)

	// All 3 eager specializations were admitted (MaxInflight=4 >= delta=3).
	require.Eventually(t, func() bool {
		return gpm.calls.Load() == 3
	}, 2*time.Second, 10*time.Millisecond, "delta=3 eager calls fired")

	// Status must be published with ready=0, target=3 (warming, not yet
	// ready — the pods are still blocking in GetFuncSvc).
	st := getFnStatus(t, p, "fn")
	assert.Equal(t, 3, st.ProvisionedTarget, "target published")
	assert.Equal(t, 0, st.ProvisionedReady, "ready is 0 during warm-up")

	// Unblock and let the goroutines drain so they don't leak.
	close(block)
	require.Eventually(t, func() bool {
		return gpm.concurrent.Load() == 0
	}, 2*time.Second, 10*time.Millisecond, "eager calls drain after unblock")
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
		require.NoError(t, p.updateFunctionStatus(t.Context(), fn, 2, 5, 5))
		st := getFnStatus(t, p, "fn")
		assert.Equal(t, 2, st.ProvisionedReady)
		assert.Equal(t, 5, st.ProvisionedTarget)
		assert.Equal(t, 5, st.ProvisionedSpecTarget)
		cond := metaFindCondition(st, fv1.FunctionConditionProvisioned)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, fv1.FunctionReasonProvisionedWarming, cond.Reason)
	})

	t.Run("satisfied: ready >= target", func(t *testing.T) {
		require.NoError(t, p.updateFunctionStatus(t.Context(), fn, 5, 5, 5))
		st := getFnStatus(t, p, "fn")
		assert.Equal(t, 5, st.ProvisionedReady)
		assert.Equal(t, 5, st.ProvisionedTarget)
		cond := metaFindCondition(st, fv1.FunctionConditionProvisioned)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, fv1.FunctionReasonProvisionedSatisfied, cond.Reason)
	})

	t.Run("oversatisfied: ready > target still True", func(t *testing.T) {
		require.NoError(t, p.updateFunctionStatus(t.Context(), fn, 7, 5, 5))
		st := getFnStatus(t, p, "fn")
		cond := metaFindCondition(st, fv1.FunctionConditionProvisioned)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
	})

	t.Run("disabled: target=0 sets False/ProvisionedDisabled", func(t *testing.T) {
		require.NoError(t, p.updateFunctionStatus(t.Context(), fn, 0, 0, 0))
		st := getFnStatus(t, p, "fn")
		assert.Equal(t, 0, st.ProvisionedReady)
		assert.Equal(t, 0, st.ProvisionedTarget)
		cond := metaFindCondition(st, fv1.FunctionConditionProvisioned)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, fv1.FunctionReasonProvisionedDisabled, cond.Reason)
	})

	t.Run("clamped: specTarget > target sets ProvisionedClamped reason", func(t *testing.T) {
		// spec target=50, effective target=20 (clamped by MaxPerFunction).
		// Warming: ready=5 < target=20 → False/ProvisionedClamped.
		require.NoError(t, p.updateFunctionStatus(t.Context(), fn, 5, 20, 50))
		st := getFnStatus(t, p, "fn")
		assert.Equal(t, 5, st.ProvisionedReady)
		assert.Equal(t, 20, st.ProvisionedTarget, "effective target clamped")
		assert.Equal(t, 50, st.ProvisionedSpecTarget, "spec target preserved")
		cond := metaFindCondition(st, fv1.FunctionConditionProvisioned)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, fv1.FunctionReasonProvisionedClamped, cond.Reason)
		assert.Contains(t, cond.Message, "spec target 50 exceeds the namespace cap")
		assert.Contains(t, cond.Message, "clamped to 20")
	})

	t.Run("clamped and satisfied: ready >= target still True but reason Clamped", func(t *testing.T) {
		// spec target=50, effective=20, ready=20 → True/ProvisionedClamped.
		require.NoError(t, p.updateFunctionStatus(t.Context(), fn, 20, 20, 50))
		st := getFnStatus(t, p, "fn")
		cond := metaFindCondition(st, fv1.FunctionConditionProvisioned)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, fv1.FunctionReasonProvisionedClamped, cond.Reason)
		assert.Contains(t, cond.Message, "spec target 50 exceeds the namespace cap")
		assert.Contains(t, cond.Message, "clamped to 20")
	})
}

// ---------------------------------------------------------------------------
// disableProvisioning (collapsed StopProvisioning + UpdateFunctionStatusZero)
// ---------------------------------------------------------------------------

func TestProvisioner_disableProvisioning(t *testing.T) {
	const uid = "u1"
	fn := provisionedFnWithUID("fn", uid, 2)
	pods := []*corev1.Pod{readyPod("a", uid), readyPod("b", uid)}
	p := newTestProvisionerWithPods(t, pods...)
	// Seed fn in both clients so updateFunctionStatus can Get+Update.
	p.crClient = crfake.NewClientBuilder().WithScheme(scheme()).
		WithObjects(toClientObjects(pods...)...).WithObjects(fn).Build()
	p.fissionClient = fClient.NewSimpleClientset(fn) //nolint:staticcheck
	// Pre-seed inflight counter to verify it's deleted.
	_, _ = p.inflight.LoadOrStore(types.UID(uid), new(atomic.Int32))

	p.disableProvisioning(t.Context(), fn)

	// Labels cleared on all pods.
	for _, name := range []string{"a", "b"} {
		got := getPod(t, p, name)
		assert.NotContains(t, got.Labels, fv1.PROVISIONED_LABEL, "%s cleared", name)
	}
	// Inflight counter deleted.
	_, ok := p.inflight.Load(types.UID(uid))
	assert.False(t, ok, "inflight entry deleted")
	// Status zeroed and condition False/Disabled.
	st := getFnStatus(t, p, "fn")
	assert.Equal(t, 0, st.ProvisionedReady)
	assert.Equal(t, 0, st.ProvisionedTarget)
	cond := metaFindCondition(st, fv1.FunctionConditionProvisioned)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, fv1.FunctionReasonProvisionedDisabled, cond.Reason)
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
	t.Run("empty executor type treated as poolmgr", func(t *testing.T) {
		fn := provisionedFn("e", 3)
		fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = ""
		list := &fv1.FunctionList{Items: []fv1.Function{*fn}}
		got := filterOptedFunctions(list)
		assert.Len(t, got, 1)
		assert.Equal(t, "e", got[0].Name)
	})
}

// ---------------------------------------------------------------------------
// lockFor / forget race (regression: fatal "sync: unlock of unlocked mutex")
// ---------------------------------------------------------------------------

// TestProvisioner_lockForget_race exercises the race that used to crash the
// executor process: reconcileFunction and disableProvisioning each resolved
// p.lockFor(fn.UID) twice — once to Lock, once (in the deferred call) to
// Unlock. A concurrent forget(fn.UID) (the Function-delete path) can Delete
// the reconcileLocks map entry between those two lookups, so the deferred
// Unlock resolves a brand-new, never-locked mutex and hits Go's fatal "sync:
// unlock of unlocked mutex" — not a recoverable panic, it kills the process
// outright (this is exactly what CI observed at provisioner.go:248 under PC
// create/delete churn). The fix resolves the lock once and reuses the same
// *sync.Mutex for both Lock and Unlock, so a forget in between cannot swap
// out the mutex from under an in-progress critical section.
//
// Must be run with -race: besides making any reintroduced unsynchronized
// access visible, -race slows/serializes scheduling in a way that widens the
// window for the old bug's interleaving. Run with -count=5 for coverage
// across scheduling variance — the old code could not reliably survive even
// one iteration of this loop; the new code must survive all of them.
func TestProvisioner_lockForget_race(t *testing.T) {
	const uid = types.UID("racy-fn-uid")
	// target=0 drives reconcileFunction/disableProvisioning down the fast
	// disableProvisioningLocked path (list 0 pods, zero the status) so the
	// loop can run many iterations quickly while still taking the lock.
	fn := provisionedFnWithUID("racy-fn", string(uid), 0)

	p := newTestProvisionerWithPods(t)
	p.crClient = crfake.NewClientBuilder().WithScheme(scheme()).WithObjects(fn).Build()
	p.fissionClient = fClient.NewSimpleClientset(fn) //nolint:staticcheck

	const iterations = 1000
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for range iterations {
			p.reconcileFunction(t.Context(), fn)
		}
	}()
	go func() {
		defer wg.Done()
		for range iterations {
			p.disableProvisioning(t.Context(), fn)
		}
	}()
	go func() {
		defer wg.Done()
		for range iterations {
			p.forget(uid)
		}
	}()

	wg.Wait()
	// Reaching this line is the assertion: the old two-lookup lockFor usage
	// could fatally crash the test binary partway through this loop (no
	// recover() catches a runtime fatal error); the fixed single-resolve
	// usage survives every interleaving forget can produce.
}

func TestProvisioner_RunZeroIntervalNoPanic(t *testing.T) {
	p := newTestProvisioner(t)
	p.config.ReconcileInterval = 0

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // exit immediately after first reconcileAll

	assert.NotPanics(t, func() { p.Run(ctx) })
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

// ---------------------------------------------------------------------------
// schedule tests
// ---------------------------------------------------------------------------

// setupWindowProvisioner creates a provisioner seeded with 5 unlabeled pods
// for a function with the given provisioned-concurrency windows, runs the
// initial reconcile, and asserts the starting target is 0.
// It returns the provisioner, the fake GPM (for call-count assertions),
// and the refreshed Function object.
func setupWindowProvisioner(t *testing.T, windows []fv1.ProvisionedWindow) (*Provisioner, *fakeGPM, *fv1.Function) {
	t.Helper()
	const uid = "u1"
	fn := provisionedFnWithUID("fn", uid, 0)
	fn.Spec.ProvisionedConcurrency.Windows = windows
	pods := []*corev1.Pod{}
	pref := []corev1.ObjectReference{}
	for i := range 5 {
		podName := fmt.Sprintf("pod%d", i)
		pod := readyPod(podName, uid)
		delete(pod.Labels, fv1.PROVISIONED_LABEL)
		pods = append(pods, pod)
		pref = append(pref, podRef(podName, "default"))
	}
	gpm := &fakeGPM{
		refs: pref,
	}
	p := newTestProvisionerWithGPM(t, gpm, fn, pods...)
	return p, gpm, fn
}

// TestProvisionerArmTransitionSyncBubbleTest tests a multi window function.
// It first checks if the target is zero at 12AM
// Then it check if the target is 3 at 9AM
// Then it checks if the target is 5 at 12PM
// Then it checks if the target is 3 at 2PM
// Then it checks if the target is 0 at 12AM next day
func TestProvisionerArmTransitionSyncBubbleTest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, gpm, fn := setupWindowProvisioner(t, []fv1.ProvisionedWindow{
			{Name: "window1", Start: "CRON_TZ=UTC 0 9 * * *", Duration: "8h", Target: 3},
			{Name: "window2", Start: "CRON_TZ=UTC 0 12 * * *", Duration: "2h", Target: 5},
		})
		p.reconcileFunction(t.Context(), fn)
		synctest.Wait()
		fn, err := p.fissionClient.CoreV1().Functions(fn.Namespace).Get(t.Context(), fn.Name, metav1.GetOptions{})
		assert.NoError(t, err, "no error expected fetching latest function")
		assert.Equal(t, 0, fn.Status.ProvisionedTarget, "expected 0 target, got: ", fn.Status.ProvisionedTarget)

		// check if window 1 provisioned target is updated
		synctest.Sleep(9 * time.Hour)
		fn, err = p.fissionClient.CoreV1().Functions(fn.Namespace).Get(t.Context(), fn.Name, metav1.GetOptions{})
		assert.NoError(t, err, "no error expected fetching latest function")
		assert.Equal(t, 3, fn.Status.ProvisionedTarget, "expected 3 target, got: ", fn.Status.ProvisionedTarget)
		labeled := countLabelledPods(t, p)
		assert.Equal(t, 3, labeled, "expected 3 pods to be labeled, instead got ", labeled)
		assert.Equal(t, int64(3), gpm.calls.Load())

		// update crClient cache
		updateCrClient(t, p)

		// check if window 2 provisioned target is updated
		synctest.Sleep(3 * time.Hour)
		fn, err = p.fissionClient.CoreV1().Functions(fn.Namespace).Get(t.Context(), fn.Name, metav1.GetOptions{})
		assert.NoError(t, err, "no error expected fetching latest function")
		assert.Equal(t, 5, fn.Status.ProvisionedTarget, "expected 5 target, got: ", fn.Status.ProvisionedTarget)
		labeled = countLabelledPods(t, p)
		assert.Equal(t, 5, labeled, "expected 5 pods to be labeled, instead got ", labeled)
		assert.Equal(t, int64(5), gpm.calls.Load())
		// update crClient cache
		updateCrClient(t, p)

		// window 2 is over, check if provisioned target is updated to window1 target
		synctest.Sleep(2 * time.Hour)
		fn, err = p.fissionClient.CoreV1().Functions(fn.Namespace).Get(t.Context(), fn.Name, metav1.GetOptions{})
		assert.NoError(t, err, "no error expected fetching latest function")
		assert.Equal(t, 3, fn.Status.ProvisionedTarget, "expected 3 target, got: ", fn.Status.ProvisionedTarget)
		labeled = countLabelledPods(t, p)
		assert.Equal(t, 3, labeled, "expected 3 pods to be labeled, instead got ", labeled)
		assert.Equal(t, int64(5), gpm.calls.Load())
		// update crClient cache
		updateCrClient(t, p)

		// window1 is over, check if provisioned target is updated to 0
		synctest.Sleep(3 * time.Hour)
		fn, err = p.fissionClient.CoreV1().Functions(fn.Namespace).Get(t.Context(), fn.Name, metav1.GetOptions{})
		assert.NoError(t, err, "no error expected fetching latest function")
		assert.Equal(t, 0, fn.Status.ProvisionedTarget, "expected 0 target, got: ", fn.Status.ProvisionedTarget)
		for i := range 5 {
			podName := fmt.Sprintf("pod%d", i)
			pod := getPod(t, p, podName)
			assert.Equal(t, "", pod.Labels[fv1.PROVISIONED_LABEL],
				"provisioned label must be removed")
		}
		assert.Equal(t, int64(5), gpm.calls.Load())
	})
}

// updateCrClient mirrors provisioned-label state from the
// kubernetesClient (where labelPods writes) into the crClient (where
// the provisioner reconciler reads via List). Call this between
// time-advance steps so the next reconcile sees up-to-date labels.
func updateCrClient(t *testing.T, p *Provisioner) {
	t.Helper()
	for i := range 5 {
		podName := fmt.Sprintf("pod%d", i)
		var cur corev1.Pod
		require.NoError(t, p.crClient.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: podName}, &cur))
		src := getPod(t, p, podName)
		if v, ok := src.Labels[fv1.PROVISIONED_LABEL]; ok {
			cur.Labels[fv1.PROVISIONED_LABEL] = v
		} else {
			delete(cur.Labels, fv1.PROVISIONED_LABEL)
		}
		require.NoError(t, p.crClient.Update(t.Context(), &cur))
	}
}

func TestProvisionerScheduleStopProvisionerRemoveProvisionedConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, gpm, fn := setupWindowProvisioner(t, []fv1.ProvisionedWindow{
			{Name: "window1", Start: "CRON_TZ=UTC 0 9 * * *", Duration: "8h", Target: 3},
		})
		p.reconcileFunction(t.Context(), fn)
		synctest.Wait()
		fn, err := p.fissionClient.CoreV1().Functions(fn.Namespace).Get(t.Context(), fn.Name, metav1.GetOptions{})
		assert.NoError(t, err, "no error expected fetching latest function")
		assert.Equal(t, 0, fn.Status.ProvisionedTarget, "expected 0 target, got: ", fn.Status.ProvisionedTarget)

		// check if window 1 provisioned target is updated
		synctest.Sleep(9 * time.Hour)
		fn, err = p.fissionClient.CoreV1().Functions(fn.Namespace).Get(t.Context(), fn.Name, metav1.GetOptions{})
		assert.NoError(t, err, "no error expected fetching latest function")
		assert.Equal(t, 3, fn.Status.ProvisionedTarget, "expected 3 target, got: ", fn.Status.ProvisionedTarget)
		labeled := countLabelledPods(t, p)
		assert.Equal(t, 3, labeled, "expected 3 pods to be labeled, instead got ", labeled)
		assert.Equal(t, int64(3), gpm.calls.Load())

		// update crClient cache
		updateCrClient(t, p)

		// get functions schedule
		sched := p.scheduleFor(fn)
		assert.NotNil(t, sched.timer)

		// remove provisionedconcurrency from function
		fn.Spec.ProvisionedConcurrency = nil
		fn, err = p.fissionClient.CoreV1().Functions(fn.Namespace).Update(t.Context(), fn, metav1.UpdateOptions{})
		require.NoError(t, err)
		p.reconcileFunction(t.Context(), fn)
		synctest.Wait()
		assert.Nil(t, fn.Spec.ProvisionedConcurrency, "expected provisionedconcurrency to be nil")
		_, hasSched := p.timers.Load(fn.UID)
		assert.False(t, hasSched, "expected disableProvisioning to drop the function's schedule entry")

		// update crClient cache
		updateCrClient(t, p)
		labeled = countLabelledPods(t, p)
		assert.Equal(t, 0, labeled, "expected 0 pods to be labeled, instead got ", labeled)
	})
}

// TestProvisionerScheduleStopProvisionerDeleteFunction tests whether
// deleting a function deletes the schedule and stops provisioning.
func TestProvisionerScheduleStopProvisionerDeleteFunction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, gpm, fn := setupWindowProvisioner(t, []fv1.ProvisionedWindow{
			{Name: "window1", Start: "CRON_TZ=UTC 0 9 * * *", Duration: "8h", Target: 3},
		})
		p.reconcileFunction(t.Context(), fn)
		synctest.Wait()
		fn, err := p.fissionClient.CoreV1().Functions(fn.Namespace).Get(t.Context(), fn.Name, metav1.GetOptions{})
		assert.NoError(t, err, "no error expected fetching latest function")
		assert.Equal(t, 0, fn.Status.ProvisionedTarget, "expected 0 target, got: ", fn.Status.ProvisionedTarget)

		// check if window 1 provisioned target is updated
		synctest.Sleep(9 * time.Hour)
		fn, err = p.fissionClient.CoreV1().Functions(fn.Namespace).Get(t.Context(), fn.Name, metav1.GetOptions{})
		assert.NoError(t, err, "no error expected fetching latest function")
		assert.Equal(t, 3, fn.Status.ProvisionedTarget, "expected 3 target, got: ", fn.Status.ProvisionedTarget)
		labeled := countLabelledPods(t, p)
		assert.Equal(t, 3, labeled, "expected 3 pods to be labeled, instead got ", labeled)
		assert.Equal(t, int64(3), gpm.calls.Load())

		// update crClient cache
		updateCrClient(t, p)

		// get functions schedule
		sched := p.scheduleFor(fn)
		assert.NotNil(t, sched.timer)

		// delete function
		err = p.fissionClient.CoreV1().Functions(fn.Namespace).Delete(t.Context(), fn.Name, metav1.DeleteOptions{})
		require.NoError(t, err)
		p.forget(fn.UID)
		p.clearProvisionedLabels(t.Context(), fn, -1)
		synctest.Wait()

		_, ok := p.timers.Load(fn.UID)
		assert.False(t, ok, "schedule entry should be deleted, not just recreated empty")

		// update crClient cache
		updateCrClient(t, p)
		labeled = countLabelledPods(t, p)
		assert.Equal(t, 0, labeled, "expected 0 pods to be labeled, instead got ", labeled)
	})
}

// countLabelledPods counts the number of pods with the provisioned label set to "true".
func countLabelledPods(t *testing.T, p *Provisioner) int {
	t.Helper()
	labeled := 0
	for i := range 5 {
		podName := fmt.Sprintf("pod%d", i)
		pod := getPod(t, p, podName)
		if pod.Labels[fv1.PROVISIONED_LABEL] == "true" {
			labeled++
		}
	}
	return labeled
}

// TestProvisionerScheduleStopProvisionerDeleteFunctionWhileArming tests the race condition
// where a schedule and a delete function call happens at the same time.
func TestProvisionerScheduleStopProvisionerDeleteFunctionWhileArming(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, _, fn := setupWindowProvisioner(t, []fv1.ProvisionedWindow{
			{Name: "window1", Start: "CRON_TZ=UTC 0 9 * * *", Duration: "8h", Target: 3},
		})
		react := atomic.Bool{}
		// SAFETY: the test builds p with a fake *fClient.Clientset.
		fakeClient := p.fissionClient.(*fClient.Clientset)
		block := make(chan struct{})
		fakeClient.PrependReactor("get", "functions", func(_ k8stesting.Action) (handled bool, result runtime.Object, err error) {
			react.Store(true)
			select {
			case <-block:
			case <-t.Context().Done():
				return true, nil, t.Context().Err()
			}
			return false, nil, nil
		})
		synctest.Wait()
		time.Sleep(9 * time.Hour)
		gvr := schema.GroupVersionResource{
			Group:    "fission.io",
			Version:  "v1",
			Resource: "functions",
		}
		require.NoError(t, fakeClient.Tracker().Delete(gvr, fn.Namespace, fn.Name, metav1.DeleteOptions{}))
		p.forget(fn.UID)
		p.clearProvisionedLabels(t.Context(), fn, -1)
		close(block)
		synctest.Wait()
		_, ok := p.timers.Load(fn.UID)
		assert.False(t, ok, "schedule entry should be deleted, not just recreated empty")
		_, err := p.fissionClient.CoreV1().Functions(fn.Namespace).Get(t.Context(), fn.Name, metav1.GetOptions{})
		assert.Error(t, err)
		assert.True(t, apierrors.IsNotFound(err))
		labelled := countLabelledPods(t, p)
		assert.Equal(t, 0, labelled)

		assert.True(t, react.Load())
	})
}

// TestProvisionerScheduleTickerTimer proves that two independent triggers of
// reconcileFunction for the same function — a real fired per-function timer
// (armTransition's time.AfterFunc) and a rival caller standing in for the
// ticker's reconcileAll pass — never both run reconcileFunctionLocked at
// once. lockFor(fn.UID) must force them to serialize.
//
// GetFuncSvc (fakeGPM) is deliberately NOT the probe point: fireEagerSpecializations
// fires each pod's eagerSpecialize/GetFuncSvc call from its own background
// goroutine, so a count there conflates "how many pods this one reconcile is
// warming" with "how many reconciles are running" — it would climb even with
// the lock working perfectly. Instead this test intercepts crClient's List
// (via crfake's WithInterceptorFuncs): countProvisionedPods calls List
// exactly once per reconcileFunctionLocked call, synchronously, right after
// the lock is taken and before any pod work starts. So listCalls climbing
// past what a checkpoint expects can only mean two reconciles genuinely had
// the lock at overlapping times — a direct proxy for lock ordering.
//
// The test is split into two non-overlapping phases instead of one long
// synctest.Sleep spanning both triggers. That split is forced by a real
// synctest limitation, not a style choice: synctest.Wait/time.Sleep can only
// return once every other goroutine in the bubble is "durably blocked" —
// which per testing/synctest's doc comment on Wait means blocked on a
// channel op, select, sync.Cond.Wait, or time.Sleep. A goroutine contending
// a plain sync.Mutex.Lock — exactly what happens when a second
// reconcileFunction call waits on lockFor — does NOT count as durably
// blocked. If a mutex-contended goroutine and a channel-blocked goroutine
// both exist while a Sleep/Wait needs to resolve, the bubble can never be
// judged quiescent and the test hangs for real, caught only by go test's
// -timeout (verified empirically: an earlier draft of this test hung this
// exact way). Each phase below fully releases its blocked caller — close
// the channel, then Wait — before the next contender is introduced, so a
// mutex-contended goroutine is never left needing to survive across a
// Sleep.
//
// Reusing a plain variable (a channel, a *Provisioner, a struct field) that
// a still-live background goroutine might read is ALSO unsafe here, even
// across phase boundaries and even when the reassignment happens earlier in
// the same goroutine's program order: an earlier draft that swapped
// p.crClient mid-test, and one that reassigned a captured listBlock
// variable, both tripped -race, because synctest's fake-time ordering is
// not a synchronization primitive the race detector recognizes. Each phase
// below therefore builds a fully independent *Provisioner (its own
// crClient, its own listBlock/listCalls closure) rather than mutating
// shared state — and phase 1's Provisioner has its own real, live
// armTransition timer explicitly stopped via p.forget(fn.UID) before phase
// 2 starts, otherwise that leftover timer independently fires during phase
// 2's sleep and inflates the call count (this was caught empirically: the
// count came in one higher than expected until forget was added).
func TestProvisionerScheduleTickerTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const uid = "u1"
		// Window opens 9h into the fake clock (which always starts at
		// midnight UTC 2000-01-01 inside synctest) and asks for 3 warm pods
		// while open. 5 unlabeled pods (no PROVISIONED_LABEL) exist, all
		// tagged for this function's UID, so ready starts at 0 and there's
		// real work for fireEagerSpecializations once a reconcile sees the
		// window open. Both phases reuse this same fn/pods/uid — forget()
		// below is what makes that safe, so there's no need for a second
		// function identity.
		fn := provisionedFnWithUID("fn", uid, 0)
		fn.Spec.ProvisionedConcurrency.Windows = []fv1.ProvisionedWindow{
			{Name: "window1", Start: "CRON_TZ=UTC 0 9 * * *", Duration: "8h", Target: 3},
		}
		pods := []*corev1.Pod{}
		pref := []corev1.ObjectReference{}
		for i := range 5 {
			podName := fmt.Sprintf("pod%d", i)
			pod := readyPod(podName, uid)
			delete(pod.Labels, fv1.PROVISIONED_LABEL)
			pods = append(pods, pod)
			pref = append(pref, podRef(podName, "default"))
		}

		// newProbe builds one fully independent crClient + interceptor per
		// phase: every List of a *PodList (i.e. every countProvisionedPods
		// call) increments the returned counter and then parks on the
		// returned channel until the test closes it. Function-list calls
		// (reconcileAll's own List, of *FunctionList) pass straight
		// through — blocking those too would stall the ticker loop on a
		// call unrelated to the thing being probed, an earlier bug in this
		// test. Each phase gets its own counter/channel/crClient rather
		// than reassigning shared ones, since a background goroutine (the
		// fired timer's callback) reading a variable the main goroutine
		// just reassigned is a data race by Go's memory model even when
		// synctest's fake-time ordering makes it safe in practice.
		newProbe := func() (*atomic.Int64, chan struct{}, client.Client) {
			var listCalls atomic.Int64
			listBlock := make(chan struct{})
			crClient := crfake.NewClientBuilder().WithScheme(scheme()).
				WithObjects(toClientObjects(pods...)...).
				WithObjects(fn).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if _, isPodList := list.(*corev1.PodList); !isPodList {
							return cl.List(ctx, list, opts...)
						}
						listCalls.Add(1)
						select {
						case <-listBlock:
						case <-ctx.Done():
							return ctx.Err()
						}
						return cl.List(ctx, list, opts...)
					},
				}).Build()
			return &listCalls, listBlock, crClient
		}

		newTestProvisioner := func(crClient client.Client) *Provisioner {
			return NewProvisioner(
				logr.Discard(),
				&fakeGPM{refs: pref},
				fClient.NewSimpleClientset(fn), //nolint:staticcheck
				k8sfake.NewSimpleClientset(toRuntimeObjects(pods...)...),
				crClient,
				defaultCfg,
			)
		}

		// Phase 1: sanity-check the lock in isolation, before any window or
		// timer is meant to matter. At time 0 the window is closed, so
		// this call takes the target-0/disableProvisioningLocked path — it
		// is not simulating the ticker or the timer, just proving one
		// caller can reach and get stuck at the probe point on its own.
		// reconcileFunctionLocked calls armTransition unconditionally
		// before the target-0 check, so this call also arms a real 9h
		// timer as a side effect — forget() below stops it before phase 2,
		// so it can't fire independently and corrupt phase 2's count.
		listCalls1, listBlock1, crClient1 := newProbe()
		p1 := newTestProvisioner(crClient1)

		go p1.reconcileFunction(t.Context(), fn)
		synctest.Wait()
		assert.Equal(t, int64(1), listCalls1.Load())
		close(listBlock1)
		p1.forget(fn.UID)
		synctest.Wait()

		// Phase 2: the real collision. A fresh Provisioner (own crClient,
		// own probe) avoids reassigning anything p1's now-stopped timer
		// goroutine could have read. p2.armTransition(fn) arms its own
		// timer explicitly, since this Provisioner has never reconciled
		// yet. Sleeping to just past the window-open edge lets that timer
		// fire for real — this is genuinely armTransition's time.AfterFunc
		// callback, not a stand-in for anything.
		listCalls2, listBlock2, crClient2 := newProbe()
		p2 := newTestProvisioner(crClient2)
		p2.armTransition(fn)

		synctest.Sleep(9*time.Hour + time.Nanosecond)
		// The fired timer's callback is the one stuck here — the only
		// contender that exists at this point, so exactly 1 call.
		assert.Equal(t, int64(1), listCalls2.Load())

		// The rival caller: stands in for what would be the ticker's
		// concurrent reconcileAll pass. Starting it here, then closing
		// listBlock2 with no blocking call in between, is what keeps this
		// safe under synctest — see the function doc comment above.
		go p2.reconcileFunction(t.Context(), fn)
		close(listBlock2)
		synctest.Wait()

		// Both reconciles reached the probe point, one after the other —
		// proof they were serialized by lockFor, not run concurrently.
		assert.Equal(t, int64(2), listCalls2.Load())
	})
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package idle

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/utils/metrics/metricstest"
)

func TestGetDeploymentObj(t *testing.T) {
	t.Parallel()
	objs := []apiv1.ObjectReference{
		{Kind: "Service", Name: "svc"},
		{Kind: "Deployment", Name: "depl", Namespace: "ns"},
	}
	got := getDeploymentObj(objs)
	require.NotNil(t, got)
	assert.Equal(t, "depl", got.Name)

	assert.Nil(t, getDeploymentObj([]apiv1.ObjectReference{{Kind: "Service"}}), "no deployment returns nil")
}

func TestIdleThreshold(t *testing.T) {
	t.Parallel()
	def := 2 * time.Minute
	assert.Equal(t, def, idleThreshold(nil, def), "nil function uses default")

	fn := &fv1.Function{}
	assert.Equal(t, def, idleThreshold(fn, def), "unset IdleTimeout uses default")

	timeout := 30
	fn.Spec.IdleTimeout = &timeout
	assert.Equal(t, 30*time.Second, idleThreshold(fn, def), "IdleTimeout overrides default")
}

// fakeStrategy records which function services Reap was invoked on.
type fakeStrategy struct {
	execType fv1.ExecutorType
	idle     []*fscache.FuncSvc
	prepErr  error

	mu     sync.Mutex
	reaped []string
}

func (f *fakeStrategy) Name() string                          { return "fake" }
func (f *fakeStrategy) ExecutorType() fv1.ExecutorType        { return f.execType }
func (f *fakeStrategy) Interval() time.Duration               { return time.Hour }
func (f *fakeStrategy) ListIdle() ([]*fscache.FuncSvc, error) { return f.idle, nil }
func (f *fakeStrategy) Prepare(_ context.Context) error       { return f.prepErr }
func (f *fakeStrategy) Reap(_ context.Context, fsvc *fscache.FuncSvc) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reaped = append(f.reaped, fsvc.Name)
	return nil
}

func fsvc(name string, executor fv1.ExecutorType) *fscache.FuncSvc {
	return &fscache.FuncSvc{
		Name:     name,
		Executor: executor,
		Function: &metav1.ObjectMeta{Name: name, Namespace: "default"},
	}
}

// TestReapLag pins what the #1989 metric measures. The distinction that matters:
// it is the reaper's own responsiveness — how late it was — not how long the
// function sat idle. A function idle for an hour under a one-hour timeout is
// reaped ON TIME and must record ~0, or the metric just restates the timeout
// back to whoever configured it.
func TestReapLag(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		idleFor   time.Duration
		threshold time.Duration
		want      float64
	}{
		{"reaped on the deadline is zero lag", 2 * time.Minute, 2 * time.Minute, 0},
		{"one tick late", 125 * time.Second, 2 * time.Minute, 5},
		{"long idle timeout still reports only the delay", time.Hour + 3*time.Second, time.Hour, 3},
		{"saturated reaper falling minutes behind", 10 * time.Minute, 2 * time.Minute, 480},
		{"per-function timeout shorter than the default", 35 * time.Second, 30 * time.Second, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.InDelta(t, tc.want, reapLag(tc.idleFor, tc.threshold), 1e-9)
		})
	}
}

func TestReaperReapOnce_FiltersByExecutorType(t *testing.T) {
	t.Parallel()
	s := &fakeStrategy{
		execType: fv1.ExecutorTypeNewdeploy,
		idle: []*fscache.FuncSvc{
			fsvc("nd1", fv1.ExecutorTypeNewdeploy),
			fsvc("pool1", fv1.ExecutorTypePoolmgr),
			fsvc("nd2", fv1.ExecutorTypeNewdeploy),
		},
	}
	r := NewReaper(logr.Discard(), s)
	r.reapOnce(t.Context(), s)

	s.mu.Lock()
	defer s.mu.Unlock()
	sort.Strings(s.reaped)
	assert.Equal(t, []string{"nd1", "nd2"}, s.reaped, "only matching executor type is reaped")
}

func TestReaperReapOnce_SkipsTickOnPrepareError(t *testing.T) {
	t.Parallel()
	s := &fakeStrategy{
		execType: fv1.ExecutorTypeNewdeploy,
		idle:     []*fscache.FuncSvc{fsvc("nd1", fv1.ExecutorTypeNewdeploy)},
		prepErr:  assert.AnError,
	}
	r := NewReaper(logr.Discard(), s)
	r.reapOnce(t.Context(), s)
	assert.Empty(t, s.reaped, "a Prepare error skips the whole tick")
}

func TestPoolDeleteStrategy_Skips(t *testing.T) {
	t.Parallel()
	newStrategy := func() (*PoolDeleteStrategy, *fscache.FunctionServiceCache) {
		fc := fscache.MakeFunctionServiceCache(logr.Discard())
		s := NewPoolDeleteStrategy(logr.Discard(), nil, fc, k8sfake.NewSimpleClientset(), 2*time.Minute, 5*time.Second, false, nil)
		s.envUIDs = map[types.UID]struct{}{}
		s.fnByUID = map[types.UID]fv1.Function{}
		s.provisionedPods = map[string]struct{}{}
		return s, fc
	}

	idleFsvc := func() *fscache.FuncSvc {
		return &fscache.FuncSvc{
			Name:        "p",
			Executor:    fv1.ExecutorTypePoolmgr,
			Function:    &metav1.ObjectMeta{Name: "fn", Namespace: "default"},
			Environment: &fv1.Environment{},
			Atime:       time.Now().Add(-time.Hour),
			KubernetesObjects: []apiv1.ObjectReference{
				{Kind: "Pod", Namespace: "default", Name: "p"},
			},
		}
	}

	t.Run("websocket connection is skipped", func(t *testing.T) {
		s, fc := newStrategy()
		f := idleFsvc()
		fc.WebsocketFsvc.Store(f.Name, true)
		require.NoError(t, s.Reap(t.Context(), f))
	})

	t.Run("infinite functions-per-container is skipped", func(t *testing.T) {
		s, _ := newStrategy()
		f := idleFsvc()
		f.Environment.Spec.AllowedFunctionsPerContainer = fv1.AllowedFunctionsPerContainerInfinite
		require.NoError(t, s.Reap(t.Context(), f))
	})

	t.Run("not-yet-idle is skipped", func(t *testing.T) {
		s, _ := newStrategy()
		f := idleFsvc()
		f.Atime = time.Now() // fresh
		require.NoError(t, s.Reap(t.Context(), f))
	})

	t.Run("provisioned pod (labeled) is skipped", func(t *testing.T) {
		s, fc := newStrategy()
		const uid = types.UID("fn-uid-1")
		s.fnByUID[uid] = fv1.Function{
			Name: "fn", Namespace: "default", UID: uid,
			Spec: fv1.FunctionSpec{
				ProvisionedConcurrency: &fv1.ProvisionedConcurrencyConfig{Target: 2},
			},
		}
		// Pod carries the provisioned label → provisioner wants it → exempt.
		s.provisionedPods["default/p"] = struct{}{}
		f := idleFsvc()
		f.Function.UID = uid
		f.Atime = time.Now().Add(-time.Hour) // very idle
		fc.PodToFsvc.Store(f.Name, f)
		require.NoError(t, s.Reap(t.Context(), f))
		_, ok := fc.PodToFsvc.Load(f.Name)
		assert.True(t, ok, "labeled pod must survive Reap")
	})

	t.Run("unlabeled pod of provisioned function is reaped (PR2 per-pod-label fix)", func(t *testing.T) {
		s, fc := newStrategy()
		const uid = types.UID("fn-uid-1")
		s.fnByUID[uid] = fv1.Function{
			Name: "fn", Namespace: "default", UID: uid,
			Spec: fv1.FunctionSpec{
				ProvisionedConcurrency: &fv1.ProvisionedConcurrencyConfig{Target: 2},
			},
		}
		// provisionedPods is empty — provisioner cleared the label (window close /
		// target drop). Pod must be reaped despite the function opting into PC.
		f := idleFsvc()
		f.Function.UID = uid
		f.Atime = time.Now().Add(-time.Hour) // very idle
		fc.PodToFsvc.Store(f.Name, f)
		require.NoError(t, s.Reap(t.Context(), f))
		_, ok := fc.PodToFsvc.Load(f.Name)
		assert.False(t, ok, "unlabeled pod of PC function must be reaped")
	})

	t.Run("non-provisioned function is reaped normally", func(t *testing.T) {
		s, fc := newStrategy()
		const uid = types.UID("fn-uid-2")
		// Function does NOT opt into provisioned concurrency.
		s.fnByUID[uid] = fv1.Function{
			Name: "fn", Namespace: "default", UID: uid,
		}
		f := idleFsvc()
		f.Function.UID = uid
		f.Atime = time.Now().Add(-time.Hour) // very idle
		// Seed PodToFsvc so we can assert the entry is removed by Reap.
		fc.PodToFsvc.Store(f.Name, f)
		require.NoError(t, s.Reap(t.Context(), f))
		// PodToFsvc entry must be gone — Reap proceeded past the PC
		// exemption and called DeleteOldPoolCache → DeleteFunctionSvc,
		// which removes the PodToFsvc entry.
		_, ok := fc.PodToFsvc.Load(f.Name)
		assert.False(t, ok, "PodToFsvc entry must be removed by Reap when PC is not enabled")
	})
}

func TestScaleDownStrategy_Reap(t *testing.T) {
	t.Parallel()
	const ns, deplName, fnName = "default", "fn-depl", "fn"

	makeFn := func(minScale int) *fv1.Function {
		fn := &fv1.Function{Name: fnName, Namespace: ns}
		fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale = minScale
		return fn
	}
	makeFsvc := func() *fscache.FuncSvc {
		return &fscache.FuncSvc{
			Name:        "p",
			Executor:    fv1.ExecutorTypeNewdeploy,
			Function:    &metav1.ObjectMeta{Name: fnName, Namespace: ns},
			Environment: &fv1.Environment{},
			Atime:       time.Now().Add(-time.Hour),
			KubernetesObjects: []apiv1.ObjectReference{
				{Kind: "Deployment", Name: deplName, Namespace: ns},
			},
		}
	}
	deployWithReplicas := func(r int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			Name: deplName, Namespace: ns,
			Spec: appsv1.DeploymentSpec{Replicas: &r},
		}
	}
	// captureScale records the replica count of any scale update.
	captureScale := func(kc *k8sfake.Clientset, got *int32) {
		kc.PrependReactor("update", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.GetSubresource() != "scale" {
				return false, nil, nil
			}
			// SAFETY: this reactor is registered for "update" on the scale subresource, so the action is an UpdateAction carrying a *Scale.
			sc := action.(k8stesting.UpdateAction).GetObject().(*autoscalingv1.Scale)
			*got = sc.Spec.Replicas
			return true, sc, nil
		})
	}

	t.Run("scales an idle deployment down to MinScale", func(t *testing.T) {
		kc := k8sfake.NewSimpleClientset(deployWithReplicas(3))
		var scaled int32 = -1
		captureScale(kc, &scaled)
		s := NewScaleDownStrategy(logr.Discard(), fv1.ExecutorTypeNewdeploy,
			fissionfake.NewClientset(makeFn(1)), fscache.MakeFunctionServiceCache(logr.Discard()), kc, 2*time.Minute, 5*time.Second, true)

		require.NoError(t, s.Reap(t.Context(), makeFsvc()))
		assert.Equal(t, int32(1), scaled, "deployment scaled to MinScale")
	})

	t.Run("no scale when already at or below MinScale", func(t *testing.T) {
		kc := k8sfake.NewSimpleClientset(deployWithReplicas(1))
		var scaled int32 = -1
		captureScale(kc, &scaled)
		s := NewScaleDownStrategy(logr.Discard(), fv1.ExecutorTypeNewdeploy,
			fissionfake.NewClientset(makeFn(1)), fscache.MakeFunctionServiceCache(logr.Discard()), kc, 2*time.Minute, 5*time.Second, true)

		require.NoError(t, s.Reap(t.Context(), makeFsvc()))
		assert.Equal(t, int32(-1), scaled, "no scale issued when already at MinScale")
	})

	t.Run("missing function is not an error", func(t *testing.T) {
		kc := k8sfake.NewSimpleClientset(deployWithReplicas(3))
		s := NewScaleDownStrategy(logr.Discard(), fv1.ExecutorTypeNewdeploy,
			fissionfake.NewClientset(), fscache.MakeFunctionServiceCache(logr.Discard()), kc, 2*time.Minute, 5*time.Second, true)

		require.NoError(t, s.Reap(t.Context(), makeFsvc()), "a deleted function is handled by the deploy manager, not the reaper")
	})
}

// TestPoolDeleteStrategy_DrainThenDelete: with drainBeforeDelete on, reaping
// first removes the served label (the pod leaves its function Service's
// EndpointSlices) and defers the actual delete past a drain grace.
func TestPoolDeleteStrategy_DrainThenDelete(t *testing.T) {
	t.Parallel()
	pod := &apiv1.Pod{
		Name:      "fn-pod",
		Namespace: "default",
		Labels:    map[string]string{fv1.SERVED_LABEL: "true", "managed": "false"},
	}
	kc := k8sfake.NewSimpleClientset(pod)
	fc := fscache.MakeFunctionServiceCache(logr.Discard())
	s := NewPoolDeleteStrategy(logr.Discard(), nil, fc, kc, 2*time.Minute, 5*time.Second, true, nil)
	s.fnByUID = map[types.UID]fv1.Function{}

	f := &fscache.FuncSvc{
		Name:        "fn-pod",
		Executor:    fv1.ExecutorTypePoolmgr,
		Function:    &metav1.ObjectMeta{Name: "fn", Namespace: "default", UID: "u1"},
		Environment: &fv1.Environment{},
		Atime:       time.Now().Add(-time.Hour),
		KubernetesObjects: []apiv1.ObjectReference{
			{Kind: "pod", Name: "fn-pod", Namespace: "default"},
		},
	}

	s.drainThenDelete(t.Context(), f)

	got, err := kc.CoreV1().Pods("default").Get(t.Context(), "fn-pod", metav1.GetOptions{})
	require.NoError(t, err, "the pod must NOT be deleted during the drain grace")
	_, served := got.Labels[fv1.SERVED_LABEL]
	assert.False(t, served, "the served label must be removed so the pod leaves the slices")
	assert.Equal(t, "false", got.Labels["managed"], "other labels are untouched")
}

// TestReaperMetrics asserts what the instruments record AT THEIR CALL SITES,
// which is where the #1989 metrics can be wrong in ways a pure-function test of
// reapLag cannot see: fired on a skip, missed on a real reap, or a whole failed
// pass recording nothing at all.
//
// metricstest installs a MeterProvider globally and only the FIRST install in a
// test binary takes effect, so every metric assertion in this package has to
// live in this one test rather than being spread across the others.
func TestReaperMetrics(t *testing.T) {
	reg := metricstest.SetupMeter(t)

	// sampleCount reads a series' counter value or histogram sample count,
	// returning 0 when the series was never recorded.
	sampleCount := func(name string, labels map[string]string) float64 {
		t.Helper()
		families, err := reg.Gather()
		require.NoError(t, err)
		for _, fam := range families {
			if fam.GetName() != name {
				continue
			}
			m := metricstest.FindMetric(fam, labels)
			if m == nil {
				return 0
			}
			if m.GetCounter() != nil {
				return m.GetCounter().GetValue()
			}
			return float64(m.GetHistogram().GetSampleCount())
		}
		return 0
	}

	const lag = "fission_executor_idle_reap_lag_seconds"
	const errs = "fission_executor_idle_reap_errors_total"
	newdeploy := map[string]string{"executor_type": "newdeploy"}

	const ns, deplName, fnName = "default", "fn-depl", "fn"
	fn := &fv1.Function{Name: fnName, Namespace: ns}
	fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale = 1
	newFsvc := func() *fscache.FuncSvc {
		return &fscache.FuncSvc{
			Name: "p", Executor: fv1.ExecutorTypeNewdeploy,
			Function:    &metav1.ObjectMeta{Name: fnName, Namespace: ns},
			Environment: &fv1.Environment{},
			Atime:       time.Now().Add(-time.Hour),
			KubernetesObjects: []apiv1.ObjectReference{
				{Kind: "Deployment", Name: deplName, Namespace: ns},
			},
		}
	}
	newDeploy := func(r int32) *appsv1.Deployment {
		return &appsv1.Deployment{Name: deplName, Namespace: ns, Spec: appsv1.DeploymentSpec{Replicas: &r}}
	}
	newScaleDown := func(kc *k8sfake.Clientset) *ScaleDownStrategy {
		return NewScaleDownStrategy(logr.Discard(), fv1.ExecutorTypeNewdeploy,
			fissionfake.NewClientset(fn), fscache.MakeFunctionServiceCache(logr.Discard()),
			kc, 2*time.Minute, 5*time.Second, true)
	}

	t.Run("a real reap records one lag sample", func(t *testing.T) {
		before := sampleCount(lag, newdeploy)
		require.NoError(t, newScaleDown(k8sfake.NewSimpleClientset(newDeploy(3))).Reap(t.Context(), newFsvc()))
		assert.InDelta(t, before+1, sampleCount(lag, newdeploy), 0.001, "scaling down to MinScale is a reap")
	})

	t.Run("a skip records nothing", func(t *testing.T) {
		before := sampleCount(lag, newdeploy)
		// Already at MinScale: nothing is released, so it is not a reap.
		require.NoError(t, newScaleDown(k8sfake.NewSimpleClientset(newDeploy(1))).Reap(t.Context(), newFsvc()))
		assert.InDelta(t, before, sampleCount(lag, newdeploy), 0.001, "an at-MinScale deployment must not count as reaped")
	})

	t.Run("a failed pass is counted at its stage", func(t *testing.T) {
		// The case that was invisible before: a Prepare failure aborts the whole
		// pass, so nothing is reaped and no lag is sampled. Without a
		// stage-labelled error the series look exactly like a quiet cluster.
		prepareFail := map[string]string{"executor_type": "newdeploy", "stage": "prepare"}
		before := sampleCount(errs, prepareFail)

		fc := fissionfake.NewClientset()
		fc.PrependReactor("list", "environments", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, assert.AnError
		})
		s := NewScaleDownStrategy(logr.Discard(), fv1.ExecutorTypeNewdeploy, fc,
			fscache.MakeFunctionServiceCache(logr.Discard()), k8sfake.NewSimpleClientset(),
			2*time.Minute, 5*time.Second, true)

		NewReaper(logr.Discard(), s).reapOnce(t.Context(), s)
		assert.InDelta(t, before+1, sampleCount(errs, prepareFail), 0.001,
			"a Prepare failure must increment the error counter at stage=prepare")
	})
}

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
		return s, fc
	}

	idleFsvc := func() *fscache.FuncSvc {
		return &fscache.FuncSvc{
			Name:        "p",
			Executor:    fv1.ExecutorTypePoolmgr,
			Function:    &metav1.ObjectMeta{Name: "fn", Namespace: "default"},
			Environment: &fv1.Environment{},
			Atime:       time.Now().Add(-time.Hour),
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

	t.Run("provisioned-concurrency function is skipped (PR1 function-level exemption)", func(t *testing.T) {
		s, fc := newStrategy()
		const uid = types.UID("fn-uid-1")
		// Function opts into provisioned concurrency (target=2).
		s.fnByUID[uid] = fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default", UID: uid},
			Spec: fv1.FunctionSpec{
				ProvisionedConcurrency: &fv1.ProvisionedConcurrencyConfig{Target: 2},
			},
		}
		f := idleFsvc()
		f.Function.UID = uid
		f.Atime = time.Now().Add(-time.Hour) // very idle
		// Seed PodToFsvc so we can assert the entry survives Reap.
		fc.PodToFsvc.Store(f.Name, f)
		require.NoError(t, s.Reap(t.Context(), f))
		// PodToFsvc entry must survive — the PC exemption prevented reaping
		// (DeleteFunctionSvc would have removed it).
		_, ok := fc.PodToFsvc.Load(f.Name)
		assert.True(t, ok, "PodToFsvc entry must survive Reap when PC is enabled")
	})

	t.Run("non-provisioned function is reaped normally", func(t *testing.T) {
		s, fc := newStrategy()
		const uid = types.UID("fn-uid-2")
		// Function does NOT opt into provisioned concurrency.
		s.fnByUID[uid] = fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default", UID: uid},
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
		fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: fnName, Namespace: ns}}
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
			ObjectMeta: metav1.ObjectMeta{Name: deplName, Namespace: ns},
			Spec:       appsv1.DeploymentSpec{Replicas: &r},
		}
	}
	// captureScale records the replica count of any scale update.
	captureScale := func(kc *k8sfake.Clientset, got *int32) {
		kc.PrependReactor("update", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if action.GetSubresource() != "scale" {
				return false, nil, nil
			}
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fn-pod",
			Namespace: "default",
			Labels:    map[string]string{fv1.SERVED_LABEL: "true", "managed": "false"},
		},
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

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

type fakeFuncMgr struct {
	deleted      []crd.CacheKeyURG
	refreshed    []string
	istioCreated []string
	istioDeleted []string
}

func (f *fakeFuncMgr) markFuncDeleted(k crd.CacheKeyURG) { f.deleted = append(f.deleted, k) }
func (f *fakeFuncMgr) refreshFuncPods(_ context.Context, fn *fv1.Function) error {
	f.refreshed = append(f.refreshed, fn.Name)
	return nil
}
func (f *fakeFuncMgr) createIstioServiceForFunction(_ context.Context, fn *fv1.Function) error {
	f.istioCreated = append(f.istioCreated, fn.Name)
	return nil
}
func (f *fakeFuncMgr) deleteIstioServiceForFunction(_ context.Context, fn *fv1.Function) error {
	f.istioDeleted = append(f.istioDeleted, fn.Name)
	return nil
}

type fakeRSCleaner struct{ processed []string }

func (f *fakeRSCleaner) processReplicaSet(_ context.Context, rs *appsv1.ReplicaSet) {
	f.processed = append(f.processed, rs.Name)
}

type fakePoolMgr struct {
	reconciled []string
	cleaned    []string
}

func (f *fakePoolMgr) reconcileEnvPool(_ context.Context, env *fv1.Environment) error {
	f.reconciled = append(f.reconciled, env.Name)
	return nil
}
func (f *fakePoolMgr) cleanupEnvPool(_ context.Context, env *fv1.Environment) {
	f.cleaned = append(f.cleaned, env.Name)
}

func crClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
}

func poolmgrFn(name string, et fv1.ExecutorType) *fv1.Function {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: "u1", ResourceVersion: "9", Generation: 2}}
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = et
	return fn
}

func TestPoolmgrFunctionReconciler(t *testing.T) {
	key := types.NamespacedName{Name: "fn", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}

	t.Run("first sight of poolmgr function creates istio service and caches", func(t *testing.T) {
		m := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(poolmgrFn("fn", fv1.ExecutorTypePoolmgr)), mgr: m, enableIstio: true}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, m.istioCreated)
		assert.Empty(t, m.deleted)
		_, cached := r.lastSeen.Load(key)
		assert.True(t, cached)
	})

	t.Run("istio disabled: no istio service on create", func(t *testing.T) {
		m := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(poolmgrFn("fn", "")), mgr: m, enableIstio: false}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, m.istioCreated)
		_, cached := r.lastSeen.Load(key)
		assert.True(t, cached, "empty executor type defaults to poolmgr and is managed")
	})

	t.Run("non-poolmgr function on first sight is ignored", func(t *testing.T) {
		m := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(poolmgrFn("fn", fv1.ExecutorTypeNewdeploy)), mgr: m, enableIstio: true}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, m.istioCreated)
		_, cached := r.lastSeen.Load(key)
		assert.False(t, cached)
	})

	t.Run("spec change of a managed function refreshes its pods", func(t *testing.T) {
		m := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(poolmgrFn("fn", fv1.ExecutorTypePoolmgr)), mgr: m, enableIstio: true}
		r.lastSeen.Store(key, poolmgrFn("fn", fv1.ExecutorTypePoolmgr))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, m.refreshed, "package/config change must re-specialize pods")
		assert.Empty(t, m.istioCreated, "istio service is stable across spec updates")
	})

	t.Run("deleted function is marked deleted and its istio service removed", func(t *testing.T) {
		m := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(), mgr: m, enableIstio: true} // empty client -> NotFound
		cached := poolmgrFn("fn", fv1.ExecutorTypePoolmgr)
		r.lastSeen.Store(key, cached)
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		require.Len(t, m.deleted, 1)
		assert.Equal(t, crd.CacheKeyURGFromMeta(&cached.ObjectMeta), m.deleted[0])
		assert.Equal(t, []string{"fn"}, m.istioDeleted)
		_, ok := r.lastSeen.Load(key)
		assert.False(t, ok)
	})

	t.Run("delete of an unseen function is a no-op", func(t *testing.T) {
		m := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(), mgr: m, enableIstio: true}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, m.deleted)
	})

	t.Run("delete+recreate with a new UID cleans the old and creates the new", func(t *testing.T) {
		m := &fakeFuncMgr{}
		old := poolmgrFn("fn", fv1.ExecutorTypePoolmgr) // UID u1
		recreated := poolmgrFn("fn", fv1.ExecutorTypePoolmgr)
		recreated.UID = "u2"
		r := &functionReconciler{logger: logr.Discard(), client: crClient(recreated), mgr: m, enableIstio: true}
		r.lastSeen.Store(key, old)
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		require.Len(t, m.deleted, 1)
		assert.Equal(t, crd.CacheKeyURGFromMeta(&old.ObjectMeta), m.deleted[0])
		assert.Equal(t, []string{"fn"}, m.istioCreated, "new incarnation gets a fresh istio service")
		newCached, _ := r.lastSeen.Load(key)
		assert.Equal(t, types.UID("u2"), newCached.(*fv1.Function).UID)
	})

	t.Run("transition away from poolmgr cleans up and uncaches", func(t *testing.T) {
		m := &fakeFuncMgr{}
		now := poolmgrFn("fn", fv1.ExecutorTypeNewdeploy)
		r := &functionReconciler{logger: logr.Discard(), client: crClient(now), mgr: m, enableIstio: true}
		r.lastSeen.Store(key, poolmgrFn("fn", fv1.ExecutorTypePoolmgr))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		require.Len(t, m.deleted, 1, "old poolmgr incarnation must be cleaned up")
		assert.Equal(t, []string{"fn"}, m.istioDeleted)
		_, ok := r.lastSeen.Load(key)
		assert.False(t, ok)
	})
}

func TestPoolmgrEnvironmentReconciler(t *testing.T) {
	key := types.NamespacedName{Name: "env", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "e1"}}

	t.Run("existing environment reconciles its pool and is cached", func(t *testing.T) {
		m := &fakePoolMgr{}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(env), mgr: m}
		res, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"env"}, m.reconciled)
		assert.Empty(t, m.cleaned)
		assert.Equal(t, envResyncPeriod, res.RequeueAfter, "should periodically re-reconcile")
		_, cached := r.lastSeen.Load(key)
		assert.True(t, cached)
	})

	t.Run("deleted environment cleans up its pool via the cached object", func(t *testing.T) {
		m := &fakePoolMgr{}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(), mgr: m} // empty -> NotFound
		r.lastSeen.Store(key, env.DeepCopy())
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"env"}, m.cleaned)
		_, cached := r.lastSeen.Load(key)
		assert.False(t, cached)
	})

	t.Run("delete of an unseen environment is a no-op", func(t *testing.T) {
		m := &fakePoolMgr{}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(), mgr: m}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, m.cleaned)
	})
}

func crClientK8s(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(objs...).Build()
}

func TestPoolmgrReplicaSetReconciler(t *testing.T) {
	key := types.NamespacedName{Name: "rs", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "rs", Namespace: "default"}}

	t.Run("existing replicaset is handed to the cleaner", func(t *testing.T) {
		c := &fakeRSCleaner{}
		r := &replicaSetReconciler{logger: logr.Discard(), client: crClientK8s(rs), cleaner: c}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"rs"}, c.processed, "the zero-replica check lives in processReplicaSet")
	})

	t.Run("deleted replicaset is a no-op", func(t *testing.T) {
		c := &fakeRSCleaner{}
		r := &replicaSetReconciler{logger: logr.Discard(), client: crClientK8s(), cleaner: c}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, c.processed, "scale-to-zero precedes deletion, so a missing RS needs no cleanup")
	})
}

type fakeEnqueuer struct{ enqueued map[string]string } // env UID -> key

func (f *fakeEnqueuer) enqueueReadyPod(envUID, key string) {
	if f.enqueued == nil {
		f.enqueued = map[string]string{}
	}
	f.enqueued[envUID] = key
}

func warmPod(name, envUID string, phase corev1.PodPhase, managed string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: map[string]string{
			fv1.EXECUTOR_TYPE:   string(fv1.ExecutorTypePoolmgr),
			fv1.ENVIRONMENT_UID: envUID,
			"managed":           managed,
		}},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func TestPoolmgrReadyPodReconciler(t *testing.T) {
	key := types.NamespacedName{Name: "pod", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}

	t.Run("running warm pod is enqueued to its pool by env UID", func(t *testing.T) {
		e := &fakeEnqueuer{}
		r := &readyPodReconciler{logger: logr.Discard(), client: crClientK8s(warmPod("pod", "e1", corev1.PodRunning, "true")), enqueuer: e}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, "default/pod", e.enqueued["e1"])
	})

	t.Run("pending warm pod is not enqueued", func(t *testing.T) {
		e := &fakeEnqueuer{}
		r := &readyPodReconciler{logger: logr.Discard(), client: crClientK8s(warmPod("pod", "e1", corev1.PodPending, "true")), enqueuer: e}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, e.enqueued, "only Running pods are fed to the queue")
	})

	t.Run("specialized pod (managed=false) is not enqueued", func(t *testing.T) {
		e := &fakeEnqueuer{}
		r := &readyPodReconciler{logger: logr.Discard(), client: crClientK8s(warmPod("pod", "e1", corev1.PodRunning, "false")), enqueuer: e}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, e.enqueued, "specialized pods are not warm candidates")
	})

	t.Run("deleted pod is a no-op", func(t *testing.T) {
		e := &fakeEnqueuer{}
		r := &readyPodReconciler{logger: logr.Discard(), client: crClientK8s(), enqueuer: e}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, e.enqueued)
	})
}

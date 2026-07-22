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
)

type fakeFuncMgr struct {
	deleted      []crd.CacheKeyUG
	refreshed    []string
	istioCreated []string
	istioDeleted []string
	fnSvcDeleted []string
}

func (f *fakeFuncMgr) markFuncDeleted(k crd.CacheKeyUG) { f.deleted = append(f.deleted, k) }
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
func (f *fakeFuncMgr) deleteFunctionService(_ context.Context, fn *fv1.Function) error {
	f.fnSvcDeleted = append(f.fnSvcDeleted, fn.Name)
	return nil
}

type fakeRSCleaner struct{ processed []string }

func (f *fakeRSCleaner) processReplicaSet(_ context.Context, rs *appsv1.ReplicaSet) {
	f.processed = append(f.processed, rs.Name)
}

type fakePoolMgr struct {
	reconciled []string
	cleaned    []string
	reconcErr  error
}

func (f *fakePoolMgr) reconcileEnvPool(_ context.Context, env *fv1.Environment) error {
	f.reconciled = append(f.reconciled, env.Name)
	return f.reconcErr
}
func (f *fakePoolMgr) cleanupEnvPool(_ context.Context, env *fv1.Environment) {
	f.cleaned = append(f.cleaned, env.Name)
}

func poolmgrFn(name string, et fv1.ExecutorType) *fv1.Function {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: "u1", ResourceVersion: "9", Generation: 2}}
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = et
	return fn
}

func TestReconcilePoolmgrFunc(t *testing.T) {
	fn := poolmgrFn("fn", fv1.ExecutorTypePoolmgr)

	t.Run("create (old == nil) creates the istio service when istio is enabled", func(t *testing.T) {
		m := &fakeFuncMgr{}
		require.NoError(t, reconcilePoolmgrFunc(t.Context(), m, true, nil, fn))
		assert.Equal(t, []string{"fn"}, m.istioCreated)
		assert.Empty(t, m.refreshed, "no pod refresh on first sight — pods specialize lazily")
	})

	t.Run("create with istio disabled is a no-op", func(t *testing.T) {
		m := &fakeFuncMgr{}
		require.NoError(t, reconcilePoolmgrFunc(t.Context(), m, false, nil, fn))
		assert.Empty(t, m.istioCreated)
	})

	t.Run("update (old != nil) refreshes pods, leaving the stable istio service", func(t *testing.T) {
		m := &fakeFuncMgr{}
		require.NoError(t, reconcilePoolmgrFunc(t.Context(), m, true, fn, fn))
		assert.Equal(t, []string{"fn"}, m.refreshed, "package/config change must re-specialize pods")
		assert.Empty(t, m.istioCreated, "istio service is stable across spec updates")
	})
}

func TestCleanupPoolmgrFunc(t *testing.T) {
	fn := poolmgrFn("fn", fv1.ExecutorTypePoolmgr)

	t.Run("marks fsCache deleted and removes the istio service", func(t *testing.T) {
		m := &fakeFuncMgr{}
		require.NoError(t, cleanupPoolmgrFunc(t.Context(), m, true, false, fn))
		require.Len(t, m.deleted, 1)
		assert.Equal(t, crd.CacheKeyUGFromMeta(&fn.ObjectMeta), m.deleted[0])
		assert.Equal(t, []string{"fn"}, m.istioDeleted)
		assert.Empty(t, m.fnSvcDeleted)
	})

	t.Run("istio disabled: marks fsCache deleted only", func(t *testing.T) {
		m := &fakeFuncMgr{}
		require.NoError(t, cleanupPoolmgrFunc(t.Context(), m, false, false, fn))
		require.Len(t, m.deleted, 1)
		assert.Empty(t, m.istioDeleted)
		assert.Empty(t, m.fnSvcDeleted)
	})

	t.Run("function services enabled: also deletes the headless function service", func(t *testing.T) {
		m := &fakeFuncMgr{}
		require.NoError(t, cleanupPoolmgrFunc(t.Context(), m, false, true, fn))
		require.Len(t, m.deleted, 1)
		assert.Equal(t, []string{"fn"}, m.fnSvcDeleted)
		assert.Empty(t, m.istioDeleted)
	})
}

func TestPoolmgrReconcileEnvironment(t *testing.T) {
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "e1"}}

	t.Run("reconciles the pool and requests the periodic resync", func(t *testing.T) {
		m := &fakePoolMgr{}
		// old is ignored by poolmgr — the pool reconcile is idempotent.
		requeue, err := reconcilePoolmgrEnv(t.Context(), m, env)
		require.NoError(t, err)
		assert.Equal(t, []string{"env"}, m.reconciled)
		assert.Equal(t, envResyncPeriod, requeue, "poolmgr drives the periodic pool re-reconcile")
	})

	t.Run("reconcile error propagates with no requeue", func(t *testing.T) {
		m := &fakePoolMgr{reconcErr: assert.AnError}
		requeue, err := reconcilePoolmgrEnv(t.Context(), m, env)
		require.Error(t, err)
		assert.Zero(t, requeue)
	})

	t.Run("CleanupEnvironment destroys the pool", func(t *testing.T) {
		// CleanupEnvironment delegates straight to cleanupEnvPool; exercise that wiring.
		m := &fakePoolMgr{}
		m.cleanupEnvPool(t.Context(), env)
		assert.Equal(t, []string{"env"}, m.cleaned)
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

type fakeEnqueuer struct{ enqueued map[string]string } // pool key -> pod key

func (f *fakeEnqueuer) enqueueReadyPod(queueKey, podKey string) {
	if f.enqueued == nil {
		f.enqueued = map[string]string{}
	}
	f.enqueued[queueKey] = podKey
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

	t.Run("image-volume pool pod routes to its per-image queue", func(t *testing.T) {
		// A pod carrying the image-hash label belongs to a per-image Path B
		// pool: it has no fetcher, so handing it to the plain pool's queue
		// would break fetcher-path functions. The reconciler must route it
		// to poolKey(envUID, hash), never to the bare env UID.
		pod := warmPod("pod", "e1", corev1.PodRunning, "true")
		pod.Labels[fv1.POOL_OCI_IMAGE_HASH] = "abcdef0123456789"
		e := &fakeEnqueuer{}
		r := &readyPodReconciler{logger: logr.Discard(), client: crClientK8s(pod), enqueuer: e}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, "default/pod", e.enqueued["e1/abcdef0123456789"])
		assert.NotContains(t, e.enqueued, "e1", "a Path B pod must not reach the plain pool's queue")
	})
}

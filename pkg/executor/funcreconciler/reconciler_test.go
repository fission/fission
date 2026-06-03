// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package funcreconciler

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

// reconcileCall records one ReconcileFunction invocation: the function name and
// whether it was a first-sight create (old == nil).
type reconcileCall struct {
	name       string
	firstSight bool
}

// fakeBackend is a FuncReconciler that records the calls it receives.
type fakeBackend struct {
	reconciled []reconcileCall
	deleted    []string
	reconcErr  error
	deleteErr  error
}

func (f *fakeBackend) ReconcileFunction(_ context.Context, old, fn *fv1.Function) error {
	f.reconciled = append(f.reconciled, reconcileCall{name: fn.Name, firstSight: old == nil})
	return f.reconcErr
}

func (f *fakeBackend) DeleteFunction(_ context.Context, fn *fv1.Function) error {
	f.deleted = append(f.deleted, fn.Name)
	return f.deleteErr
}

func crClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
}

func fn(name string, et fv1.ExecutorType, uid types.UID) *fv1.Function {
	f := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: uid}}
	f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = et
	return f
}

func TestResolveExecutorType(t *testing.T) {
	assert.Equal(t, fv1.ExecutorTypePoolmgr, resolveExecutorType(fn("a", "", "u1")), "unset type defaults to poolmgr")
	assert.Equal(t, fv1.ExecutorTypePoolmgr, resolveExecutorType(fn("a", fv1.ExecutorTypePoolmgr, "u1")))
	assert.Equal(t, fv1.ExecutorTypeNewdeploy, resolveExecutorType(fn("a", fv1.ExecutorTypeNewdeploy, "u1")))
	assert.Equal(t, fv1.ExecutorTypeContainer, resolveExecutorType(fn("a", fv1.ExecutorTypeContainer, "u1")))
}

func TestFunctionReconcilerDispatch(t *testing.T) {
	key := types.NamespacedName{Name: "fn", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}

	newReconciler := func(c client.Client, pm, nd *fakeBackend) *functionReconciler {
		return &functionReconciler{
			logger: logr.Discard(),
			client: c,
			backends: map[fv1.ExecutorType]executortype.FuncReconciler{
				fv1.ExecutorTypePoolmgr:   pm,
				fv1.ExecutorTypeNewdeploy: nd,
			},
		}
	}

	t.Run("first sight routes to the owning backend with nil old and caches", func(t *testing.T) {
		pm, nd := &fakeBackend{}, &fakeBackend{}
		r := newReconciler(crClient(fn("fn", fv1.ExecutorTypeNewdeploy, "u1")), pm, nd)
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []reconcileCall{{name: "fn", firstSight: true}}, nd.reconciled)
		assert.Empty(t, pm.reconciled)
		_, cached := r.lastReconciled.Load(key)
		assert.True(t, cached)
	})

	t.Run("unset executor type routes to poolmgr", func(t *testing.T) {
		pm, nd := &fakeBackend{}, &fakeBackend{}
		r := newReconciler(crClient(fn("fn", "", "u1")), pm, nd)
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []reconcileCall{{name: "fn", firstSight: true}}, pm.reconciled)
		assert.Empty(t, nd.reconciled)
	})

	t.Run("same-type update hands the backend the cached old object", func(t *testing.T) {
		pm, nd := &fakeBackend{}, &fakeBackend{}
		r := newReconciler(crClient(fn("fn", fv1.ExecutorTypeNewdeploy, "u1")), pm, nd)
		r.lastReconciled.Store(key, fn("fn", fv1.ExecutorTypeNewdeploy, "u1"))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []reconcileCall{{name: "fn", firstSight: false}}, nd.reconciled, "update must not be treated as a create")
	})

	t.Run("delete tears down via the cached object's backend and uncaches", func(t *testing.T) {
		pm, nd := &fakeBackend{}, &fakeBackend{}
		r := newReconciler(crClient(), pm, nd) // empty client -> NotFound
		r.lastReconciled.Store(key, fn("fn", fv1.ExecutorTypeNewdeploy, "u1"))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, nd.deleted)
		assert.Empty(t, pm.deleted)
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached)
	})

	t.Run("delete of an unseen function is a no-op", func(t *testing.T) {
		pm, nd := &fakeBackend{}, &fakeBackend{}
		r := newReconciler(crClient(), pm, nd)
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, pm.deleted)
		assert.Empty(t, nd.deleted)
	})

	t.Run("executor-type transition tears down the old type and creates under the new", func(t *testing.T) {
		pm, nd := &fakeBackend{}, &fakeBackend{}
		r := newReconciler(crClient(fn("fn", fv1.ExecutorTypeNewdeploy, "u1")), pm, nd)
		r.lastReconciled.Store(key, fn("fn", fv1.ExecutorTypePoolmgr, "u1"))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, pm.deleted, "old poolmgr incarnation torn down")
		assert.Equal(t, []reconcileCall{{name: "fn", firstSight: true}}, nd.reconciled, "created afresh under newdeploy")
		cached, _ := r.lastReconciled.Load(key)
		assert.Equal(t, fv1.ExecutorTypeNewdeploy, resolveExecutorType(cached.(*fv1.Function)))
	})

	t.Run("delete+recreate with a new UID tears down the old and creates the new", func(t *testing.T) {
		pm, nd := &fakeBackend{}, &fakeBackend{}
		r := newReconciler(crClient(fn("fn", fv1.ExecutorTypeNewdeploy, "u2")), pm, nd)
		r.lastReconciled.Store(key, fn("fn", fv1.ExecutorTypeNewdeploy, "u1"))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, nd.deleted, "old UID torn down")
		assert.Equal(t, []reconcileCall{{name: "fn", firstSight: true}}, nd.reconciled, "new UID created afresh")
	})

	t.Run("a ReconcileFunction error does not advance the cache", func(t *testing.T) {
		pm, nd := &fakeBackend{}, &fakeBackend{reconcErr: assert.AnError}
		r := newReconciler(crClient(fn("fn", fv1.ExecutorTypeNewdeploy, "u1")), pm, nd)
		_, err := r.Reconcile(t.Context(), req)
		require.Error(t, err)
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached)
	})

	t.Run("transition cleanup error stops before creating under the new type", func(t *testing.T) {
		pm, nd := &fakeBackend{deleteErr: assert.AnError}, &fakeBackend{}
		r := newReconciler(crClient(fn("fn", fv1.ExecutorTypeNewdeploy, "u1")), pm, nd)
		r.lastReconciled.Store(key, fn("fn", fv1.ExecutorTypePoolmgr, "u1"))
		_, err := r.Reconcile(t.Context(), req)
		require.Error(t, err)
		assert.Empty(t, nd.reconciled, "must not create the new type if tearing down the old failed")
	})
}

func TestFunctionReconcilerUnmanagedType(t *testing.T) {
	key := types.NamespacedName{Name: "fn", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}
	// Only poolmgr is registered; "newdeploy" is unmanaged in this reconciler.
	backends := map[fv1.ExecutorType]executortype.FuncReconciler{fv1.ExecutorTypePoolmgr: &fakeBackend{}}

	t.Run("unmanaged type on first sight is a no-op and not cached", func(t *testing.T) {
		r := &functionReconciler{logger: logr.Discard(), client: crClient(fn("fn", fv1.ExecutorTypeNewdeploy, "u1")), backends: backends}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached)
	})

	t.Run("transition to an unmanaged type still tears down the old and drops the cache", func(t *testing.T) {
		pm := &fakeBackend{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(fn("fn", fv1.ExecutorTypeNewdeploy, "u1")),
			backends: map[fv1.ExecutorType]executortype.FuncReconciler{fv1.ExecutorTypePoolmgr: pm}}
		r.lastReconciled.Store(key, fn("fn", fv1.ExecutorTypePoolmgr, "u1"))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, pm.deleted, "old poolmgr resources torn down")
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached, "no stale cache entry for the now-unmanaged function")
	})
}

// fakeFuncExecutorType is an ExecutorType that also implements FuncReconciler.
type fakeFuncExecutorType struct {
	executortype.ExecutorType
	fakeBackend
}

// nonFuncExecutorType implements ExecutorType but NOT FuncReconciler.
type nonFuncExecutorType struct{ executortype.ExecutorType }

func TestCollectBackends(t *testing.T) {
	types := map[fv1.ExecutorType]executortype.ExecutorType{
		fv1.ExecutorTypePoolmgr:   &fakeFuncExecutorType{},
		fv1.ExecutorTypeNewdeploy: &fakeFuncExecutorType{},
		fv1.ExecutorTypeContainer: nonFuncExecutorType{},
	}
	backends := collectBackends(types)
	require.Len(t, backends, 2, "only types implementing FuncReconciler are collected")
	assert.Contains(t, backends, fv1.ExecutorTypePoolmgr)
	assert.Contains(t, backends, fv1.ExecutorTypeNewdeploy)
	assert.NotContains(t, backends, fv1.ExecutorTypeContainer)
}

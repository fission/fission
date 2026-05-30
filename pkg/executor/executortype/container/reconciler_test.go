// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

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
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

type fakeFuncMgr struct {
	created, updated, deleted []string
}

func (f *fakeFuncMgr) createFunction(_ context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	f.created = append(f.created, fn.Name)
	return nil, nil
}
func (f *fakeFuncMgr) updateFunction(_ context.Context, old, _ *fv1.Function) error {
	f.updated = append(f.updated, old.Name)
	return nil
}
func (f *fakeFuncMgr) deleteFunction(_ context.Context, fn *fv1.Function) error {
	f.deleted = append(f.deleted, fn.Name)
	return nil
}

func fnOfType(name string, et fv1.ExecutorType) *fv1.Function {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = et
	return fn
}

func crClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
}

func TestContainerFunctionReconcilerRouting(t *testing.T) {
	key := types.NamespacedName{Name: "fn", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}

	t.Run("first sight of container function creates and caches", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(fnOfType("fn", fv1.ExecutorTypeContainer)), caaf: mgr}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, mgr.created)
		assert.Empty(t, mgr.updated)
		_, cached := r.lastReconciled.Load(key)
		assert.True(t, cached, "managed function must be cached")
	})

	t.Run("cached function updates with the cached old object", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(fnOfType("fn", fv1.ExecutorTypeContainer)), caaf: mgr}
		r.lastReconciled.Store(key, fnOfType("fn", fv1.ExecutorTypeContainer))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, mgr.updated)
		assert.Empty(t, mgr.created)
	})

	t.Run("deleted function cleans up via the cached old object", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(), caaf: mgr} // empty client -> NotFound
		r.lastReconciled.Store(key, fnOfType("fn", fv1.ExecutorTypeContainer))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, mgr.deleted)
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached, "deleted function must be evicted from cache")
	})

	t.Run("non-container function on first sight is ignored", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(fnOfType("fn", fv1.ExecutorTypeNewdeploy)), caaf: mgr}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, mgr.created)
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached)
	})

	t.Run("transition away from container updates and uncaches", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(fnOfType("fn", fv1.ExecutorTypeNewdeploy)), caaf: mgr}
		r.lastReconciled.Store(key, fnOfType("fn", fv1.ExecutorTypeContainer))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, mgr.updated, "updateFunction handles the type-transition cleanup")
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached, "transitioned-away function must be evicted")
	})
}

func TestIsContainerType(t *testing.T) {
	assert.True(t, isContainerType(fnOfType("a", "")), "unset type falls back to container")
	assert.True(t, isContainerType(fnOfType("a", fv1.ExecutorTypeContainer)))
	assert.False(t, isContainerType(fnOfType("a", fv1.ExecutorTypeNewdeploy)))
	assert.False(t, isContainerType(fnOfType("a", fv1.ExecutorTypePoolmgr)))
}

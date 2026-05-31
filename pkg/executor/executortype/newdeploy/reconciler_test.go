// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

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
	created, updated, deleted, reconciled []string
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
func (f *fakeFuncMgr) reconcileDeploymentSpec(_ context.Context, fn *fv1.Function) error {
	f.reconciled = append(f.reconciled, fn.Name)
	return nil
}

type fakeEnvUpdater struct {
	updated []string
}

func (f *fakeEnvUpdater) updateEnvFunctions(_ context.Context, env *fv1.Environment) error {
	f.updated = append(f.updated, env.Name)
	return nil
}

func fnOfType(name string, et fv1.ExecutorType) *fv1.Function {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = et
	return fn
}

func envWithImage(name, image string) *fv1.Environment {
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	env.Spec.Runtime.Image = image
	return env
}

func crClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
}

func TestNewdeployFunctionReconcilerRouting(t *testing.T) {
	key := types.NamespacedName{Name: "fn", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}

	t.Run("first sight of newdeploy function creates and caches", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(fnOfType("fn", fv1.ExecutorTypeNewdeploy)), deploy: mgr}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, mgr.created)
		assert.Equal(t, []string{"fn"}, mgr.reconciled, "first sight must reconcile a possibly-stale adopted deployment to current spec")
		assert.Empty(t, mgr.updated)
		_, cached := r.lastReconciled.Load(key)
		assert.True(t, cached, "managed function must be cached")
	})

	t.Run("cached function updates with the cached old object", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(fnOfType("fn", fv1.ExecutorTypeNewdeploy)), deploy: mgr}
		r.lastReconciled.Store(key, fnOfType("fn", fv1.ExecutorTypeNewdeploy))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, mgr.updated)
		assert.Empty(t, mgr.created)
	})

	t.Run("deleted function cleans up via the cached old object", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(), deploy: mgr} // empty client -> NotFound
		r.lastReconciled.Store(key, fnOfType("fn", fv1.ExecutorTypeNewdeploy))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, mgr.deleted)
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached, "deleted function must be evicted from cache")
	})

	t.Run("non-newdeploy function on first sight is ignored", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(fnOfType("fn", fv1.ExecutorTypePoolmgr)), deploy: mgr}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, mgr.created)
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached)
	})

	t.Run("transition away from newdeploy updates and uncaches", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(fnOfType("fn", fv1.ExecutorTypePoolmgr)), deploy: mgr}
		r.lastReconciled.Store(key, fnOfType("fn", fv1.ExecutorTypeNewdeploy))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, mgr.updated, "updateFunction handles the type-transition cleanup")
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached, "transitioned-away function must be evicted")
	})
}

func TestNewdeployEnvironmentReconcilerRouting(t *testing.T) {
	key := types.NamespacedName{Name: "env", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}

	t.Run("first sight caches without touching functions", func(t *testing.T) {
		up := &fakeEnvUpdater{}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(envWithImage("env", "img:1")), deploy: up}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, up.updated, "first sight must not roll functions (matches old AddFunc no-op)")
		_, cached := r.lastReconciled.Load(key)
		assert.True(t, cached)
	})

	t.Run("image change rolls the environment's functions", func(t *testing.T) {
		up := &fakeEnvUpdater{}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(envWithImage("env", "img:2")), deploy: up}
		r.lastReconciled.Store(key, envWithImage("env", "img:1"))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"env"}, up.updated)
	})

	t.Run("non-image spec change is a no-op", func(t *testing.T) {
		up := &fakeEnvUpdater{}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(envWithImage("env", "img:1")), deploy: up}
		r.lastReconciled.Store(key, envWithImage("env", "img:1"))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, up.updated, "same image must not roll functions")
	})

	t.Run("deleted environment drops the cache and does nothing", func(t *testing.T) {
		up := &fakeEnvUpdater{}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(), deploy: up} // empty -> NotFound
		r.lastReconciled.Store(key, envWithImage("env", "img:1"))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, up.updated)
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached)
	})
}

func TestIsNewdeployType(t *testing.T) {
	assert.True(t, isNewdeployType(fnOfType("a", fv1.ExecutorTypeNewdeploy)))
	assert.False(t, isNewdeployType(fnOfType("a", "")), "unset type is not managed (createFunction no-ops on it)")
	assert.False(t, isNewdeployType(fnOfType("a", fv1.ExecutorTypeContainer)))
	assert.False(t, isNewdeployType(fnOfType("a", fv1.ExecutorTypePoolmgr)))
}

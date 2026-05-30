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

type fakeEnvUpdater struct{ updated []string }

func (f *fakeEnvUpdater) updateEnvFunctionDeployments(_ context.Context, env *fv1.Environment) {
	f.updated = append(f.updated, env.Name)
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
		_, cached := r.lastReconciled.Load(key)
		assert.True(t, cached)
	})

	t.Run("non-newdeploy function on first sight is ignored", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(fnOfType("fn", fv1.ExecutorTypeContainer)), deploy: mgr}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, mgr.created)
		_, cached := r.lastReconciled.Load(key)
		assert.False(t, cached)
	})

	t.Run("deleted function cleans up via cached old", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		r := &functionReconciler{logger: logr.Discard(), client: crClient(), deploy: mgr}
		r.lastReconciled.Store(key, fnOfType("fn", fv1.ExecutorTypeNewdeploy))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"fn"}, mgr.deleted)
	})
}

func TestIsNewdeployType(t *testing.T) {
	assert.True(t, isNewdeployType(fnOfType("a", fv1.ExecutorTypeNewdeploy)))
	assert.False(t, isNewdeployType(fnOfType("a", "")), "unset type is NOT newdeploy")
	assert.False(t, isNewdeployType(fnOfType("a", fv1.ExecutorTypeContainer)))
	assert.False(t, isNewdeployType(fnOfType("a", fv1.ExecutorTypePoolmgr)))
}

func TestEnvironmentReconcilerImageGate(t *testing.T) {
	key := types.NamespacedName{Name: "env", Namespace: "default"}
	req := ctrl.Request{NamespacedName: key}

	t.Run("first sight does not update functions", func(t *testing.T) {
		up := &fakeEnvUpdater{}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(envWithImage("env", "img:1")), deploy: up}
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, up.updated, "first reconcile must not recreate deployments")
	})

	t.Run("image change updates functions", func(t *testing.T) {
		up := &fakeEnvUpdater{}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(envWithImage("env", "img:2")), deploy: up}
		r.lastReconciled.Store(key, envWithImage("env", "img:1"))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, []string{"env"}, up.updated)
	})

	t.Run("same image does not update functions", func(t *testing.T) {
		up := &fakeEnvUpdater{}
		r := &environmentReconciler{logger: logr.Discard(), client: crClient(envWithImage("env", "img:1")), deploy: up}
		r.lastReconciled.Store(key, envWithImage("env", "img:1"))
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, up.updated)
	})
}

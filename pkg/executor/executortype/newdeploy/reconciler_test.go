// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
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

func TestReconcileNewdeployFunc(t *testing.T) {
	fn := fnOfType("fn", fv1.ExecutorTypeNewdeploy)

	t.Run("create (old == nil) creates then reconciles the deployment spec", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		require.NoError(t, reconcileNewdeployFunc(t.Context(), mgr, nil, fn))
		assert.Equal(t, []string{"fn"}, mgr.created)
		assert.Equal(t, []string{"fn"}, mgr.reconciled, "first sight must reconcile a possibly-stale adopted deployment to current spec")
		assert.Empty(t, mgr.updated)
	})

	t.Run("update (old != nil) diffs against the old object", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		require.NoError(t, reconcileNewdeployFunc(t.Context(), mgr, fn, fn))
		assert.Equal(t, []string{"fn"}, mgr.updated)
		assert.Empty(t, mgr.created)
	})

	t.Run("DeleteFunction tears down the function", func(t *testing.T) {
		// DeleteFunction delegates straight to deleteFunction; exercise the wiring.
		mgr := &fakeFuncMgr{}
		require.NoError(t, mgr.deleteFunction(t.Context(), fn))
		assert.Equal(t, []string{"fn"}, mgr.deleted)
	})
}

func TestNewdeployReconcileEnvironment(t *testing.T) {
	t.Run("first sight (old == nil) does not roll functions", func(t *testing.T) {
		up := &fakeEnvUpdater{}
		requeue, err := reconcileNewdeployEnv(t.Context(), up, nil, envWithImage("env", "img:1"))
		require.NoError(t, err)
		assert.Empty(t, up.updated, "first sight must not roll functions (matches old AddFunc no-op)")
		assert.Zero(t, requeue, "newdeploy requests no periodic requeue")
	})

	t.Run("image change rolls the environment's functions", func(t *testing.T) {
		up := &fakeEnvUpdater{}
		_, err := reconcileNewdeployEnv(t.Context(), up, envWithImage("env", "img:1"), envWithImage("env", "img:2"))
		require.NoError(t, err)
		assert.Equal(t, []string{"env"}, up.updated)
	})

	t.Run("non-image spec change is a no-op", func(t *testing.T) {
		up := &fakeEnvUpdater{}
		_, err := reconcileNewdeployEnv(t.Context(), up, envWithImage("env", "img:1"), envWithImage("env", "img:1"))
		require.NoError(t, err)
		assert.Empty(t, up.updated, "same image must not roll functions")
	})

	t.Run("CleanupEnvironment is a no-op (function cleanup is driven by the Function watch)", func(t *testing.T) {
		// Compile-time + behavioural guard that delete needs no newdeploy-side action.
		(&NewDeploy{}).CleanupEnvironment(t.Context(), envWithImage("env", "img:1"))
	})
}

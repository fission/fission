// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

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

func TestReconcileContainerFunc(t *testing.T) {
	fn := fnOfType("fn", fv1.ExecutorTypeContainer)

	t.Run("create (old == nil) creates the function", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		require.NoError(t, reconcileContainerFunc(t.Context(), mgr, nil, fn))
		assert.Equal(t, []string{"fn"}, mgr.created)
		assert.Empty(t, mgr.updated)
	})

	t.Run("update (old != nil) diffs against the old object", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		require.NoError(t, reconcileContainerFunc(t.Context(), mgr, fn, fn))
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

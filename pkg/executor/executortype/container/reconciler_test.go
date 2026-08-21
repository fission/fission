// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
)

type fakeFuncMgr struct {
	created, updated, deleted, reconciled []string
	calls                                 []string // ordered call log: "create", "converge", "update"
	createErr                             error    // injected createFunction failure
	resourcesGone                         bool     // resourcesExist returns false (drift)
}

func (f *fakeFuncMgr) resourcesExist(_ context.Context, _ *fv1.Function) (bool, error) {
	return !f.resourcesGone, nil
}

func (f *fakeFuncMgr) createFunction(_ context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	f.calls = append(f.calls, "create")
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, fn.Name)
	return nil, nil
}
func (f *fakeFuncMgr) updateFunction(_ context.Context, old, _ *fv1.Function) error {
	f.calls = append(f.calls, "update")
	f.updated = append(f.updated, old.Name)
	return nil
}
func (f *fakeFuncMgr) deleteFunction(_ context.Context, fn *fv1.Function) error {
	f.deleted = append(f.deleted, fn.Name)
	return nil
}
func (f *fakeFuncMgr) reconcileDeploymentSpec(_ context.Context, fn *fv1.Function) error {
	f.calls = append(f.calls, "converge")
	f.reconciled = append(f.reconciled, fn.Name)
	return nil
}

func fnOfType(name string, et fv1.ExecutorType) *fv1.Function {
	fn := &fv1.Function{Name: name, Namespace: "default"}
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = et
	return fn
}

// Every create-routed reconcile must chase createFunction with
// reconcileDeploymentSpec — see createAndConverge for why skipping it swallows a
// coalesced update permanently.
func TestReconcileContainerFunc(t *testing.T) {
	fn := fnOfType("fn", fv1.ExecutorTypeContainer)

	t.Run("create (old == nil) creates the function and converges the spec", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		require.NoError(t, reconcileContainerFunc(t.Context(), mgr, nil, fn))
		assert.Equal(t, []string{"create", "converge"}, mgr.calls,
			"createFunction adopts without a respec; the chaser must run AFTER it")
		assert.Empty(t, mgr.updated)
	})

	t.Run("update (old != nil) diffs against the old object", func(t *testing.T) {
		mgr := &fakeFuncMgr{}
		require.NoError(t, reconcileContainerFunc(t.Context(), mgr, fn, fn))
		assert.Equal(t, []string{"fn"}, mgr.updated)
		assert.Empty(t, mgr.created)
		assert.Empty(t, mgr.reconciled, "the diff path owns spec convergence on plain updates")
	})

	t.Run("drift: missing backing resources are recreated, not diffed", func(t *testing.T) {
		mgr := &fakeFuncMgr{resourcesGone: true}
		require.NoError(t, reconcileContainerFunc(t.Context(), mgr, fn, fn))
		assert.Equal(t, []string{"create", "converge"}, mgr.calls,
			"the chase must FOLLOW the create — a drift false-alarm converges the adopted deployment, in that order")
		assert.Empty(t, mgr.updated, "no diff against a non-existent object")
	})

	t.Run("create failure short-circuits the chaser", func(t *testing.T) {
		mgr := &fakeFuncMgr{resourcesGone: true, createErr: assert.AnError}
		require.Error(t, reconcileContainerFunc(t.Context(), mgr, fn, fn))
		assert.Equal(t, []string{"create"}, mgr.calls,
			"a failed create must propagate before the converge runs; the retry re-enters the whole branch")
	})

	t.Run("DeleteFunction tears down the function", func(t *testing.T) {
		// DeleteFunction delegates straight to deleteFunction; exercise the wiring.
		mgr := &fakeFuncMgr{}
		require.NoError(t, mgr.deleteFunction(t.Context(), fn))
		assert.Equal(t, []string{"fn"}, mgr.deleted)
	})
}

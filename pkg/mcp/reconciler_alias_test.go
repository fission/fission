// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

func newAliasReconciler(t *testing.T, objs ...client.Object) (*FunctionAliasToolReconciler, *FunctionToolReconciler, *Registry, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(objs...).
		WithStatusSubresource(&fv1.Function{}, &fv1.FunctionAlias{}).
		Build()
	reg := NewRegistry()
	server := NewServer(reg, NewProxy("http://router-internal", nil, logr.Discard()), NewAuthorizer(nil), logr.Discard())
	tool := &FunctionToolReconciler{logger: logr.Discard(), client: c, reg: reg, server: server}
	ar := &FunctionAliasToolReconciler{logger: logr.Discard(), client: c, tool: tool}
	return ar, tool, reg, c
}

// TestFunctionAliasToolReconciler_RepointRefreshesToolEntry is the
// registry-level assertion for "alias repoint -> list_changed delta emitted":
// a FunctionAlias event re-reconciles every Function whose Tool.Alias names
// it, and a target change (v1 -> v2) is reflected in the registered entry --
// which is exactly what drives Server.ApplyToolDelta's UpsertApplied branch
// (the SDK's list_changed notification is a direct consequence of that
// registry change, not separately observable without a live SDK session).
func TestFunctionAliasToolReconciler_RepointRefreshesToolEntry(t *testing.T) {
	t.Parallel()
	fn := exposedFn("repointed", &fv1.ToolConfig{Alias: "repoint-alias", Description: "unused-live-desc"})
	v1 := mkVersion("repointed-v1", "repointed", fv1.FunctionSpec{
		Tool: &fv1.ToolConfig{Alias: "repoint-alias", Description: "v1-desc"},
	})
	v2 := mkVersion("repointed-v2", "repointed", fv1.FunctionSpec{
		Tool: &fv1.ToolConfig{Alias: "repoint-alias", Description: "v2-desc"},
	})
	alias := mkAlias("repoint-alias", "repointed", "repointed-v1", "")

	ar, tool, reg, c := newAliasReconciler(t, fn, v1, v2, alias)
	ctx := t.Context()

	// Populate the initial entry the way FunctionToolReconciler normally
	// would (the Function's own reconcile, independent of the alias watch).
	_, err := tool.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "repointed"}})
	require.NoError(t, err)
	e, ok := reg.Lookup("default-repointed")
	require.True(t, ok)
	require.Equal(t, "v1-desc", e.Description, "precondition: entry reflects v1")

	// Repoint the alias to v2 (a Status-subresource-style write in real use;
	// a direct Update is equivalent for this reconciler, which only reads
	// Spec.Version/Status.ResolvedVersion).
	got := &fv1.FunctionAlias{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "repoint-alias"}, got))
	got.Spec.Version = "repointed-v2"
	require.NoError(t, c.Update(ctx, got))

	// The alias reconciler fires on the FunctionAlias event, not the Function.
	_, err = ar.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "repoint-alias"}})
	require.NoError(t, err)

	e, ok = reg.Lookup("default-repointed")
	require.True(t, ok)
	assert.Equal(t, "v2-desc", e.Description, "repoint must refresh the registered entry to the new target's snapshot")
}

// TestFunctionAliasToolReconciler_IgnoresFunctionsNotTargetingThisAlias
// asserts the namespace-scoped List-and-filter only touches Functions whose
// Tool.Alias actually names the alias that fired -- an unrelated Tool-bearing
// function (or one with no Tool at all) in the same namespace must be left
// alone.
func TestFunctionAliasToolReconciler_IgnoresFunctionsNotTargetingThisAlias(t *testing.T) {
	t.Parallel()
	unrelated := exposedFn("unrelated", &fv1.ToolConfig{Description: "d"}) // no Alias
	otherAliasFn := exposedFn("other", &fv1.ToolConfig{Alias: "other-alias", Description: "d"})
	alias := mkAlias("some-alias", "does-not-exist", "does-not-exist-v1", "")

	ar, tool, reg, _ := newAliasReconciler(t, unrelated, otherAliasFn, alias)
	ctx := t.Context()

	// Seed both functions' entries directly (as their own reconcile would).
	_, err := tool.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "unrelated"}})
	require.NoError(t, err)
	_, err = tool.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "other"}})
	require.NoError(t, err)
	require.Equal(t, 2, reg.Len())

	// "some-alias" fires; neither function targets it, and its own target
	// function doesn't even exist -- must be a no-op, not an error.
	_, err = ar.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "some-alias"}})
	require.NoError(t, err)
	assert.Equal(t, 2, reg.Len(), "unrelated functions must be untouched")
}

// TestFunctionAliasToolReconciler_DeleteIsNoop asserts a FunctionAlias delete
// event (Get returns NotFound) does not error: there is no registry state
// owned by the alias itself, and the affected Functions' own next reconcile
// (or this same List-based pass, since deletion doesn't change the
// namespace's Function list) settles them via resolveEntry's
// errAliasUnresolved path.
func TestFunctionAliasToolReconciler_DeleteIsNoop(t *testing.T) {
	t.Parallel()
	fn := exposedFn("orphaned", &fv1.ToolConfig{Alias: "gone-alias", Description: "live-desc"})
	ar, tool, reg, _ := newAliasReconciler(t, fn)
	ctx := t.Context()

	_, err := tool.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "orphaned"}})
	require.NoError(t, err)
	e, ok := reg.Lookup("default-orphaned")
	require.True(t, ok)
	assert.Empty(t, e.Alias, "never-resolved fallback: entry calls the live function directly")

	// "gone-alias" was never created, so a reconcile for it behaves exactly
	// like a delete (client.Get on the alias itself is never even attempted
	// here -- List-and-filter over Functions doesn't touch the alias object).
	_, err = ar.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "gone-alias"}})
	require.NoError(t, err)
}

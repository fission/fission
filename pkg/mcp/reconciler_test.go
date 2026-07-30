// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

func newReconciler(t *testing.T, objs ...client.Object) (*FunctionToolReconciler, *Registry, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(objs...).
		WithStatusSubresource(&fv1.Function{}).
		Build()
	reg := NewRegistry()
	server := NewServer(reg, NewProxy("http://router-internal", nil, logr.Discard()), NewAuthorizer(nil), logr.Discard())
	r := &FunctionToolReconciler{
		logger: logr.Discard(),
		client: c,
		reg:    reg,
		server: server,
	}
	return r, reg, c
}

func exposedFn(name string, tool *fv1.ToolConfig) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Generation: 1},
		Spec:       fv1.FunctionSpec{Tool: tool},
	}
}

func TestFunctionToolReconcilerExpose(t *testing.T) {
	fn := exposedFn("hello", &fv1.ToolConfig{
		Description: "greets",
		InputSchema: &apiextensionsv1.JSON{Raw: []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`)},
	})
	r, reg, c := newReconciler(t, fn)
	key := types.NamespacedName{Namespace: "default", Name: "hello"}
	ctx := t.Context()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	e, ok := reg.Lookup("default-hello")
	require.True(t, ok, "tool should be registered")
	assert.Equal(t, "greets", e.Description)
	assert.JSONEq(t, `{"type":"object","properties":{"name":{"type":"string"}}}`, string(e.InputSchema), "raw input schema round-trips")

	got := &fv1.Function{}
	require.NoError(t, c.Get(ctx, key, got))
	assert.True(t, conditions.IsTrue(got.Status.Conditions, fv1.FunctionConditionToolExposed), "ToolExposed should be True")
}

func TestFunctionToolReconcilerNotExposed(t *testing.T) {
	fn := exposedFn("plain", nil) // no Tool
	r, reg, _ := newReconciler(t, fn)
	key := types.NamespacedName{Namespace: "default", Name: "plain"}

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Zero(t, reg.Len(), "a function without Tool must not be advertised")
}

func TestFunctionToolReconcilerToggleOff(t *testing.T) {
	fn := exposedFn("toggle", &fv1.ToolConfig{Description: "d"})
	r, reg, c := newReconciler(t, fn)
	key := types.NamespacedName{Namespace: "default", Name: "toggle"}
	ctx := t.Context()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	_, ok := reg.Lookup("default-toggle")
	require.True(t, ok)

	// Clear Tool (presence is the on switch) and reconcile: the tool must drop.
	got := &fv1.Function{}
	require.NoError(t, c.Get(ctx, key, got))
	got.Spec.Tool = nil
	require.NoError(t, c.Update(ctx, got))

	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	_, ok = reg.Lookup("default-toggle")
	assert.False(t, ok, "tool should be removed once Tool is cleared")
}

func TestFunctionToolReconcilerRename(t *testing.T) {
	fn := exposedFn("rn", &fv1.ToolConfig{Description: "d", ToolName: "first"})
	r, reg, c := newReconciler(t, fn)
	key := types.NamespacedName{Namespace: "default", Name: "rn"}
	ctx := t.Context()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	_, ok := reg.Lookup("first")
	require.True(t, ok)

	got := &fv1.Function{}
	require.NoError(t, c.Get(ctx, key, got))
	got.Spec.Tool.ToolName = "second"
	require.NoError(t, c.Update(ctx, got))

	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	_, ok = reg.Lookup("first")
	assert.False(t, ok, "old tool name dropped on rename")
	_, ok = reg.Lookup("second")
	assert.True(t, ok, "new tool name registered")
}

// conflictStatus asserts a function carries ToolExposed=False/ToolNameConflict.
func conflictStatus(t *testing.T, c client.Client, ctx context.Context, name string) {
	t.Helper()
	gotFn := &fv1.Function{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: name}, gotFn))
	cond := meta.FindStatusCondition(gotFn.Status.Conditions, fv1.FunctionConditionToolExposed)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, fv1.FunctionReasonToolNameConflict, cond.Reason)
}

// TestFunctionToolReconcilerNameConflict verifies that a contested tool name is
// resolved to the lexicographically-smallest function regardless of reconcile
// order, and the loser is marked ToolExposed=False either way (conflict path
// when it reconciles second, eviction path when the winner reconciles second).
func TestFunctionToolReconcilerNameConflict(t *testing.T) {
	rec := func(t *testing.T, r *FunctionToolReconciler, name string) {
		t.Helper()
		_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: name}})
		require.NoError(t, err)
	}

	t.Run("loser reconciles second (conflict path)", func(t *testing.T) {
		win := exposedFn("aaa", &fv1.ToolConfig{Description: "d", ToolName: "shared"})
		lose := exposedFn("zzz", &fv1.ToolConfig{Description: "d", ToolName: "shared"})
		r, reg, c := newReconciler(t, win, lose)
		rec(t, r, "aaa")
		rec(t, r, "zzz")

		got, ok := reg.Lookup("shared")
		require.True(t, ok)
		assert.Equal(t, "aaa", got.FnName, "smaller key wins")
		assert.Equal(t, 1, reg.Len())
		conflictStatus(t, c, t.Context(), "zzz")
	})

	t.Run("winner reconciles second (eviction path)", func(t *testing.T) {
		win := exposedFn("aaa", &fv1.ToolConfig{Description: "d", ToolName: "shared"})
		lose := exposedFn("zzz", &fv1.ToolConfig{Description: "d", ToolName: "shared"})
		r, reg, c := newReconciler(t, win, lose)
		rec(t, r, "zzz") // loser registers first
		rec(t, r, "aaa") // winner takes over, evicting the loser

		got, ok := reg.Lookup("shared")
		require.True(t, ok)
		assert.Equal(t, "aaa", got.FnName, "smaller key takes over the name")
		assert.Equal(t, 1, reg.Len())
		conflictStatus(t, c, t.Context(), "zzz") // evicted loser marked not-exposed
	})
}

// mkVersion builds a minimal FunctionVersion snapshot for tests. The fake
// client does not run CEL/webhook admission, so only the fields resolveEntry
// itself reads (FunctionName, Snapshot) need to be populated.
func mkVersion(name, fnName string, snapshot fv1.FunctionSpec) *fv1.FunctionVersion {
	return &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: fv1.FunctionVersionSpec{
			FunctionName:  fnName,
			Sequence:      1,
			Snapshot:      snapshot,
			PackageDigest: "sha256:" + strings.Repeat("a", 64),
			PublishedAt:   metav1.Now(),
		},
	}
}

// mkAlias builds a FunctionAlias. Exactly one of version/resolved is
// typically non-empty in a test, mirroring the Spec.Version-XOR-
// Status.ResolvedVersion precedence resolveEntry applies.
func mkAlias(name, fnName, version, resolved string) *fv1.FunctionAlias {
	return &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: fnName, Version: version},
		Status:     fv1.FunctionAliasStatus{ResolvedVersion: resolved},
	}
}

// TestFunctionToolReconciler_AliasBuildsFromResolvedSnapshot is the RFC-0025
// "snapshot wins" case: the live function's Tool config differs from the
// alias-resolved version's recorded snapshot, and the registered entry must
// come from the snapshot, not the live spec.
func TestFunctionToolReconciler_AliasBuildsFromResolvedSnapshot(t *testing.T) {
	t.Parallel()
	liveSchema := &apiextensionsv1.JSON{Raw: []byte(`{"type":"object","properties":{"live":{"type":"string"}}}`)}
	snapSchema := &apiextensionsv1.JSON{Raw: []byte(`{"type":"object","properties":{"snap":{"type":"string"}}}`)}

	fn := exposedFn("aliased", &fv1.ToolConfig{Alias: "live-alias", Description: "live-desc", InputSchema: liveSchema})
	v := mkVersion("aliased-v1", "aliased", fv1.FunctionSpec{
		Tool: &fv1.ToolConfig{Alias: "live-alias", Description: "snap-desc", InputSchema: snapSchema},
	})
	alias := mkAlias("live-alias", "aliased", "aliased-v1", "")

	r, reg, _ := newReconciler(t, fn, v, alias)
	key := types.NamespacedName{Namespace: "default", Name: "aliased"}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	e, ok := reg.Lookup("default-aliased")
	require.True(t, ok)
	assert.Equal(t, "snap-desc", e.Description, "snapshot's Tool config wins over live's")
	assert.JSONEq(t, string(snapSchema.Raw), string(e.InputSchema))
	assert.Equal(t, "live-alias", e.Alias, "entry proxies through the alias route")
	assert.Equal(t, "aliased", e.FnName)
	assert.Equal(t, "default", e.Namespace)
}

// TestFunctionToolReconciler_AliasResolvesViaStatus covers the
// digest-pinned/eventual-resolution path: Spec.Version empty,
// Status.ResolvedVersion set by the (separate, leader-elected) alias
// resolver.
func TestFunctionToolReconciler_AliasResolvesViaStatus(t *testing.T) {
	t.Parallel()
	fn := exposedFn("aliased2", &fv1.ToolConfig{Alias: "digest-alias", Description: "live-desc"})
	v := mkVersion("aliased2-v1", "aliased2", fv1.FunctionSpec{
		Tool: &fv1.ToolConfig{Alias: "digest-alias", Description: "snap-desc"},
	})
	alias := mkAlias("digest-alias", "aliased2", "", "aliased2-v1")

	r, reg, _ := newReconciler(t, fn, v, alias)
	key := types.NamespacedName{Namespace: "default", Name: "aliased2"}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	e, ok := reg.Lookup("default-aliased2")
	require.True(t, ok)
	assert.Equal(t, "snap-desc", e.Description)
	assert.Equal(t, "digest-alias", e.Alias)
}

// TestFunctionToolReconciler_AliasUnresolvedNeverRegistered_FallsBackToLive
// covers a brand-new alias-addressed Tool whose alias has never resolved
// (missing entirely, in this case): a tool must still be advertised, built
// from the live function's own Tool config, and callable directly (no Alias
// suffix -- the router has never materialized a route for an alias that has
// never resolved).
func TestFunctionToolReconciler_AliasUnresolvedNeverRegistered_FallsBackToLive(t *testing.T) {
	t.Parallel()
	fn := exposedFn("aliased3", &fv1.ToolConfig{Alias: "missing-alias", Description: "live-desc"})
	// No FunctionAlias object created: the alias does not exist yet.
	r, reg, _ := newReconciler(t, fn)
	key := types.NamespacedName{Namespace: "default", Name: "aliased3"}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	e, ok := reg.Lookup("default-aliased3")
	require.True(t, ok, "a tool must still be advertised while the alias is unresolved")
	assert.Equal(t, "live-desc", e.Description)
	assert.Empty(t, e.Alias, "fallback entry must call the live function directly, not an unmaterialized alias route")
}

// TestFunctionToolReconciler_AliasUnresolvedAfterward_KeepsLastEntry covers
// an alias that resolved once and then stops resolving (e.g. its target
// FunctionVersion is deleted): the previously-registered entry must keep
// serving untouched rather than being pulled or reverted to the live spec.
func TestFunctionToolReconciler_AliasUnresolvedAfterward_KeepsLastEntry(t *testing.T) {
	t.Parallel()
	fn := exposedFn("aliased4", &fv1.ToolConfig{Alias: "flaky-alias", Description: "live-desc"})
	v := mkVersion("aliased4-v1", "aliased4", fv1.FunctionSpec{
		Tool: &fv1.ToolConfig{Alias: "flaky-alias", Description: "snap-desc"},
	})
	alias := mkAlias("flaky-alias", "aliased4", "aliased4-v1", "")

	r, reg, c := newReconciler(t, fn, v, alias)
	key := types.NamespacedName{Namespace: "default", Name: "aliased4"}
	ctx := t.Context()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	e, ok := reg.Lookup("default-aliased4")
	require.True(t, ok)
	assert.Equal(t, "snap-desc", e.Description, "precondition: alias resolved once")

	// The version disappears (GC'd) while the alias still names it.
	require.NoError(t, c.Delete(ctx, v))

	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	e, ok = reg.Lookup("default-aliased4")
	require.True(t, ok, "the last-known entry must keep serving")
	assert.Equal(t, "snap-desc", e.Description, "unchanged: not reverted to live nor removed")
	assert.Equal(t, "flaky-alias", e.Alias)
}

// TestFunctionToolReconciler_AliasFallbackConditionReasonTransitions is the
// auditability requirement: a fallback-serving entry (alias never resolved)
// must carry a distinct ToolExposed Reason/Message naming the unresolved
// alias, and the Reason must flip back to the normal one the reconcile after
// the alias resolves.
func TestFunctionToolReconciler_AliasFallbackConditionReasonTransitions(t *testing.T) {
	t.Parallel()
	fn := exposedFn("flips", &fv1.ToolConfig{Alias: "not-yet-alias", Description: "live-desc"})
	r, reg, c := newReconciler(t, fn)
	key := types.NamespacedName{Namespace: "default", Name: "flips"}
	ctx := t.Context()

	// Pass 1: the alias does not exist yet -- fallback-serving.
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	e, ok := reg.Lookup("default-flips")
	require.True(t, ok)
	assert.Empty(t, e.Alias, "fallback entry calls the live function directly")

	got := &fv1.Function{}
	require.NoError(t, c.Get(ctx, key, got))
	cond := meta.FindStatusCondition(got.Status.Conditions, fv1.FunctionConditionToolExposed)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, fv1.FunctionReasonToolAliasFallback, cond.Reason, "fallback-serving must carry a distinct Reason")
	assert.Contains(t, cond.Message, "not-yet-alias", "message must name the unresolved alias")

	// The alias now shows up and resolves to a version.
	v := mkVersion("flips-v1", "flips", fv1.FunctionSpec{
		Tool: &fv1.ToolConfig{Alias: "not-yet-alias", Description: "snap-desc"},
	})
	alias := mkAlias("not-yet-alias", "flips", "flips-v1", "")
	require.NoError(t, c.Create(ctx, v))
	require.NoError(t, c.Create(ctx, alias))

	// Pass 2: resolves successfully -- Reason must flip back.
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	e, ok = reg.Lookup("default-flips")
	require.True(t, ok)
	assert.Equal(t, "snap-desc", e.Description, "now served from the resolved snapshot")
	assert.Equal(t, "not-yet-alias", e.Alias)

	require.NoError(t, c.Get(ctx, key, got))
	cond = meta.FindStatusCondition(got.Status.Conditions, fv1.FunctionConditionToolExposed)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, fv1.FunctionReasonToolExposed, cond.Reason, "must flip back to the normal Reason once resolved")
}

func TestFunctionToolReconcilerDelete(t *testing.T) {
	fn := exposedFn("del", &fv1.ToolConfig{Description: "d"})
	r, reg, c := newReconciler(t, fn)
	key := types.NamespacedName{Namespace: "default", Name: "del"}
	ctx := t.Context()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	require.NotZero(t, reg.Len())

	got := &fv1.Function{}
	require.NoError(t, c.Get(ctx, key, got))
	require.NoError(t, c.Delete(ctx, got))

	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Zero(t, reg.Len(), "tool removed after function deletion")
}

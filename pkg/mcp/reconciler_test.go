// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
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

func TestFunctionToolReconcilerNameConflict(t *testing.T) {
	owner := exposedFn("owner", &fv1.ToolConfig{Description: "d", ToolName: "shared"})
	intruder := exposedFn("intruder", &fv1.ToolConfig{Description: "d", ToolName: "shared"})
	r, reg, c := newReconciler(t, owner, intruder)
	ctx := t.Context()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "owner"}})
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "intruder"}})
	require.NoError(t, err)

	// The owner keeps the name; the intruder is not registered and is marked
	// ToolExposed=False with the conflict reason.
	got, ok := reg.Lookup("shared")
	require.True(t, ok)
	assert.Equal(t, "owner", got.FnName)
	assert.Equal(t, 1, reg.Len())

	gotFn := &fv1.Function{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "intruder"}, gotFn))
	cond := meta.FindStatusCondition(gotFn.Status.Conditions, fv1.FunctionConditionToolExposed)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, fv1.FunctionReasonToolNameConflict, cond.Reason)
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

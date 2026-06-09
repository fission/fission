// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func entry(ns, fn, tool string) ToolEntry {
	return ToolEntry{ToolName: tool, Namespace: ns, FnName: fn, Description: "d", InputSchema: defaultInputSchema}
}

func TestRegistryUpsert(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	res, oldName := r.Upsert(entry("default", "fn1", "t1"))
	assert.Equal(t, UpsertApplied, res)
	assert.Empty(t, oldName)

	// Same entry again: no-op.
	res, oldName = r.Upsert(entry("default", "fn1", "t1"))
	assert.Equal(t, UpsertNoChange, res)
	assert.Equal(t, "t1", oldName)

	// Description change: applied.
	e := entry("default", "fn1", "t1")
	e.Description = "new"
	res, _ = r.Upsert(e)
	assert.Equal(t, UpsertApplied, res)

	got, ok := r.Lookup("t1")
	require.True(t, ok)
	assert.Equal(t, "new", got.Description)
}

func TestRegistryRename(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Upsert(entry("default", "fn1", "old"))

	res, oldName := r.Upsert(entry("default", "fn1", "new"))
	assert.Equal(t, UpsertApplied, res)
	assert.Equal(t, "old", oldName, "rename should report the prior tool name")

	_, ok := r.Lookup("old")
	assert.False(t, ok, "old tool name must be dropped on rename")
	_, ok = r.Lookup("new")
	assert.True(t, ok)
}

func TestRegistryNameConflict(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Upsert(entry("ns-a", "fn1", "shared"))

	// A different function claiming the same tool name is a conflict: nothing
	// changes and fn1's registration is untouched.
	res, _ := r.Upsert(entry("ns-b", "fn2", "shared"))
	assert.Equal(t, UpsertConflict, res)

	got, ok := r.Lookup("shared")
	require.True(t, ok)
	assert.Equal(t, "fn1", got.FnName, "the original owner must keep the name")
	assert.Equal(t, 1, r.Len())
}

func TestRegistryRemoveByFunction(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Upsert(entry("default", "fn1", "t1"))

	oldName, existed := r.RemoveByFunction(types.NamespacedName{Namespace: "default", Name: "fn1"})
	assert.True(t, existed)
	assert.Equal(t, "t1", oldName)
	_, ok := r.Lookup("t1")
	assert.False(t, ok)

	_, existed = r.RemoveByFunction(types.NamespacedName{Namespace: "default", Name: "fn1"})
	assert.False(t, existed, "removing twice is a no-op")
}

func TestRegistryLen(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	assert.Equal(t, 0, r.Len())
	r.Upsert(entry("ns-a", "fn1", "t-a1"))
	r.Upsert(entry("ns-a", "fn2", "t-a2"))
	assert.Equal(t, 2, r.Len())
	r.RemoveByFunction(types.NamespacedName{Namespace: "ns-a", Name: "fn1"})
	assert.Equal(t, 1, r.Len())
}

func TestToolEntryFromFunction(t *testing.T) {
	t.Parallel()

	mkFn := func(tc *fv1.ToolConfig) *fv1.Function {
		return &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "myfn", Namespace: "default"},
			Spec:       fv1.FunctionSpec{Tool: tc},
		}
	}

	t.Run("defaults name and schema", func(t *testing.T) {
		t.Parallel()
		e := toolEntryFromFunction(mkFn(&fv1.ToolConfig{Description: "d"}))
		assert.Equal(t, "default-myfn", e.ToolName)
		assert.JSONEq(t, `{"type":"object"}`, string(e.InputSchema))
	})

	t.Run("honors explicit name and schema", func(t *testing.T) {
		t.Parallel()
		raw := `{"type":"object","properties":{"q":{"type":"string"}}}`
		e := toolEntryFromFunction(mkFn(&fv1.ToolConfig{
			Description: "d", ToolName: "search",
			InputSchema: &apiextensionsv1.JSON{Raw: []byte(raw)},
		}))
		assert.Equal(t, "search", e.ToolName)
		assert.JSONEq(t, raw, string(e.InputSchema))
	})
}

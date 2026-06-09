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

	added, changed, oldName := r.Upsert(entry("default", "fn1", "t1"))
	assert.True(t, added)
	assert.False(t, changed)
	assert.Empty(t, oldName)

	// Same entry again: no-op.
	added, changed, oldName = r.Upsert(entry("default", "fn1", "t1"))
	assert.False(t, added)
	assert.False(t, changed)
	assert.Equal(t, "t1", oldName)

	// Description change: changed.
	e := entry("default", "fn1", "t1")
	e.Description = "new"
	added, changed, _ = r.Upsert(e)
	assert.False(t, added)
	assert.True(t, changed)

	got, ok := r.Lookup("t1")
	require.True(t, ok)
	assert.Equal(t, "new", got.Description)
}

func TestRegistryRename(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Upsert(entry("default", "fn1", "old"))

	added, _, oldName := r.Upsert(entry("default", "fn1", "new"))
	assert.True(t, added)
	assert.Equal(t, "old", oldName, "rename should report the prior tool name")

	_, ok := r.Lookup("old")
	assert.False(t, ok, "old tool name must be dropped on rename")
	_, ok = r.Lookup("new")
	assert.True(t, ok)
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

func TestRegistryListForNamespaces(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Upsert(entry("ns-a", "fn1", "t-a1"))
	r.Upsert(entry("ns-a", "fn2", "t-a2"))
	r.Upsert(entry("ns-b", "fn3", "t-b1"))

	t.Run("wildcard sees all, sorted", func(t *testing.T) {
		t.Parallel()
		got := r.ListForNamespaces(nil, true)
		require.Len(t, got, 3)
		assert.Equal(t, "t-a1", got[0].ToolName)
		assert.Equal(t, "t-a2", got[1].ToolName)
		assert.Equal(t, "t-b1", got[2].ToolName)
	})

	t.Run("scoped to ns-a", func(t *testing.T) {
		t.Parallel()
		got := r.ListForNamespaces([]string{"ns-a"}, false)
		require.Len(t, got, 2)
		for _, e := range got {
			assert.Equal(t, "ns-a", e.Namespace)
		}
	})

	t.Run("no namespaces sees nothing", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, r.ListForNamespaces(nil, false))
	})
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
		e := toolEntryFromFunction(mkFn(&fv1.ToolConfig{ExposeAsMCP: true, Description: "d"}))
		assert.Equal(t, "default-myfn", e.ToolName)
		assert.JSONEq(t, `{"type":"object"}`, string(e.InputSchema))
	})

	t.Run("honors explicit name and schema", func(t *testing.T) {
		t.Parallel()
		raw := `{"type":"object","properties":{"q":{"type":"string"}}}`
		e := toolEntryFromFunction(mkFn(&fv1.ToolConfig{
			ExposeAsMCP: true, Description: "d", ToolName: "search",
			InputSchema: &apiextensionsv1.JSON{Raw: []byte(raw)},
		}))
		assert.Equal(t, "search", e.ToolName)
		assert.JSONEq(t, raw, string(e.InputSchema))
	})
}

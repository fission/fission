// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"slices"
	"sort"
	"sync"

	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// defaultInputSchema is advertised when a tool declares no InputSchema: an open
// object that accepts any arguments.
var defaultInputSchema = json.RawMessage(`{"type":"object"}`)

// ToolEntry is the resolved, agent-facing view of one MCP-exposed Function. The
// tool name → (namespace, function) mapping lives here so the agent never names
// a namespace: it calls a tool by name and the server resolves the target.
type ToolEntry struct {
	ToolName    string
	Namespace   string
	FnName      string
	Description string
	InputSchema json.RawMessage
}

// Registry is the in-memory source of truth for the MCP tool set, maintained by
// the Function reconciler and read by the MCP server on every request. Each
// router/MCP replica keeps its own copy (no leader election), so the reconcile
// is idempotent registry mutation. Safe for concurrent reconcile/serve.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]ToolEntry            // toolName -> entry (uniqueness + tools/call resolution)
	byFn   map[types.NamespacedName]string // function -> toolName (rename/delete bookkeeping)
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		byName: map[string]ToolEntry{},
		byFn:   map[types.NamespacedName]string{},
	}
}

// Upsert inserts or updates the tool for a function. It returns whether the tool
// was newly added, whether an existing tool changed, and the previous tool name
// for that function (empty if none) so the caller can drop a stale registration
// when ToolName changed.
func (r *Registry) Upsert(e ToolEntry) (added, changed bool, oldName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	nn := types.NamespacedName{Namespace: e.Namespace, Name: e.FnName}
	oldName = r.byFn[nn]
	if oldName != "" && oldName != e.ToolName {
		delete(r.byName, oldName)
	}

	prev, existed := r.byName[e.ToolName]
	switch {
	case !existed || oldName != e.ToolName:
		added = true
	case !toolEntryEqual(prev, e):
		changed = true
	}

	r.byName[e.ToolName] = e
	r.byFn[nn] = e.ToolName
	return added, changed, oldName
}

// RemoveByFunction drops the tool registered for a function, returning the tool
// name that was removed (empty if none).
func (r *Registry) RemoveByFunction(nn types.NamespacedName) (oldName string, existed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	oldName, existed = r.byFn[nn]
	if !existed {
		return "", false
	}
	delete(r.byFn, nn)
	delete(r.byName, oldName)
	return oldName, true
}

// Lookup returns the tool entry for a tool name.
func (r *Registry) Lookup(toolName string) (ToolEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byName[toolName]
	return e, ok
}

// ListForNamespaces returns the tools visible to a caller authorized for the
// given namespaces, sorted by tool name for a stable tools/list. A wildcard
// caller sees every tool.
func (r *Registry) ListForNamespaces(allowed []string, wildcard bool) []ToolEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]ToolEntry, 0, len(r.byName))
	for _, e := range r.byName {
		if wildcard || slices.Contains(allowed, e.Namespace) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ToolName < out[j].ToolName })
	return out
}

// toolEntryFromFunction builds the resolved ToolEntry for an MCP-exposed
// function: it defaults the tool name to "<namespace>-<name>" and supplies an
// open object schema when InputSchema is unset. Callers must only pass functions
// whose Tool.ExposeAsMCP is true.
func toolEntryFromFunction(fn *fv1.Function) ToolEntry {
	tc := fn.Spec.Tool
	name := tc.ToolName
	if name == "" {
		name = fn.Namespace + "-" + fn.Name
	}
	schema := defaultInputSchema
	if tc.InputSchema != nil && len(tc.InputSchema.Raw) > 0 {
		schema = json.RawMessage(slices.Clone(tc.InputSchema.Raw))
	}
	return ToolEntry{
		ToolName:    name,
		Namespace:   fn.Namespace,
		FnName:      fn.Name,
		Description: tc.Description,
		InputSchema: schema,
	}
}

func toolEntryEqual(a, b ToolEntry) bool {
	return a.ToolName == b.ToolName &&
		a.Namespace == b.Namespace &&
		a.FnName == b.FnName &&
		a.Description == b.Description &&
		string(a.InputSchema) == string(b.InputSchema)
}

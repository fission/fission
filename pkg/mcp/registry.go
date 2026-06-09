// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"slices"
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

// UpsertResult reports the outcome of an Upsert.
type UpsertResult int

const (
	// UpsertNoChange: the function's tool is already registered identically.
	UpsertNoChange UpsertResult = iota
	// UpsertApplied: the tool was added or changed; the caller should emit an
	// add-delta (and drop oldName first if it differs).
	UpsertApplied
	// UpsertConflict: the desired tool name is already owned by a *different*
	// function; nothing was registered. The caller should not advertise this
	// function and should surface the conflict.
	UpsertConflict
)

// Upsert inserts or updates the tool for a function. It returns the outcome and
// the previous tool name for that function (empty if none) so the caller can
// drop a stale registration when ToolName changed. A tool name already owned by
// a different function is a conflict: nothing is mutated and UpsertConflict is
// returned (the default "<namespace>-<name>" naming never collides; only an
// explicit ToolName override can).
func (r *Registry) Upsert(e ToolEntry) (res UpsertResult, oldName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	nn := types.NamespacedName{Namespace: e.Namespace, Name: e.FnName}
	oldName = r.byFn[nn]

	if existing, ok := r.byName[e.ToolName]; ok && (existing.Namespace != e.Namespace || existing.FnName != e.FnName) {
		return UpsertConflict, oldName
	}

	if oldName != "" && oldName != e.ToolName {
		delete(r.byName, oldName)
	}

	prev, existed := r.byName[e.ToolName]
	r.byName[e.ToolName] = e
	r.byFn[nn] = e.ToolName

	if existed && oldName == e.ToolName && toolEntryEqual(prev, e) {
		return UpsertNoChange, oldName
	}
	return UpsertApplied, oldName
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

// Len returns the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byName)
}

// toolEntryFromFunction builds the resolved ToolEntry for an MCP-exposed
// function: it defaults the tool name to "<namespace>-<name>" and supplies an
// open object schema when InputSchema is unset. Callers must only pass functions
// whose Tool is non-nil.
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

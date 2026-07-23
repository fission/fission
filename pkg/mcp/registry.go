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

	// Alias, when non-empty, is the FunctionAlias name (RFC-0025) tools/call
	// proxies through -- Proxy.Invoke builds the ":<alias>" internal route
	// (utils.UrlForFunctionRef) instead of addressing FnName's live route.
	// Empty (the default) preserves the pre-RFC-0025 behavior.
	Alias string
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

// Upsert inserts or updates the tool for a function. It returns the outcome, the
// previous tool name for that function (empty if none) so the caller can drop a
// stale registration when ToolName changed, and the function evicted from a
// contested name (nil if none).
//
// The default "<namespace>-<name>" naming never collides; only an explicit
// ToolName override can. A contested name is resolved deterministically: the
// lexicographically-smallest "<namespace>/<name>" owns it, so every replica
// converges on the same winner regardless of reconcile order (no leader
// election). When the incoming function wins it evicts the prior owner (returned
// as evicted, so the caller can mark it not-exposed); when it loses it gets
// UpsertConflict and nothing is mutated.
func (r *Registry) Upsert(e ToolEntry) (res UpsertResult, oldName string, evicted *types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()

	nn := types.NamespacedName{Namespace: e.Namespace, Name: e.FnName}
	oldName = r.byFn[nn]

	if existing, ok := r.byName[e.ToolName]; ok && (existing.Namespace != e.Namespace || existing.FnName != e.FnName) {
		if fnKey(e.Namespace, e.FnName) >= fnKey(existing.Namespace, existing.FnName) {
			return UpsertConflict, oldName, nil
		}
		// Incoming wins: evict the prior owner before claiming the name.
		ev := types.NamespacedName{Namespace: existing.Namespace, Name: existing.FnName}
		delete(r.byFn, ev)
		evicted = &ev
	}

	if oldName != "" && oldName != e.ToolName {
		delete(r.byName, oldName)
	}

	prev, existed := r.byName[e.ToolName]
	r.byName[e.ToolName] = e
	r.byFn[nn] = e.ToolName

	if evicted == nil && existed && oldName == e.ToolName && toolEntryEqual(prev, e) {
		return UpsertNoChange, oldName, nil
	}
	return UpsertApplied, oldName, evicted
}

// fnKey is the total ordering used to pick a deterministic winner for a
// contested tool name.
func fnKey(namespace, name string) string { return namespace + "/" + name }

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

// HasFunction reports whether a tool is currently registered for the given
// function. Used by the reconciler to decide, when an alias-addressed
// function's target is momentarily unresolved, whether to keep serving the
// last-registered entry untouched (true) or fall back to the live Function's
// own Tool config because nothing has ever been registered for it yet
// (false).
func (r *Registry) HasFunction(nn types.NamespacedName) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byFn[nn]
	return ok
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
		// Alias is whatever fn.Spec.Tool.Alias says on the fn actually passed
		// in: for the live function this is live's own Alias; for a
		// versioning.VersionedFunction projection it is the resolved
		// snapshot's recorded Alias -- reconciler.go's resolveEntry
		// additionally overrides this to the alias CR's own name after the
		// call, since that is the one value it knows for certain matched
		// (fn.Spec.Tool.Alias could theoretically differ in a stale
		// snapshot).
		Alias: tc.Alias,
	}
}

func toolEntryEqual(a, b ToolEntry) bool {
	return a.ToolName == b.ToolName &&
		a.Namespace == b.Namespace &&
		a.FnName == b.FnName &&
		a.Alias == b.Alias &&
		a.Description == b.Description &&
		string(a.InputSchema) == string(b.InputSchema)
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package routetable holds the router's incremental route state (RFC-0013).
//
// It splits a route's SHAPE (path/prefix/methods/host — changes rarely,
// human-driven) from its HANDLER (function snapshots, canary weights —
// changes constantly). Handlers live behind a stable HandlerRef registered
// into the httpmux mux once per shape; the steady-churn class (canary weight
// ticks, function updates) becomes a single atomic pointer store with zero
// mux rebuild, while shape changes signal the materializer to rebuild.
//
// The package is deliberately free of fission-internal imports: it knows
// nothing about resolution, functionHandlers, or the mux — callers hand it
// shapes and opaque http.Handlers.
package routetable

import (
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// HandlerRef is the stable indirection registered into the mux: requests go
// through one atomic pointer load, swaps go through one store. A request
// entering ServeHTTP sees one consistent handler — one consistent canary
// distribution — for its lifetime; in-flight requests are untouched by swaps
// (the same property the atomic mux swap gives today).
type HandlerRef struct {
	h atomic.Pointer[http.Handler]
}

// NewHandlerRef returns a ref serving h.
func NewHandlerRef(h http.Handler) *HandlerRef {
	r := &HandlerRef{}
	r.h.Store(&h)
	return r
}

// Swap atomically replaces the served handler.
func (r *HandlerRef) Swap(h http.Handler) {
	r.h.Store(&h)
}

func (r *HandlerRef) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if hp := r.h.Load(); hp != nil {
		(*hp).ServeHTTP(w, req)
		return
	}
	// Unreachable by construction (refs are always created with a handler);
	// degrade with a 503 rather than a nil-deref panic.
	http.Error(w, "route handler not initialized", http.StatusServiceUnavailable)
}

// RouteSpec is one HTTPTrigger's derived route: identity + change-detection
// fields, the match shape, and the swappable handler. Shape equality drives
// the materializer; (TriggerGen, FnGens, AliasGens, StickyGen) equality
// drives handler swaps.
type RouteSpec struct {
	// Identity / change detection. Generations (not ResourceVersions) on
	// purpose: the reconcilers are registered with
	// GenerationChangedPredicate, so status-only writes (the router's own
	// condition updates, the executor's function readiness) never reach the
	// event path — and the resync must use the SAME notion of "changed", or
	// every status write would count as drift and rebuild a handler.
	TriggerUID types.UID
	Namespace  string
	Name       string
	TriggerGen int64
	// FnGens maps each resolved backend's BackendKey (RFC-0025: "name" for
	// the live Function, "name@version" for a pinned FunctionVersion
	// snapshot) to its Generation. It supersedes TTL-based invalidation with
	// precise per-trigger change detection: a function spec change bumps its
	// generation, which makes the apply a HandlerSwapped instead of a
	// NoChange. reindexLocked strips the "@version" suffix back to the live
	// function's name when populating fnIndex — see its doc comment.
	FnGens map[string]int64

	// Match shape. Exactly one of ExactPath / PrefixPath may be empty; a
	// non-slash trigger Prefix sets BOTH (the dual-registration pair —
	// exact `/api` plus subtree `/api/` — which must always swap together,
	// hence one spec).
	ExactPath  string
	PrefixPath string
	Host       string
	Methods    []string // sorted by the caller

	// Created is the trigger's creationTimestamp — the phase-2 precedence
	// tiebreak for exact-duplicate shapes.
	Created metav1.Time

	// AliasGens maps each FunctionAlias name this route's resolution consumed
	// (RFC-0025) to that alias's observed Generation: populated from
	// resolveResult.AliasGens, which only resolveByAlias sets (a plain name,
	// version-pinned, or FunctionWeights reference never references an
	// alias). The KEYS are the index input: reindexLocked mirrors them into
	// the table's aliasIndex, so a FunctionAlias event (a repoint) can find
	// exactly the triggers to re-apply via TriggersForAlias — the same
	// cascade fnIndex/FnGens gives function events. The VALUES are change
	// detection: a weight-only alias edit (`fission alias update --weight`,
	// or an alias-mode canary stepping its split) bumps ONLY the alias's own
	// spec Generation — the resolved BackendKey set and every pinned
	// snapshot's Generation are unchanged — so without the alias Generation
	// in the route identity ApplyTrigger would answer NoChange and the
	// handler closure would keep serving the stale weight distribution.
	// Symmetric with the internal `:<alias>` route, whose
	// aliasRouteGeneration folds alias.Generation for exactly this reason.
	AliasGens map[string]int64

	// StickyGen is the Generation of the resolution's sticky-config source
	// function (resolveResult.stickySource; 0 when there is none — the
	// legacy FunctionWeights canary). For a plain or version-pinned
	// reference it duplicates a Generation already present in FnGens; for an
	// alias-resolved route the sticky source is the LIVE function, whose
	// Generation appears nowhere in FnGens (only the pinned snapshots' do) —
	// without it, a live Spec.State.Sticky edit would never reach an
	// alias-routed trigger's handler: the function-event cascade would
	// re-apply the trigger only for ApplyTrigger to answer NoChange.
	StickyGen int64

	// Handler is the stable ref registered into the mux. Owned by the
	// table: ApplyTrigger sets it on insert and preserves it across shape
	// changes and handler swaps.
	Handler *HandlerRef
}

// shapeEqual reports whether two specs register identical mux routes.
func (s *RouteSpec) shapeEqual(o *RouteSpec) bool {
	return s.ExactPath == o.ExactPath &&
		s.PrefixPath == o.PrefixPath &&
		s.Host == o.Host &&
		slices.Equal(s.Methods, o.Methods)
}

// InternalKey identifies one internal-listener route
// (/fission-function/...). Suffix is "" for a live Function's own route; a
// non-empty Suffix materializes the RFC-0025 tag grammar the internal URL
// carries after the function name — an alias's CR name for a `:<alias>`
// route, or a FunctionVersion's CR name for a `:<version>` route (both
// addressing that function's namespace). Suffix is part of the route's
// identity, not its handler: two InternalSpecs sharing a NamespacedName but
// differing Suffix are two independent routes (the plain function route and
// its `:<alias>`/`:<version>` siblings all coexist and are applied,
// swapped, and deleted independently).
//
// This package has no way to tell an alias-sourced Suffix from a
// version-sourced one — a FunctionAlias and a FunctionVersion of the same
// function sharing a name would collide on the identical InternalKey. The
// FunctionAlias admission webhook (v1 package's aliasNameShadowsVersionScheme)
// is what makes that impossible for a well-formed alias, by rejecting any
// name matching the FunctionVersion naming scheme for its own function; this
// package intentionally does not re-enforce it (a hand-crafted/legacy object
// that bypassed the webhook simply falls to last-writer-wins, same as any
// other unenforced invariant here).
type InternalKey struct {
	types.NamespacedName
	Suffix string
}

// InternalSpec is one internal-listener route
// (/fission-function/...[:<suffix>]). Its shape is fully derived from Key,
// so it never shape-changes in place — only insert and delete touch the
// internal mux.
type InternalSpec struct {
	Key InternalKey
	// Revision is any value whose change means the handler must be rebuilt —
	// the live Function's Generation for plain internal routes, a derived
	// identity hash for alias/version routes.
	Revision int64
	Handler  *HandlerRef
}

// ApplyResult tells the caller what an apply did — and therefore what it
// owes: nothing (NoChange), nothing (HandlerSwapped — the swap is already
// live), or a debounced materializer signal (ShapeChanged).
type ApplyResult int

const (
	NoChange ApplyResult = iota
	HandlerSwapped
	ShapeChanged
)

func (r ApplyResult) String() string {
	switch r {
	case NoChange:
		return "no_change"
	case HandlerSwapped:
		return "handler_swapped"
	case ShapeChanged:
		return "shape_changed"
	default:
		return "unknown"
	}
}

// Table is the routing state both listeners are materialized from. All
// methods are safe for concurrent use; HandlerRef swaps inside an apply are
// visible to in-flight traffic immediately (that is the point), while shape
// mutations only become visible when the materializer next snapshots.
type Table struct {
	mu       sync.Mutex
	public   map[types.UID]*RouteSpec
	internal map[InternalKey]*InternalSpec
	// fnIndex maps a function to the triggers (by UID) whose routes resolve
	// through it, so a function event can re-apply exactly the affected
	// triggers. See biIndex's doc comment for why it (and aliasIndex) is
	// keyed on types.UID while unresolved/unresolvedAlias are keyed on
	// types.NamespacedName.
	fnIndex biIndex[types.UID]
	// aliasIndex maps a FunctionAlias to the triggers whose routes were
	// resolved through it (RouteSpec.AliasGens keys), mirroring fnIndex so an
	// alias event can re-apply exactly the affected triggers via
	// TriggersForAlias.
	aliasIndex biIndex[types.UID]
	// unresolved maps a function to the triggers (by namespace/name) that
	// REFERENCE it but could not resolve (the function does not exist yet).
	// It keeps the function-create cascade working for the
	// trigger-before-function apply ordering: without it, the route's
	// removal would drop the index edge and the trigger would only re-admit
	// at the next resync.
	unresolved biIndex[types.NamespacedName]
	// unresolvedAlias mirrors unresolved for FunctionAlias references: a
	// trigger whose alias does not exist (or has not resolved) yet is
	// indexed here so the alias's create/first-resolve event admits it
	// immediately, the same guarantee unresolved gives function references.
	unresolvedAlias biIndex[types.NamespacedName]
}

func New() *Table {
	return &Table{
		public:          make(map[types.UID]*RouteSpec),
		internal:        make(map[InternalKey]*InternalSpec),
		fnIndex:         newBiIndex[types.UID](),
		aliasIndex:      newBiIndex[types.UID](),
		unresolved:      newBiIndex[types.NamespacedName](),
		unresolvedAlias: newBiIndex[types.NamespacedName](),
	}
}

// ApplyTrigger reconciles one trigger's spec into the table. build is called
// only when a new handler is actually needed (insert, handler swap, or shape
// change) — a NoChange apply never builds.
//
// The HandlerRef's identity is preserved across shape changes so the
// materializer's next snapshot re-registers the same ref the swap path keeps
// updating.
func (t *Table) ApplyTrigger(spec *RouteSpec, build func() http.Handler) ApplyResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.clearUnresolvedLocked(types.NamespacedName{Namespace: spec.Namespace, Name: spec.Name})

	old, ok := t.public[spec.TriggerUID]
	if !ok {
		spec.Handler = NewHandlerRef(build())
		t.public[spec.TriggerUID] = spec
		t.reindexLocked(spec)
		return ShapeChanged
	}
	if old.shapeEqual(spec) {
		if old.TriggerGen == spec.TriggerGen && maps.Equal(old.FnGens, spec.FnGens) &&
			maps.Equal(old.AliasGens, spec.AliasGens) && old.StickyGen == spec.StickyGen {
			return NoChange
		}
		old.Handler.Swap(build())
		old.TriggerGen = spec.TriggerGen
		old.FnGens = spec.FnGens
		old.AliasGens = spec.AliasGens
		old.StickyGen = spec.StickyGen
		old.Created = spec.Created
		old.Namespace, old.Name = spec.Namespace, spec.Name
		t.reindexLocked(old)
		return HandlerSwapped
	}
	// Shape changed: keep the ref (stable identity), swap its handler, and
	// replace the spec; the caller owes the materializer a signal.
	spec.Handler = old.Handler
	spec.Handler.Swap(build())
	t.public[spec.TriggerUID] = spec
	t.reindexLocked(spec)
	return ShapeChanged
}

// DeleteTrigger removes a trigger's route. Returns ShapeChanged when a route
// was actually removed (the caller owes a materializer signal), NoChange when
// the trigger was unknown.
func (t *Table) DeleteTrigger(uid types.UID) ApplyResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	spec, ok := t.public[uid]
	if !ok {
		return NoChange
	}
	t.clearUnresolvedLocked(types.NamespacedName{Namespace: spec.Namespace, Name: spec.Name})
	delete(t.public, uid)
	t.fnIndex.dropTrigger(uid)
	t.aliasIndex.dropTrigger(uid)
	return ShapeChanged
}

// DeleteTriggerByName removes the route of the trigger with the given
// namespace/name — the form a delete event arrives in (the object, and with
// it the UID, is already gone). Linear over the table; deletes are rare,
// human-driven events. Removes every matching UID (a missed delete event
// followed by a recreate can briefly leave two UIDs for one name).
func (t *Table) DeleteTriggerByName(key types.NamespacedName) ApplyResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.clearUnresolvedLocked(key)
	res := NoChange
	for uid, spec := range t.public {
		if spec.Namespace == key.Namespace && spec.Name == key.Name {
			delete(t.public, uid)
			t.fnIndex.dropTrigger(uid)
			t.aliasIndex.dropTrigger(uid)
			res = ShapeChanged
		}
	}
	return res
}

// ApplyFunction reconciles one internal-listener route, keyed by an
// InternalKey: the live function's own route (Suffix "", from
// applyFunctionIncremental/resync) or a materialized `:<alias>`/`:<version>`
// route (RFC-0025, from the alias/version reconcilers) — both share this one
// insert/swap/delete contract, so an alias repoint is exactly the same
// HandlerSwapped path a function spec change already takes. Insert is a
// ShapeChanged (the internal mux gains the route pair); a revision change
// (revision is any value whose change means the handler must be rebuilt —
// the live Function's Generation for plain internal routes, a derived
// identity hash for alias/version routes; see versioning.VersionedFunction,
// which makes each distinct FunctionVersion's Generation unique) is a pure
// handler swap; same revision is NoChange.
func (t *Table) ApplyFunction(key InternalKey, revision int64, build func() http.Handler) ApplyResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	old, ok := t.internal[key]
	if !ok {
		t.internal[key] = &InternalSpec{Key: key, Revision: revision, Handler: NewHandlerRef(build())}
		return ShapeChanged
	}
	if old.Revision == revision {
		return NoChange
	}
	old.Handler.Swap(build())
	old.Revision = revision
	return HandlerSwapped
}

// DeleteFunction removes one internal-listener route by its exact key.
func (t *Table) DeleteFunction(key InternalKey) ApplyResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.internal[key]; !ok {
		return NoChange
	}
	delete(t.internal, key)
	return ShapeChanged
}

// InternalKeysBySuffix returns every internal route key in namespace whose
// Suffix matches — the CANDIDATE set for a FunctionAlias/FunctionVersion
// DELETE event, where the deleted object (and with it the specific function
// it belonged to) is already gone. Suffix alone does not identify which
// key.Name (function) it actually orphaned: an alias named "hello-v1" on
// function "world" and a FunctionVersion named "hello-v1" on function
// "hello" are two DISTINCT InternalKeys that both match
// InternalKeysBySuffix(ns, "hello-v1") — deleting one must not remove the
// other's still-live route. Callers (incremental.go's
// deleteInternalRouteBySuffix) resolve each candidate against the live
// FunctionAlias/FunctionVersion objects before deleting; this method is
// deliberately READ-ONLY so that scoping decision cannot be skipped by
// accident. Linear over the internal table; deletes are rare, human- or
// GC-driven events.
func (t *Table) InternalKeysBySuffix(namespace, suffix string) []InternalKey {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []InternalKey
	for key := range t.internal {
		if key.Namespace == namespace && key.Suffix == suffix {
			out = append(out, key)
		}
	}
	slices.SortFunc(out, cmpInternalKey)
	return out
}

// MarkUnresolved records that a trigger references the given functions and/or
// FunctionAliases but could not resolve (the referenced object is missing, or
// an alias has not resolved to a version yet). The next ApplyTrigger or
// delete for the trigger clears the entry; a function event for any
// referenced function includes the trigger in TriggersForFunction, and an
// alias event for any referenced alias includes it in TriggersForAlias, so
// either cascade re-applies it immediately instead of waiting for the resync.
func (t *Table) MarkUnresolved(trigger types.NamespacedName, fns []types.NamespacedName, aliases []types.NamespacedName) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.unresolved.reindex(trigger, fns)
	t.unresolvedAlias.reindex(trigger, aliases)
}

// clearUnresolvedLocked drops a trigger's unresolved edges (both function and
// alias). Caller holds t.mu.
func (t *Table) clearUnresolvedLocked(trigger types.NamespacedName) {
	t.unresolved.dropTrigger(trigger)
	t.unresolvedAlias.dropTrigger(trigger)
}

// TriggersForFunction returns the namespaced names of the triggers whose
// routes resolve through the given function — plus the triggers that
// reference it but could not resolve yet — so a function event can re-apply
// exactly the affected triggers (re-resolve + handler swap / re-admit).
func (t *Table) TriggersForFunction(key types.NamespacedName) []types.NamespacedName {
	t.mu.Lock()
	defer t.mu.Unlock()
	uids := t.fnIndex.edges(key)
	seen := make(map[types.NamespacedName]struct{}, len(uids))
	out := make([]types.NamespacedName, 0, len(uids))
	for uid := range uids {
		if spec, ok := t.public[uid]; ok {
			k := types.NamespacedName{Namespace: spec.Namespace, Name: spec.Name}
			if _, dup := seen[k]; !dup {
				seen[k] = struct{}{}
				out = append(out, k)
			}
		}
	}
	for trigger := range t.unresolved.edges(key) {
		if _, dup := seen[trigger]; !dup {
			seen[trigger] = struct{}{}
			out = append(out, trigger)
		}
	}
	// Deterministic order for tests and log readability.
	slices.SortFunc(out, cmpNamespacedName)
	return out
}

// TriggersForAlias returns the namespaced names of the triggers whose routes
// were resolved through the named FunctionAlias — plus the triggers that
// reference it but could not resolve yet — mirroring TriggersForFunction so
// an alias event (a repoint, or its first resolution) can re-apply exactly
// the affected triggers.
func (t *Table) TriggersForAlias(namespace, name string) []types.NamespacedName {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := types.NamespacedName{Namespace: namespace, Name: name}
	uids := t.aliasIndex.edges(key)
	seen := make(map[types.NamespacedName]struct{}, len(uids))
	out := make([]types.NamespacedName, 0, len(uids))
	for uid := range uids {
		if spec, ok := t.public[uid]; ok {
			k := types.NamespacedName{Namespace: spec.Namespace, Name: spec.Name}
			if _, dup := seen[k]; !dup {
				seen[k] = struct{}{}
				out = append(out, k)
			}
		}
	}
	for trigger := range t.unresolvedAlias.edges(key) {
		if _, dup := seen[trigger]; !dup {
			seen[trigger] = struct{}{}
			out = append(out, trigger)
		}
	}
	slices.SortFunc(out, cmpNamespacedName)
	return out
}

// PublicTriggers returns the UID → namespace/name of every trigger in the
// public table — the resync loop diffs this against the live trigger list to
// drop deleted routes whose delete events were missed (re-checking by name
// first, so a trigger created mid-pass is not torn down by its own race).
func (t *Table) PublicTriggers() map[types.UID]types.NamespacedName {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[types.UID]types.NamespacedName, len(t.public))
	for uid, spec := range t.public {
		out[uid] = types.NamespacedName{Namespace: spec.Namespace, Name: spec.Name}
	}
	return out
}

// InternalKeys returns the keys currently in the internal table, for the
// resync diff: Suffix "" entries against the live Function list, non-empty
// Suffix entries against the live FunctionAlias/FunctionVersion lists.
func (t *Table) InternalKeys() []InternalKey {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]InternalKey, 0, len(t.internal))
	for key := range t.internal {
		out = append(out, key)
	}
	return out
}

// Snapshot returns copies of the public route specs (sharing the live
// HandlerRefs — that is the point) in a deterministic order, for the
// materializer. Phase 1 orders by namespace/name: order only matters when
// two routes overlap, where today's behavior is accidental list order;
// phase 2 replaces this with the specified precedence sort.
func (t *Table) Snapshot() []RouteSpec {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]RouteSpec, 0, len(t.public))
	for _, spec := range t.public {
		out = append(out, *spec)
	}
	slices.SortFunc(out, func(a, b RouteSpec) int {
		return cmpNamespacedName(
			types.NamespacedName{Namespace: a.Namespace, Name: a.Name},
			types.NamespacedName{Namespace: b.Namespace, Name: b.Name})
	})
	return out
}

// InternalSnapshot returns copies of the internal route specs (sharing live
// HandlerRefs) in a deterministic order.
func (t *Table) InternalSnapshot() []InternalSpec {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]InternalSpec, 0, len(t.internal))
	for _, spec := range t.internal {
		out = append(out, *spec)
	}
	slices.SortFunc(out, func(a, b InternalSpec) int {
		return cmpInternalKey(a.Key, b.Key)
	})
	return out
}

// Sizes returns the current route counts (public triggers, internal
// functions) for the routes gauge.
func (t *Table) Sizes() (public, internal int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.public), len(t.internal)
}

// reindexLocked rebuilds the fn index entries for one trigger from its
// current FnRVs. Caller holds t.mu.
//
// FnGens keys are BackendKeys (RFC-0025): a versioned backend's key is
// "name@version", not the live Function's name. The index must still be
// keyed on the live function's NamespacedName — applyFunctionIncremental /
// reapplyTriggersForFunction look triggers up by the REAL function name on
// a live-Function event, and never see "name@version" — so every FnGens key
// is stripped back to its function name (ParseBackendKey) before indexing.
// Without this, a versioned trigger's route would never re-resolve on its
// function's live events (spec change, delete): the index entry would sit
// under a key {ns, "name@version"} the cascade never looks up.
func (t *Table) reindexLocked(spec *RouteSpec) {
	keys := make([]types.NamespacedName, 0, len(spec.FnGens))
	for backendKey := range spec.FnGens {
		fnName, _ := ParseBackendKey(backendKey)
		keys = append(keys, types.NamespacedName{Namespace: spec.Namespace, Name: fnName})
	}
	t.fnIndex.reindex(spec.TriggerUID, keys)

	aliasKeys := make([]types.NamespacedName, 0, len(spec.AliasGens))
	for alias := range spec.AliasGens {
		aliasKeys = append(aliasKeys, types.NamespacedName{Namespace: spec.Namespace, Name: alias})
	}
	t.aliasIndex.reindex(spec.TriggerUID, aliasKeys)
}

func cmpNamespacedName(a, b types.NamespacedName) int {
	if c := strings.Compare(a.Namespace, b.Namespace); c != 0 {
		return c
	}
	return strings.Compare(a.Name, b.Name)
}

// cmpInternalKey orders internal routes deterministically: namespace, then
// function name, then suffix (so a function's own route sorts before its
// `:<alias>`/`:<version>` siblings).
func cmpInternalKey(a, b InternalKey) int {
	if c := cmpNamespacedName(a.NamespacedName, b.NamespacedName); c != 0 {
		return c
	}
	return strings.Compare(a.Suffix, b.Suffix)
}

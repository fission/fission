// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package routetable

import "k8s.io/apimachinery/pkg/types"

// biIndex maintains a bidirectional many-to-many edge set between
// NamespacedName keys (functions or FunctionAliases) and trigger identities
// of type K, giving O(1) forward lookup (edges) and O(len(reverse[trigger]))
// per-trigger cleanup (dropTrigger) without scanning the whole table.
//
// Table uses two instantiations with DIFFERENT trigger-identity types, which
// is why this is generic rather than a single concrete type: fnIndex and
// aliasIndex key on types.UID (a trigger's resolved route is only known by
// UID — that's the public table's key), while unresolved and
// unresolvedAlias key on types.NamespacedName (an unresolved reference is
// recorded before the trigger necessarily has a UID entry in the public
// table, e.g. across a trigger-before-function apply ordering, and is always
// looked up/cleared by name). The two pairs are otherwise identical
// bookkeeping, previously hand-rolled four times (fnIndex/triggerFns,
// aliasIndex/triggerAliases, unresolved/unresolvedFns,
// unresolvedAlias/unresolvedAliasFns) with add/drop logic duplicated in each
// copy.
//
// Not safe for concurrent use on its own: every biIndex method is called
// only from under Table.mu, and biIndex adds no locking of its own.
type biIndex[K comparable] struct {
	// forward maps an edge key (a function or FunctionAlias) to the set of
	// triggers currently referencing it.
	forward map[types.NamespacedName]map[K]struct{}
	// reverse maps a trigger to the edge keys it currently references, so
	// dropTrigger can remove exactly its forward entries without a scan.
	reverse map[K][]types.NamespacedName
}

func newBiIndex[K comparable]() biIndex[K] {
	return biIndex[K]{
		forward: make(map[types.NamespacedName]map[K]struct{}),
		reverse: make(map[K][]types.NamespacedName),
	}
}

// add records one edge between key and trigger in forward only. It does not
// touch reverse — callers use it as the inner step of reindex, which owns
// the reverse side once for the whole edge set being installed.
func (b *biIndex[K]) add(key types.NamespacedName, trigger K) {
	set, ok := b.forward[key]
	if !ok {
		set = make(map[K]struct{})
		b.forward[key] = set
	}
	set[trigger] = struct{}{}
}

// dropTrigger removes every edge currently recorded for trigger, using
// reverse to find them directly instead of scanning forward.
func (b *biIndex[K]) dropTrigger(trigger K) {
	for _, key := range b.reverse[trigger] {
		if set, ok := b.forward[key]; ok {
			delete(set, trigger)
			if len(set) == 0 {
				delete(b.forward, key)
			}
		}
	}
	delete(b.reverse, trigger)
}

// reindex replaces trigger's entire edge set with keys: drops whatever it
// had before, adds an edge for each of keys, and records keys as the new
// reverse entry (so a later dropTrigger/reindex finds exactly these).
func (b *biIndex[K]) reindex(trigger K, keys []types.NamespacedName) {
	b.dropTrigger(trigger)
	for _, key := range keys {
		b.add(key, trigger)
	}
	b.reverse[trigger] = keys
}

// edges returns the live trigger set referencing key, or nil when there are
// none. Callers only range over the result; it must not be mutated.
func (b *biIndex[K]) edges(key types.NamespacedName) map[K]struct{} {
	return b.forward[key]
}

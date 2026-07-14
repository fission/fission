// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/fission/fission/pkg/statestore"
)

// kvKey is the composite primary key of a KV entry.
type kvKey struct {
	ns, owner, keyspace, key string
}

// kvEntry is a stored value. A zero expiresAt means no expiry.
type kvEntry struct {
	data      []byte
	version   int64
	expiresAt time.Time
}

func scopeKey(s statestore.Scope, key string) kvKey {
	return kvKey{ns: s.Namespace, owner: s.Owner, keyspace: s.Keyspace, key: key}
}

// expired reports whether e has a TTL that has elapsed by now. Expiry is
// inclusive of the boundary (invariant K2): at exactly expiresAt the entry is
// gone.
func (e kvEntry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && !now.Before(e.expiresAt)
}

// liveEntry returns the entry for k only if it is present and not expired.
// Caller holds s.mu.
func (s *Store) liveEntry(k kvKey, now time.Time) (kvEntry, bool) {
	e, ok := s.kv[k]
	if !ok || e.expired(now) {
		return kvEntry{}, false
	}
	return e, true
}

// Get implements statestore.KVStore.
func (s *Store) Get(_ context.Context, scope statestore.Scope, key string) (statestore.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return statestore.Value{}, statestore.ErrClosed
	}
	e, ok := s.liveEntry(scopeKey(scope, key), time.Now())
	if !ok {
		return statestore.Value{}, statestore.ErrNotFound
	}
	// Copy the bytes so callers cannot mutate stored state.
	out := make([]byte, len(e.data))
	copy(out, e.data)
	return statestore.Value{Data: out, Version: e.version}, nil
}

// Set implements statestore.KVStore, honoring the IfVersion CAS semantics and
// TTL from o. An expired key counts as absent.
func (s *Store) Set(_ context.Context, scope statestore.Scope, key string, val []byte, o statestore.SetOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return statestore.ErrClosed
	}
	now := time.Now()
	k := scopeKey(scope, key)
	cur, exists := s.liveEntry(k, now)

	// IfVersion is a compare-and-swap on the current version, treating an absent
	// key as version 0. So IfVersion==0 is create-only, IfVersion==N requires the
	// key to exist at version N, and nil skips the check entirely.
	if o.IfVersion != nil {
		curVersion := int64(0)
		if exists {
			curVersion = cur.version
		}
		if curVersion != *o.IfVersion {
			return statestore.ErrVersionConflict
		}
	}

	next := kvEntry{version: cur.version + 1}
	if !exists {
		next.version = 1
	}
	next.data = make([]byte, len(val))
	copy(next.data, val)
	if o.TTL > 0 {
		next.expiresAt = now.Add(o.TTL)
	}
	s.kv[k] = next
	return nil
}

// Delete implements statestore.KVStore. ifVersion <= 0 deletes unconditionally
// (idempotent for an absent key); a positive ifVersion is a CAS delete.
func (s *Store) Delete(_ context.Context, scope statestore.Scope, key string, ifVersion int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return statestore.ErrClosed
	}
	k := scopeKey(scope, key)
	cur, exists := s.liveEntry(k, time.Now())
	if ifVersion > 0 {
		if !exists || cur.version != ifVersion {
			return statestore.ErrVersionConflict
		}
	}
	delete(s.kv, k)
	return nil
}

// List implements statestore.KVStore: lexicographically ordered keys under
// prefix, paginated via page.Token (the last key of the previous page).
func (s *Store) List(_ context.Context, scope statestore.Scope, prefix string, page statestore.Page) (statestore.KeyPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return statestore.KeyPage{}, statestore.ErrClosed
	}
	now := time.Now()
	var keys []string
	for k, e := range s.kv {
		if k.ns != scope.Namespace || k.owner != scope.Owner || k.keyspace != scope.Keyspace {
			continue
		}
		if e.expired(now) || !strings.HasPrefix(k.key, prefix) {
			continue
		}
		if page.Token != "" && k.key <= page.Token {
			continue
		}
		keys = append(keys, k.key)
	}
	sort.Strings(keys)

	limit := page.Limit
	if limit <= 0 || limit >= len(keys) {
		return statestore.KeyPage{Keys: keys}, nil
	}
	return statestore.KeyPage{Keys: keys[:limit], Next: keys[limit-1]}, nil
}

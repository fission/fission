// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
)

func newKV(t *testing.T) statestore.KVStore {
	t.Helper()
	s := newStore()
	kv, err := s.KV()
	require.NoError(t, err)
	return kv
}

var testScope = statestore.Scope{Namespace: "ns", Owner: "function/f", Keyspace: "k"}

// K1: compare-and-swap is linearizable per key — a stale CAS never lands.
func TestMemoryKV_K1_CASLinearizable_NoLostUpdate(t *testing.T) {
	t.Parallel()
	kv := newKV(t)
	ctx := t.Context()

	// create-only (expect version 0 == absent) -> version 1
	require.NoError(t, kv.Set(ctx, testScope, "x", []byte("0"), statestore.SetOptions{IfVersion: new(int64(0))}))
	v, err := kv.Get(ctx, testScope, "x")
	require.NoError(t, err)
	require.EqualValues(t, 1, v.Version)
	require.Equal(t, []byte("0"), v.Data)

	// create-only again -> conflict (already exists)
	require.ErrorIs(t, kv.Set(ctx, testScope, "x", []byte("y"), statestore.SetOptions{IfVersion: new(int64(0))}), statestore.ErrVersionConflict)

	// CAS on version 1 -> version 2
	require.NoError(t, kv.Set(ctx, testScope, "x", []byte("a"), statestore.SetOptions{IfVersion: new(int64(1))}))
	v, err = kv.Get(ctx, testScope, "x")
	require.NoError(t, err)
	require.EqualValues(t, 2, v.Version)

	// CAS on the now-stale version 1 -> conflict
	require.ErrorIs(t, kv.Set(ctx, testScope, "x", []byte("b"), statestore.SetOptions{IfVersion: new(int64(1))}), statestore.ErrVersionConflict)

	// unconditional set -> version 3
	require.NoError(t, kv.Set(ctx, testScope, "x", []byte("c"), statestore.SetOptions{}))
	v, err = kv.Get(ctx, testScope, "x")
	require.NoError(t, err)
	require.EqualValues(t, 3, v.Version)
}

// K2: TTL is exact on read — an expired key is never returned, even before any
// sweeper runs. Uses a synctest bubble so virtual time is deterministic.
func TestMemoryKV_K2_TTLExactOnRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newKV(t)
		ctx := t.Context()
		require.NoError(t, kv.Set(ctx, testScope, "x", []byte("v"), statestore.SetOptions{TTL: time.Hour}))

		time.Sleep(59 * time.Minute)
		_, err := kv.Get(ctx, testScope, "x")
		require.NoError(t, err) // still live

		time.Sleep(2 * time.Minute) // total 61m > 1h TTL
		_, err = kv.Get(ctx, testScope, "x")
		require.ErrorIs(t, err, statestore.ErrNotFound) // expired, before any sweeper
	})
}

func TestMemoryKV_GetNotFound(t *testing.T) {
	t.Parallel()
	kv := newKV(t)
	_, err := kv.Get(t.Context(), testScope, "absent")
	require.ErrorIs(t, err, statestore.ErrNotFound)
}

func TestMemoryKV_Delete(t *testing.T) {
	t.Parallel()
	kv := newKV(t)
	ctx := t.Context()

	// unconditional delete of an absent key is idempotent.
	require.NoError(t, kv.Delete(ctx, testScope, "gone", 0))

	require.NoError(t, kv.Set(ctx, testScope, "x", []byte("v"), statestore.SetOptions{}))
	// CAS delete with a wrong version -> conflict, key survives.
	require.ErrorIs(t, kv.Delete(ctx, testScope, "x", 99), statestore.ErrVersionConflict)
	_, err := kv.Get(ctx, testScope, "x")
	require.NoError(t, err)
	// CAS delete with the right version -> gone.
	require.NoError(t, kv.Delete(ctx, testScope, "x", 1))
	_, err = kv.Get(ctx, testScope, "x")
	require.ErrorIs(t, err, statestore.ErrNotFound)
}

func TestMemoryKV_ScopeIsolation(t *testing.T) {
	t.Parallel()
	kv := newKV(t)
	ctx := t.Context()
	a := statestore.Scope{Namespace: "ns", Owner: "function/a", Keyspace: "k"}
	b := statestore.Scope{Namespace: "ns", Owner: "function/b", Keyspace: "k"}
	require.NoError(t, kv.Set(ctx, a, "shared", []byte("A"), statestore.SetOptions{}))
	_, err := kv.Get(ctx, b, "shared")
	require.ErrorIs(t, err, statestore.ErrNotFound) // b cannot see a's key
}

func TestMemoryKV_ListPrefixAndPaging(t *testing.T) {
	t.Parallel()
	kv := newKV(t)
	ctx := t.Context()
	for _, k := range []string{"a1", "a2", "a3", "b1"} {
		require.NoError(t, kv.Set(ctx, testScope, k, []byte("v"), statestore.SetOptions{}))
	}
	// prefix filter
	page, err := kv.List(ctx, testScope, "a", statestore.Page{})
	require.NoError(t, err)
	require.Equal(t, []string{"a1", "a2", "a3"}, page.Keys)
	require.Empty(t, page.Next)

	// pagination with Limit
	p1, err := kv.List(ctx, testScope, "a", statestore.Page{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, []string{"a1", "a2"}, p1.Keys)
	require.Equal(t, "a2", p1.Next)
	p2, err := kv.List(ctx, testScope, "a", statestore.Page{Limit: 2, Token: p1.Next})
	require.NoError(t, err)
	require.Equal(t, []string{"a3"}, p2.Keys)
	require.Empty(t, p2.Next)
}

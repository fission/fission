// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package statestoretest holds the shared, driver-independent conformance suite
// for statestore drivers. Running one suite against the memory, Postgres, SQLite,
// and embedded-client drivers is what makes "consumers are identical across
// modes" a tested claim rather than a slogan.
package statestoretest

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
)

// Factory builds a fresh, empty Capabilities for one subtest. Implementations
// should register their own teardown with t.Cleanup (close connections, drop
// schemas). A capability the driver does not provide must return
// statestore.ErrCapabilityUnavailable from its accessor; the suite skips it.
type Factory func(t *testing.T) statestore.Capabilities

var confScope = statestore.Scope{Namespace: "ns", Owner: "function/conf", Keyspace: "ks"}

// RunConformance runs the time-independent behavioral matrix as subtests against
// the driver built by newCaps. Absent capabilities are skipped, not failed. Every
// driver (memory, SQLite, Postgres, embedded client) runs this.
//
// Time-dependent behavior (TTL expiry, lease expiry) is checked separately by
// RunTimingConformance, which uses testing/synctest and so runs only against
// in-process drivers.
func RunConformance(t *testing.T, newCaps Factory) {
	t.Helper()
	t.Run("KV", func(t *testing.T) { runKV(t, newCaps) })
	t.Run("EventLog", func(t *testing.T) { runEventLog(t, newCaps) })
	t.Run("Queue", func(t *testing.T) { runQueue(t, newCaps) })
}

// RunTimingConformance checks the time-dependent behavior (K2 exact-on-read TTL,
// Q2 the lease epoch guard across a re-lease, and dead-lettering on exhausted
// lease expiry) inside testing/synctest bubbles. Because synctest requires
// virtualized time and no real I/O, only in-process drivers (memory, SQLite)
// can run it; networked drivers (Postgres, the embedded HTTP client) share the
// same sqlstore timing code, which this verifies once, and add a real-time smoke
// test of their own.
func RunTimingConformance(t *testing.T, newCaps Factory) {
	t.Helper()
	t.Run("KV/TTLExactOnRead", func(t *testing.T) { runTTLExactOnRead(t, newCaps) })
	t.Run("Queue/Q2_EpochGuard", func(t *testing.T) { runQ2EpochGuard(t, newCaps) })
	t.Run("Queue/ExhaustedByExpiryDeadLettered", func(t *testing.T) { runExhaustedByExpiry(t, newCaps) })
}

func kvOrSkip(t *testing.T, newCaps Factory) statestore.KVStore {
	t.Helper()
	kv, err := newCaps(t).KV()
	if err != nil {
		t.Skipf("KV capability unavailable: %v", err)
	}
	return kv
}

func eventLogOrSkip(t *testing.T, newCaps Factory) statestore.EventLog {
	t.Helper()
	el, err := newCaps(t).EventLog()
	if err != nil {
		t.Skipf("EventLog capability unavailable: %v", err)
	}
	return el
}

func queueOrSkip(t *testing.T, newCaps Factory) statestore.Queue {
	t.Helper()
	q, err := newCaps(t).Queue()
	if err != nil {
		t.Skipf("Queue capability unavailable: %v", err)
	}
	return q
}

func runKV(t *testing.T, newCaps Factory) {
	t.Run("CASMatrix", func(t *testing.T) {
		kv := kvOrSkip(t, newCaps)
		ctx := t.Context()
		// create-only
		require.NoError(t, kv.Set(ctx, confScope, "k", []byte("v0"), statestore.SetOptions{IfVersion: new(int64(0))}))
		require.ErrorIs(t, kv.Set(ctx, confScope, "k", []byte("x"), statestore.SetOptions{IfVersion: new(int64(0))}), statestore.ErrVersionConflict)
		got, err := kv.Get(ctx, confScope, "k")
		require.NoError(t, err)
		require.EqualValues(t, 1, got.Version)
		require.Equal(t, []byte("v0"), got.Data)
		// CAS
		require.NoError(t, kv.Set(ctx, confScope, "k", []byte("v1"), statestore.SetOptions{IfVersion: new(int64(1))}))
		require.ErrorIs(t, kv.Set(ctx, confScope, "k", []byte("v2"), statestore.SetOptions{IfVersion: new(int64(1))}), statestore.ErrVersionConflict)
		// unconditional
		require.NoError(t, kv.Set(ctx, confScope, "k", []byte("v3"), statestore.SetOptions{}))
		got, err = kv.Get(ctx, confScope, "k")
		require.NoError(t, err)
		require.EqualValues(t, 3, got.Version)
		// delete CAS
		require.ErrorIs(t, kv.Delete(ctx, confScope, "k", 99), statestore.ErrVersionConflict)
		require.NoError(t, kv.Delete(ctx, confScope, "k", 3))
		_, err = kv.Get(ctx, confScope, "k")
		require.ErrorIs(t, err, statestore.ErrNotFound)
	})

	t.Run("ListPrefixPaging", func(t *testing.T) {
		kv := kvOrSkip(t, newCaps)
		ctx := t.Context()
		for _, k := range []string{"a1", "a2", "a3", "b1"} {
			require.NoError(t, kv.Set(ctx, confScope, k, []byte("v"), statestore.SetOptions{}))
		}
		p1, err := kv.List(ctx, confScope, "a", statestore.Page{Limit: 2})
		require.NoError(t, err)
		require.Equal(t, []string{"a1", "a2"}, p1.Keys)
		require.Equal(t, "a2", p1.Next)
		p2, err := kv.List(ctx, confScope, "a", statestore.Page{Limit: 2, Token: p1.Next})
		require.NoError(t, err)
		require.Equal(t, []string{"a3"}, p2.Keys)
		require.Empty(t, p2.Next)
	})

	t.Run("ListByteExact", func(t *testing.T) {
		// The prefix match must be case-sensitive and ordering byte-exact, so the
		// SQL drivers match the memory driver regardless of DB locale / LIKE
		// case-folding.
		kv := kvOrSkip(t, newCaps)
		ctx := t.Context()
		for _, k := range []string{"Ax", "aa", "az", "b"} {
			require.NoError(t, kv.Set(ctx, confScope, k, []byte("v"), statestore.SetOptions{}))
		}
		p, err := kv.List(ctx, confScope, "a", statestore.Page{})
		require.NoError(t, err)
		require.Equal(t, []string{"aa", "az"}, p.Keys, "prefix is case-sensitive; 'Ax' excluded")

		all, err := kv.List(ctx, confScope, "", statestore.Page{})
		require.NoError(t, err)
		// ASCII byte order: uppercase 'A' (0x41) sorts before lowercase.
		require.Equal(t, []string{"Ax", "aa", "az", "b"}, all.Keys, "ordering is byte-exact")
	})

	t.Run("ListUnboundedWhenNoLimit", func(t *testing.T) {
		// limit <= 0 returns everything (parity with the memory driver), so a SQL
		// driver's default page cap can never silently truncate.
		kv := kvOrSkip(t, newCaps)
		ctx := t.Context()
		const n = 1200 // more than the old 1000 SQL cap
		for i := range n {
			require.NoError(t, kv.Set(ctx, confScope, fmt.Sprintf("k%05d", i), []byte("v"), statestore.SetOptions{}))
		}
		p, err := kv.List(ctx, confScope, "k", statestore.Page{})
		require.NoError(t, err)
		require.Len(t, p.Keys, n)
		require.Empty(t, p.Next)
	})
}

func runEventLog(t *testing.T, newCaps Factory) {
	t.Run("AppendCASReadTrim", func(t *testing.T) {
		el := eventLogOrSkip(t, newCaps)
		ctx := t.Context()
		head, err := el.Append(ctx, "s", 0, []statestore.Event{{Type: "a"}, {Type: "b"}})
		require.NoError(t, err)
		require.EqualValues(t, 2, head)
		// stale expectedSeq loses
		_, err = el.Append(ctx, "s", 0, []statestore.Event{{Type: "x"}})
		require.ErrorIs(t, err, statestore.ErrVersionConflict)
		evs, err := el.Read(ctx, "s", 0, 10)
		require.NoError(t, err)
		require.Len(t, evs, 2)
		require.EqualValues(t, 1, evs[0].Seq)
		require.EqualValues(t, 2, evs[1].Seq)
		require.NoError(t, el.Trim(ctx, "s", 2))
		evs, err = el.Read(ctx, "s", 0, 10)
		require.NoError(t, err)
		require.Len(t, evs, 1)
		require.EqualValues(t, 2, evs[0].Seq)
	})

	t.Run("E1_ConcurrentAppendOneWinner", func(t *testing.T) {
		el := eventLogOrSkip(t, newCaps)
		ctx := t.Context()
		const writers = 12
		var wins atomic.Int64
		var wg sync.WaitGroup
		for range writers {
			wg.Go(func() {
				if _, err := el.Append(ctx, "race", 0, []statestore.Event{{Type: "e"}}); err == nil {
					wins.Add(1)
				}
			})
		}
		wg.Wait()
		require.EqualValues(t, 1, wins.Load())
	})
}

func runQueue(t *testing.T, newCaps Factory) {
	t.Run("EnqueueLeaseAck", func(t *testing.T) {
		q := queueOrSkip(t, newCaps)
		ctx := t.Context()
		id, err := q.Enqueue(ctx, "cq", statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
		require.NoError(t, err)
		l, err := q.Lease(ctx, "cq", 5, time.Minute)
		require.NoError(t, err)
		require.Len(t, l, 1)
		require.Equal(t, id, l[0].ID)
		require.NoError(t, q.Ack(ctx, l[0].Receipt))
		l, err = q.Lease(ctx, "cq", 5, time.Minute)
		require.NoError(t, err)
		require.Empty(t, l)
	})

	t.Run("DedupCollapse", func(t *testing.T) {
		q := queueOrSkip(t, newCaps)
		ctx := t.Context()
		id1, err := q.Enqueue(ctx, "cq", statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{DedupKey: "d"})
		require.NoError(t, err)
		id2, err := q.Enqueue(ctx, "cq", statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{DedupKey: "d"})
		require.NoError(t, err)
		require.Equal(t, id1, id2)
	})

	t.Run("NackToDeadThenRedrive", func(t *testing.T) {
		q := queueOrSkip(t, newCaps)
		ctx := t.Context()
		id, err := q.Enqueue(ctx, "cq", statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
		require.NoError(t, err)
		// Nack (re-leasing each time) until it dead-letters — attempt budget is
		// driver-configured, so loop with a safety cap instead of assuming a count.
		var dead []statestore.DeadMessage
		for range 20 {
			l, lerr := q.Lease(ctx, "cq", 1, time.Minute)
			require.NoError(t, lerr)
			if len(l) == 0 {
				break
			}
			require.NoError(t, q.Nack(ctx, l[0].Receipt, 0))
			dead, err = q.DeadLetters(ctx, "cq", statestore.Page{})
			require.NoError(t, err)
			if len(dead) > 0 {
				break
			}
		}
		require.Len(t, dead, 1)
		require.Equal(t, id, dead[0].ID)

		require.NoError(t, q.Redrive(ctx, "cq", []string{id}))
		dead, err = q.DeadLetters(ctx, "cq", statestore.Page{})
		require.NoError(t, err)
		require.Empty(t, dead)
		l, err := q.Lease(ctx, "cq", 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, l, 1)
		assert.Equal(t, 1, l[0].Attempts, "attempts reset on redrive")
	})
}

// --- Time-dependent subtests (synctest; in-process drivers only) ---

func runTTLExactOnRead(t *testing.T, newCaps Factory) {
	synctest.Test(t, func(t *testing.T) {
		kv := kvOrSkip(t, newCaps)
		ctx := t.Context()
		require.NoError(t, kv.Set(ctx, confScope, "ttl", []byte("v"), statestore.SetOptions{TTL: time.Hour}))
		time.Sleep(59 * time.Minute)
		_, err := kv.Get(ctx, confScope, "ttl")
		require.NoError(t, err) // still live
		time.Sleep(2 * time.Minute)
		_, err = kv.Get(ctx, confScope, "ttl")
		require.ErrorIs(t, err, statestore.ErrNotFound) // expired, exact on read
	})
}

func runQ2EpochGuard(t *testing.T, newCaps Factory) {
	synctest.Test(t, func(t *testing.T) {
		q := queueOrSkip(t, newCaps)
		ctx := t.Context()
		_, err := q.Enqueue(ctx, "cq", statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
		require.NoError(t, err)
		l1, err := q.Lease(ctx, "cq", 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, l1, 1)
		time.Sleep(2 * time.Minute) // lease expires
		l2, err := q.Lease(ctx, "cq", 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, l2, 1)
		require.ErrorIs(t, q.Ack(ctx, l1[0].Receipt), statestore.ErrInvalidReceipt) // stale epoch rejected
		require.NoError(t, q.Ack(ctx, l2[0].Receipt))                               // current lease decides
	})
}

func runExhaustedByExpiry(t *testing.T, newCaps Factory) {
	synctest.Test(t, func(t *testing.T) {
		q := queueOrSkip(t, newCaps)
		ctx := t.Context()
		id, err := q.Enqueue(ctx, "cq", statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
		require.NoError(t, err)
		// Lease and let the lease expire (never settle) until the message is
		// dead-lettered — the attempt budget is driver-configured, so loop with a
		// safety cap rather than assuming a count.
		var dead []statestore.DeadMessage
		for range 20 {
			l, lerr := q.Lease(ctx, "cq", 1, time.Minute)
			require.NoError(t, lerr)
			if len(l) > 0 {
				time.Sleep(2 * time.Minute) // let the lease expire
			}
			dead, err = q.DeadLetters(ctx, "cq", statestore.Page{})
			require.NoError(t, err)
			if len(dead) > 0 || len(l) == 0 {
				break
			}
		}
		require.Len(t, dead, 1, "a message exhausted by lease expiry must be dead-lettered, not stranded")
		require.Equal(t, id, dead[0].ID)
	})
}

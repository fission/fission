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
	t.Run("Queue/StatsOldestVisibleAge", func(t *testing.T) { runStatsOldestAge(t, newCaps) })
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
		// stale expectedSeq loses — and the conflict must report the current
		// head so a CAS caller can resynchronize (contract: the head is
		// meaningful on ErrVersionConflict). The HTTP client driver in
		// particular must carry it over the wire, not drop it to 0, or a
		// conflicting appender retries at the wrong seq forever.
		conflictHead, err := el.Append(ctx, "s", 0, []statestore.Event{{Type: "x"}})
		require.ErrorIs(t, err, statestore.ErrVersionConflict)
		require.EqualValues(t, 2, conflictHead, "the conflict reports the current head")
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

	t.Run("AppendAnyAndHead", func(t *testing.T) {
		el := eventLogOrSkip(t, newCaps)
		ctx := t.Context()
		// Head on an absent stream is 0 and has no side effects: a CAS append at
		// 0 still succeeds afterwards (the stream was not created by the read).
		head, err := el.Head(ctx, "anyhead")
		require.NoError(t, err)
		require.Zero(t, head)
		head, err = el.Append(ctx, "anyhead", 0, []statestore.Event{{Type: "a"}, {Type: "b"}})
		require.NoError(t, err)
		require.EqualValues(t, 2, head)
		head, err = el.Head(ctx, "anyhead")
		require.NoError(t, err)
		require.EqualValues(t, 2, head, "Head reflects the CAS append")

		// AppendAny appends at the current head with no CAS: no conflict even
		// though the caller never read the head.
		head, err = el.Append(ctx, "anyhead", statestore.AppendAny, []statestore.Event{{Type: "c"}})
		require.NoError(t, err)
		require.EqualValues(t, 3, head)

		// The two modes interoperate: a CAS caller resynchronizes via Head and
		// appends at the post-AppendAny head.
		head, err = el.Head(ctx, "anyhead")
		require.NoError(t, err)
		require.EqualValues(t, 3, head)
		head, err = el.Append(ctx, "anyhead", 3, []statestore.Event{{Type: "d"}})
		require.NoError(t, err)
		require.EqualValues(t, 4, head)

		// The log is gapless and ordered across both modes.
		evs, err := el.Read(ctx, "anyhead", 0, 0)
		require.NoError(t, err)
		require.Len(t, evs, 4)
		for i, ev := range evs {
			require.EqualValues(t, i+1, ev.Seq)
		}
		require.Equal(t, "c", evs[2].Type)

		// AppendAny on an absent stream creates it at seq 1.
		head, err = el.Append(ctx, "anyfresh", statestore.AppendAny, []statestore.Event{{Type: "x"}})
		require.NoError(t, err)
		require.EqualValues(t, 1, head)
	})

	t.Run("CASVersusAppendAnyRace", func(t *testing.T) {
		el := eventLogOrSkip(t, newCaps)
		ctx := t.Context()
		// CAS callers keep their interleave protection while AppendAny writers
		// race them: every AppendAny lands; a CAS at a stale head conflicts. At
		// most one stale-head CAS can win (only by executing first), and the
		// final head accounts for exactly the successful writes — gapless.
		const anyWriters = 8
		const casWriters = 4
		var casWins, anyFailures atomic.Int64
		// Collected in the workers, asserted after Wait: testify must not run on
		// non-test goroutines (require.FailNow would Goexit the wrong goroutine).
		casErrs := make(chan error, casWriters)
		var wg sync.WaitGroup
		for range anyWriters {
			wg.Go(func() {
				if _, err := el.Append(ctx, "mixedrace", statestore.AppendAny, []statestore.Event{{Type: "any"}}); err != nil {
					anyFailures.Add(1)
				}
			})
		}
		for range casWriters {
			wg.Go(func() {
				if _, err := el.Append(ctx, "mixedrace", 0, []statestore.Event{{Type: "cas"}}); err == nil {
					casWins.Add(1)
				} else {
					casErrs <- err
				}
			})
		}
		wg.Wait()
		close(casErrs)
		for err := range casErrs {
			require.ErrorIs(t, err, statestore.ErrVersionConflict, "a losing CAS gets the conflict sentinel, not a raw error")
		}
		require.Zero(t, anyFailures.Load(), "AppendAny never conflicts, even racing CAS writers")
		require.LessOrEqual(t, casWins.Load(), int64(1), "at most one CAS-at-0 can win")

		want := int64(anyWriters) + casWins.Load()
		head, err := el.Head(ctx, "mixedrace")
		require.NoError(t, err)
		require.Equal(t, want, head)
		evs, err := el.Read(ctx, "mixedrace", 0, 0)
		require.NoError(t, err)
		require.Len(t, evs, int(want), "the log holds exactly the successful writes, gapless")
	})

	t.Run("ConcurrentAppendAnyAllWin", func(t *testing.T) {
		el := eventLogOrSkip(t, newCaps)
		ctx := t.Context()
		// Independent topic publishers must all land without a retry loop
		// (RFC-0027): every AppendAny wins, seqs are unique and gapless.
		const writers = 12
		var failures atomic.Int64
		var wg sync.WaitGroup
		for range writers {
			wg.Go(func() {
				if _, err := el.Append(ctx, "anyrace", statestore.AppendAny, []statestore.Event{{Type: "e"}}); err != nil {
					failures.Add(1)
				}
			})
		}
		wg.Wait()
		require.Zero(t, failures.Load(), "AppendAny never conflicts")

		head, err := el.Head(ctx, "anyrace")
		require.NoError(t, err)
		require.EqualValues(t, writers, head)
		evs, err := el.Read(ctx, "anyrace", 0, 0)
		require.NoError(t, err)
		require.Len(t, evs, writers)
		seen := map[int64]bool{}
		for _, ev := range evs {
			require.False(t, seen[ev.Seq], "duplicate seq %d", ev.Seq)
			seen[ev.Seq] = true
			require.True(t, ev.Seq >= 1 && ev.Seq <= writers, "seq %d out of the gapless range", ev.Seq)
		}
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

		n, err := q.Redrive(ctx, "cq", []string{id})
		require.NoError(t, err)
		assert.EqualValues(t, 1, n, "Redrive reports the count actually re-enqueued")
		// A second redrive of the same (now-requeued, not dead) id re-enqueues nothing.
		n, err = q.Redrive(ctx, "cq", []string{id})
		require.NoError(t, err)
		assert.Zero(t, n, "an id that is not dead-lettered is not redriven")
		dead, err = q.DeadLetters(ctx, "cq", statestore.Page{})
		require.NoError(t, err)
		require.Empty(t, dead)
		l, err := q.Lease(ctx, "cq", 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, l, 1)
		assert.Equal(t, 1, l[0].Attempts, "attempts reset on redrive")
	})

	t.Run("PurgeDeadLetters", func(t *testing.T) {
		q := queueOrSkip(t, newCaps)
		ctx := t.Context()
		const pq = "purgeq"
		// Kill two to the dead set, leave one visible (Purge must touch only the dead).
		for range 2 {
			_, err := q.Enqueue(ctx, pq, statestore.Message{Body: []byte("d")}, statestore.EnqueueOptions{})
			require.NoError(t, err)
			l, err := q.Lease(ctx, pq, 1, time.Minute)
			require.NoError(t, err)
			require.Len(t, l, 1)
			require.NoError(t, q.Kill(ctx, l[0].Receipt, "permanent"))
		}
		liveID, err := q.Enqueue(ctx, pq, statestore.Message{Body: []byte("live")}, statestore.EnqueueOptions{})
		require.NoError(t, err)

		dead, err := q.DeadLetters(ctx, pq, statestore.Page{})
		require.NoError(t, err)
		require.Len(t, dead, 2)

		n, err := q.Purge(ctx, pq)
		require.NoError(t, err)
		assert.EqualValues(t, 2, n, "Purge returns the count removed")

		dead, err = q.DeadLetters(ctx, pq, statestore.Page{})
		require.NoError(t, err)
		assert.Empty(t, dead, "dead set is empty after Purge")
		st, err := q.Stats(ctx, pq)
		require.NoError(t, err)
		assert.Zero(t, st.Dead)
		assert.EqualValues(t, 1, st.Visible, "Purge leaves visible work untouched")

		// The still-visible message is leasable — the purge did not disturb it.
		l, err := q.Lease(ctx, pq, 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, l, 1)
		assert.Equal(t, liveID, l[0].ID)

		// Purging an empty dead set is a no-op that removes nothing.
		n, err = q.Purge(ctx, pq)
		require.NoError(t, err)
		assert.Zero(t, n)
	})

	t.Run("Stats", func(t *testing.T) {
		q := queueOrSkip(t, newCaps)
		ctx := t.Context()
		const sq = "statsq"
		// Unknown queue → zero snapshot (no side effect that creates it).
		st, err := q.Stats(ctx, sq)
		require.NoError(t, err)
		require.Equal(t, statestore.QueueStats{}, st)
		// Enqueue three: all visible.
		for range 3 {
			_, eerr := q.Enqueue(ctx, sq, statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
			require.NoError(t, eerr)
		}
		st, err = q.Stats(ctx, sq)
		require.NoError(t, err)
		assert.EqualValues(t, 3, st.Visible)
		assert.Zero(t, st.Leased)
		assert.Zero(t, st.Dead)
		assert.GreaterOrEqual(t, st.OldestVisibleAge, time.Duration(0))
		// Lease two: visible drops, leased rises (acked/leased are disjoint states).
		l, err := q.Lease(ctx, sq, 2, time.Minute)
		require.NoError(t, err)
		require.Len(t, l, 2)
		st, err = q.Stats(ctx, sq)
		require.NoError(t, err)
		assert.EqualValues(t, 1, st.Visible)
		assert.EqualValues(t, 2, st.Leased)
		// Ack one: acked is terminal, counted in neither visible nor leased nor dead.
		require.NoError(t, q.Ack(ctx, l[0].Receipt))
		st, err = q.Stats(ctx, sq)
		require.NoError(t, err)
		assert.EqualValues(t, 1, st.Visible)
		assert.EqualValues(t, 1, st.Leased)
		assert.Zero(t, st.Dead)
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

func runStatsOldestAge(t *testing.T, newCaps Factory) {
	// OldestVisibleAge is time-dependent, so it is checked under virtual time where
	// the elapsed duration is exact rather than a jittery wall-clock delta.
	synctest.Test(t, func(t *testing.T) {
		q := queueOrSkip(t, newCaps)
		ctx := t.Context()
		_, err := q.Enqueue(ctx, "cq", statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
		require.NoError(t, err)
		time.Sleep(5 * time.Minute)
		st, err := q.Stats(ctx, "cq")
		require.NoError(t, err)
		require.EqualValues(t, 1, st.Visible)
		require.Equal(t, 5*time.Minute, st.OldestVisibleAge)
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

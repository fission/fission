// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
)

// sweeperTestEntry returns an AgentEntry with short, distinct lifecycle
// windows so IdleAfter and ArchiveAfter thresholds are easy to straddle in a
// synctest bubble.
func sweeperTestEntry(ns, agent string) AgentEntry {
	return AgentEntry{
		Namespace:    ns,
		Name:         agent,
		IdleAfter:    5 * time.Minute,
		ArchiveAfter: time.Hour,
		MaxSessions:  0,
	}
}

func TestSweeperActiveToIdleAfterIdleAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		entry := sweeperTestEntry("ns", "agent1")
		view.Upsert(entry)

		ctx := t.Context()
		rec := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusActive, LastActiveAt: time.Now()}
		require.NoError(t, store.Create(ctx, rec, 0, time.Hour))

		sweeper := NewSweeper(logr.Discard(), view, store, time.Second, 7*24*time.Hour, time.Now)

		// Not yet due: less than IdleAfter has elapsed.
		time.Sleep(entry.IdleAfter - time.Second)
		sweeper.sweepOnce(ctx)
		got, _, err := store.Get(ctx, "ns", "agent1", "s1")
		require.NoError(t, err)
		assert.Equal(t, StatusActive, got.Status, "must not idle before IdleAfter elapses")

		// Now past IdleAfter.
		time.Sleep(2 * time.Second)
		sweeper.sweepOnce(ctx)
		got, _, err = store.Get(ctx, "ns", "agent1", "s1")
		require.NoError(t, err)
		assert.Equal(t, StatusIdle, got.Status, "must idle once IdleAfter elapses")
	})
}

func TestSweeperIdleToArchivedAfterArchiveAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		entry := sweeperTestEntry("ns", "agent1")
		view.Upsert(entry)

		ctx := t.Context()
		retention := 7 * 24 * time.Hour
		liveTTL := entry.IdleAfter + entry.ArchiveAfter + retention
		rec := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusIdle, LastActiveAt: time.Now()}
		require.NoError(t, store.Create(ctx, rec, 0, liveTTL))

		sweeper := NewSweeper(logr.Discard(), view, store, time.Second, retention, time.Now)

		// Not yet due.
		time.Sleep(entry.ArchiveAfter - time.Second)
		sweeper.sweepOnce(ctx)
		list, _, err := store.List(ctx, "ns", "agent1", "", 10)
		require.NoError(t, err)
		require.Len(t, list, 1, "must not archive before ArchiveAfter elapses")

		// Now past ArchiveAfter.
		time.Sleep(2 * time.Second)
		sweeper.sweepOnce(ctx)
		list, _, err = store.List(ctx, "ns", "agent1", "", 10)
		require.NoError(t, err)
		assert.Empty(t, list, "archived record must leave the live List")

		// Present in the archived keyspace, before its retention TTL.
		v, err := kv.Get(ctx, archivedScope("ns", "agent1"), "s1")
		require.NoError(t, err)
		assert.NotEmpty(t, v.Data)

		// Gone after retention.
		time.Sleep(retention + time.Second)
		_, err = kv.Get(ctx, archivedScope("ns", "agent1"), "s1")
		require.ErrorIs(t, err, statestore.ErrNotFound, "archived copy must expire after retention")
	})
}

// TestSweeperIdleTransitionSkipsOnCASConflict drives the sweeper's beforeCAS
// test seam to land a write in the window between the sweeper's re-Get and
// its Update call: another writer (standing in for a second replica) bumps
// the record's version first. The sweeper's own CAS must then fail with
// ErrVersionConflict and be skipped silently, without touching the record
// the other writer just wrote.
func TestSweeperIdleTransitionSkipsOnCASConflict(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		entry := sweeperTestEntry("ns", "agent1")
		view.Upsert(entry)

		ctx := t.Context()
		rec := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusActive, LastActiveAt: time.Now()}
		require.NoError(t, store.Create(ctx, rec, 0, time.Hour))
		time.Sleep(entry.IdleAfter + time.Second)

		sweeper := NewSweeper(logr.Discard(), view, store, time.Second, 7*24*time.Hour, time.Now)
		sweeper.beforeCAS = func() {
			// Simulate a second replica winning the race: it idles the
			// record first, landing between this sweeper's Get and Update.
			winner, version, err := store.Get(ctx, "ns", "agent1", "s1")
			require.NoError(t, err)
			winner.Status = StatusIdle
			winner.CurrentPod = "winner-replica"
			require.NoError(t, store.Update(ctx, winner, version, time.Hour))
		}

		sweeper.sweepOnce(ctx)

		got, _, err := store.Get(ctx, "ns", "agent1", "s1")
		require.NoError(t, err)
		assert.Equal(t, StatusIdle, got.Status)
		assert.Equal(t, "winner-replica", got.CurrentPod, "the sweeper's own CAS must have been skipped, not overwritten the winner's write")
	})
}

// TestSweeperArchiveSkipsOnReviveRace covers the specific race carried from
// Task 9's review: a turn revives (re-activates) a session between the
// sweeper's re-Get and its Archive call. SessionStore.Archive's CAS-delete
// then conflicts and aborts, undoing its speculative archived-copy write.
// The sweeper must treat that as a silent skip, and the live record must
// survive with the reviver's data intact — never archived.
func TestSweeperArchiveSkipsOnReviveRace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		entry := sweeperTestEntry("ns", "agent1")
		view.Upsert(entry)

		ctx := t.Context()
		retention := 7 * 24 * time.Hour
		liveTTL := entry.IdleAfter + entry.ArchiveAfter + retention
		rec := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusIdle, LastActiveAt: time.Now()}
		require.NoError(t, store.Create(ctx, rec, 0, liveTTL))
		time.Sleep(entry.ArchiveAfter + time.Second)

		sweeper := NewSweeper(logr.Discard(), view, store, time.Second, retention, time.Now)
		var revivedAt time.Time
		sweeper.beforeCAS = func() {
			// A live turn revives the session, landing between this
			// sweeper's Get and Archive.
			live, version, err := store.Get(ctx, "ns", "agent1", "s1")
			require.NoError(t, err)
			revivedAt = time.Now()
			live.Status = StatusActive
			live.LastActiveAt = revivedAt
			live.Stats.Turns = 1
			require.NoError(t, store.Update(ctx, live, version, time.Hour))
		}

		sweeper.sweepOnce(ctx)

		got, _, err := store.Get(ctx, "ns", "agent1", "s1")
		require.NoError(t, err, "the revived record must still be live, not archived")
		assert.Equal(t, StatusActive, got.Status)
		assert.True(t, revivedAt.Equal(got.LastActiveAt))
		assert.EqualValues(t, 1, got.Stats.Turns, "the reviver's data must survive intact")

		_, err = kv.Get(ctx, archivedScope("ns", "agent1"), "s1")
		assert.ErrorIs(t, err, statestore.ErrNotFound, "Archive's speculative archived-copy write must have been undone")
	})
}

// TestSweeperArchiveSkipsOnListToGetStalenessWindow covers the other race
// window: a turn revives the record between the sweeper's List page
// snapshot and its own re-Get (not between the Get and the CAS write, which
// TestSweeperArchiveSkipsOnReviveRace already covers). The Get itself would
// observe the revived, active record — no CAS conflict occurs at all — so
// only a re-validation of the fresh read (not just the CAS) can catch this.
// It drives sweepRecord directly with a hand-built stale snapshot, per the
// brief's "driving sweepOnce directly with a pre-staleness setup".
func TestSweeperArchiveSkipsOnListToGetStalenessWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		entry := sweeperTestEntry("ns", "agent1")
		view.Upsert(entry)

		ctx := t.Context()
		retention := 7 * 24 * time.Hour
		liveTTL := entry.IdleAfter + entry.ArchiveAfter + retention
		longAgo := time.Now()
		staleSnapshot := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusIdle, LastActiveAt: longAgo}
		require.NoError(t, store.Create(ctx, staleSnapshot, 0, liveTTL))

		time.Sleep(entry.ArchiveAfter + time.Second)

		// A turn revives the session after the (simulated) List page was
		// captured, but before the sweeper processes this record.
		_, version, err := store.Get(ctx, "ns", "agent1", "s1")
		require.NoError(t, err)
		revivedAt := time.Now()
		revived := staleSnapshot
		revived.Status = StatusActive
		revived.LastActiveAt = revivedAt
		revived.Stats.Turns = 1
		require.NoError(t, store.Update(ctx, revived, version, time.Hour))

		sweeper := NewSweeper(logr.Discard(), view, store, time.Second, retention, time.Now)
		// staleSnapshot is what the sweeper's List call would have returned
		// before the revive landed; sweepRecord must re-validate against a
		// fresh Get rather than trusting it.
		sweeper.sweepRecord(ctx, entry, staleSnapshot, liveTTL)

		got, _, err := store.Get(ctx, "ns", "agent1", "s1")
		require.NoError(t, err, "the revived record must still be live, not archived")
		assert.Equal(t, StatusActive, got.Status)
		assert.True(t, revivedAt.Equal(got.LastActiveAt))
		assert.EqualValues(t, 1, got.Stats.Turns, "the reviver's data must survive intact")

		_, err = kv.Get(ctx, archivedScope("ns", "agent1"), "s1")
		assert.ErrorIs(t, err, statestore.ErrNotFound, "must never have written a speculative archived copy")
	})
}

func TestSweeperRunRespectsContextCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		sweeper := NewSweeper(logr.Discard(), view, store, 10*time.Millisecond, time.Hour, time.Now)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			sweeper.Run(ctx)
			close(done)
		}()

		// Let a few ticks fire (no-op: the view is empty), then cancel.
		time.Sleep(50 * time.Millisecond)
		cancel()

		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("Run did not return promptly after ctx cancel")
		}
	})
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"context"
	"encoding/json/v2"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statesvc/stateapi"
)

// sweeperTestEntry returns an AgentEntry with short, distinct lifecycle
// windows so IdleAfter and ArchiveAfter thresholds are easy to straddle in a
// synctest bubble. StateKeyspace is left empty (history disabled).
func sweeperTestEntry(ns, agent string) AgentEntry {
	return AgentEntry{
		Namespace:    ns,
		Name:         agent,
		IdleAfter:    5 * time.Minute,
		ArchiveAfter: time.Hour,
		MaxSessions:  0,
	}
}

// sweeperTestEntryWithHistory is sweeperTestEntry with a state keyspace (and
// optionally the HistoryTrim knob) turned on, for the history-lifecycle
// tests.
func sweeperTestEntryWithHistory(ns, agent, keyspace string, historyTrim bool) AgentEntry {
	e := sweeperTestEntry(ns, agent)
	e.StateKeyspace = keyspace
	e.HistoryTrim = historyTrim
	return e
}

// appendHistoryEvents appends n events to sessionID's history stream and
// returns the resulting head sequence.
func appendHistoryEvents(t *testing.T, ctx context.Context, el statestore.EventLog, ns, keyspace, sessionID string, n int) int64 {
	t.Helper()
	stream := stateapi.StreamName(ns, keyspace, HistoryClientStream(sessionID))
	events := make([]statestore.Event, n)
	for i := range events {
		events[i] = statestore.Event{Type: "e"}
	}
	head, err := el.Append(ctx, stream, statestore.AppendAny, events)
	require.NoError(t, err)
	return head
}

// setTestCheckpoint writes a checkpoint record with the given coverage
// boundary directly through kv, exactly as the SDK's checkpoint write would
// land (HistoryStore has no write path of its own).
func setTestCheckpoint(t *testing.T, ctx context.Context, kv statestore.KVStore, ns, keyspace, sessionID string, coveredThroughSeq int64) {
	t.Helper()
	data, err := json.Marshal(CheckpointRecord{V: 1, CoveredThroughSeq: coveredThroughSeq, Kind: "summary"})
	require.NoError(t, err)
	require.NoError(t, kv.Set(ctx, stateScope(ns, keyspace), CheckpointKeyPrefix+sessionID, data, statestore.SetOptions{}))
}

// countingEventLog wraps a statestore.EventLog and counts Head/Trim calls, so
// a test can assert the sweeper never touched history for a no-keyspace
// agent.
type countingEventLog struct {
	statestore.EventLog
	headCalls, trimCalls int
}

func (w *countingEventLog) Head(ctx context.Context, stream string) (int64, error) {
	w.headCalls++
	return w.EventLog.Head(ctx, stream)
}

func (w *countingEventLog) Trim(ctx context.Context, stream string, belowSeq int64) error {
	w.trimCalls++
	return w.EventLog.Trim(ctx, stream, belowSeq)
}

// countingKV wraps a statestore.KVStore and counts Get/Delete calls (the only
// operations HistoryStore's checkpoint methods issue), for the same
// no-keyspace assertion.
type countingKV struct {
	statestore.KVStore
	getCalls, deleteCalls int
}

func (w *countingKV) Get(ctx context.Context, s statestore.Scope, key string) (statestore.Value, error) {
	w.getCalls++
	return w.KVStore.Get(ctx, s, key)
}

func (w *countingKV) Delete(ctx context.Context, s statestore.Scope, key string, ifVersion int64) error {
	w.deleteCalls++
	return w.KVStore.Delete(ctx, s, key, ifVersion)
}

// headInjectingEventLog wraps a statestore.EventLog so its Head call can, as
// a side effect, run onHead after computing the head it returns — used to
// simulate a new event landing in the exact window between the sweeper's
// Head capture and its later Trim call, without needing a production-only
// test seam at that point.
type headInjectingEventLog struct {
	statestore.EventLog
	onHead func()
}

func (w *headInjectingEventLog) Head(ctx context.Context, stream string) (int64, error) {
	head, err := w.EventLog.Head(ctx, stream)
	if err == nil && w.onHead != nil {
		w.onHead()
	}
	return head, err
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

		sweeper := NewSweeper(logr.Discard(), view, store, nil, time.Second, 7*24*time.Hour, time.Now)

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

		sweeper := NewSweeper(logr.Discard(), view, store, nil, time.Second, retention, time.Now)

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

		sweeper := NewSweeper(logr.Discard(), view, store, nil, time.Second, 7*24*time.Hour, time.Now)
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
// A turn revives (re-activates) a session between the
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

		sweeper := NewSweeper(logr.Discard(), view, store, nil, time.Second, retention, time.Now)
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

		sweeper := NewSweeper(logr.Discard(), view, store, nil, time.Second, retention, time.Now)
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

// TestSweeperIdleSkipsOnListToGetStalenessWindow is the idle-side analogue
// of TestSweeperArchiveSkipsOnListToGetStalenessWindow: a new turn touches
// an active session (refreshing LastActiveAt) between the sweeper's List
// page snapshot and its own re-Get. The Get observes the fresh, still-active
// record — no CAS conflict occurs — so only a re-validation of the fresh
// read (not just the CAS) prevents the sweeper from idling a session a turn
// just touched.
func TestSweeperIdleSkipsOnListToGetStalenessWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		entry := sweeperTestEntry("ns", "agent1")
		view.Upsert(entry)

		ctx := t.Context()
		longAgo := time.Now()
		staleSnapshot := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusActive, LastActiveAt: longAgo}
		require.NoError(t, store.Create(ctx, staleSnapshot, 0, time.Hour))

		time.Sleep(entry.IdleAfter + time.Second)

		// A new turn touches the session after the (simulated) List page
		// was captured, but before the sweeper processes this record.
		_, version, err := store.Get(ctx, "ns", "agent1", "s1")
		require.NoError(t, err)
		touchedAt := time.Now()
		touched := staleSnapshot
		touched.LastActiveAt = touchedAt
		touched.Stats.Turns = 1
		require.NoError(t, store.Update(ctx, touched, version, time.Hour))

		sweeper := NewSweeper(logr.Discard(), view, store, nil, time.Second, 7*24*time.Hour, time.Now)
		// staleSnapshot is what the sweeper's List call would have returned
		// before the turn landed; sweepRecord must re-validate against a
		// fresh Get rather than trusting it.
		sweeper.sweepRecord(ctx, entry, staleSnapshot, time.Hour)

		got, _, err := store.Get(ctx, "ns", "agent1", "s1")
		require.NoError(t, err)
		assert.Equal(t, StatusActive, got.Status, "must not idle a session a turn just touched")
		assert.True(t, touchedAt.Equal(got.LastActiveAt))
		assert.EqualValues(t, 1, got.Stats.Turns, "the turn's data must survive intact")
	})
}

func TestSweeperRunRespectsContextCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		sweeper := NewSweeper(logr.Discard(), view, store, nil, 10*time.Millisecond, time.Hour, time.Now)

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

// TestSweeperArchiveTrimsHistoryToPreArchiveHeadAndDeletesCheckpoint pins the
// archive-time history cleanup: once store.Archive commits, the sweeper trims
// history below headAtDecision+1 (dropping every event that existed at the
// moment it decided to archive) and deletes the checkpoint.
func TestSweeperArchiveTrimsHistoryToPreArchiveHeadAndDeletesCheckpoint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		entry := sweeperTestEntryWithHistory("ns", "agent1", "ks1", false)
		view.Upsert(entry)

		ctx := t.Context()
		retention := 7 * 24 * time.Hour
		liveTTL := entry.IdleAfter + entry.ArchiveAfter + retention
		rec := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusIdle, LastActiveAt: time.Now()}
		require.NoError(t, store.Create(ctx, rec, 0, liveTTL))

		el, hkv := newTestStores(t)
		appendHistoryEvents(t, ctx, el, "ns", "ks1", "s1", 4)
		setTestCheckpoint(t, ctx, hkv, "ns", "ks1", "s1", 4)
		hist := NewHistoryStore(el, hkv)

		sweeper := NewSweeper(logr.Discard(), view, store, hist, time.Second, retention, time.Now)
		time.Sleep(entry.ArchiveAfter + time.Second)
		sweeper.sweepOnce(ctx)

		events, err := hist.Read(ctx, "ns", "ks1", "s1", 0, 100)
		require.NoError(t, err)
		assert.Empty(t, events, "every event that existed at the archive decision must be trimmed")

		_, err = hist.GetCheckpoint(ctx, "ns", "ks1", "s1")
		assert.ErrorIs(t, err, statestore.ErrNotFound, "checkpoint must be deleted once the archive commits")
	})
}

// TestSweeperArchiveTrimSurvivesLateAppendedEvents pins constraint 3: the
// trim bound is fixed at the PRE-archive head, never a re-read current head.
// An event landing in the window between the sweeper's Head capture and its
// later Trim call (headInjectingEventLog simulates this without a
// production-only seam) must survive.
func TestSweeperArchiveTrimSurvivesLateAppendedEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		entry := sweeperTestEntryWithHistory("ns", "agent1", "ks1", false)
		view.Upsert(entry)

		ctx := t.Context()
		retention := 7 * 24 * time.Hour
		liveTTL := entry.IdleAfter + entry.ArchiveAfter + retention
		rec := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusIdle, LastActiveAt: time.Now()}
		require.NoError(t, store.Create(ctx, rec, 0, liveTTL))

		el, hkv := newTestStores(t)
		appendHistoryEvents(t, ctx, el, "ns", "ks1", "s1", 4)
		wrapped := &headInjectingEventLog{EventLog: el}
		wrapped.onHead = func() {
			// A same-id session re-created (or still being turned) in the
			// window after the sweeper captured its decision head: this
			// event must survive the later trim, which is bounded by the
			// head captured before this ran.
			appendHistoryEvents(t, ctx, el, "ns", "ks1", "s1", 1)
		}
		hist := NewHistoryStore(wrapped, hkv)

		sweeper := NewSweeper(logr.Discard(), view, store, hist, time.Second, retention, time.Now)
		time.Sleep(entry.ArchiveAfter + time.Second)
		sweeper.sweepOnce(ctx)

		events, err := hist.Read(ctx, "ns", "ks1", "s1", 0, 100)
		require.NoError(t, err)
		require.Len(t, events, 1, "the late-appended event must survive the pre-archive-head-bounded trim")
		assert.EqualValues(t, 5, events[0].Seq)
	})
}

// TestSweeperArchiveCASConflictLeavesHistoryUntouched pins constraint 2: an
// Archive CAS conflict (a concurrent turn revives the session) must leave
// history and the checkpoint completely untouched.
func TestSweeperArchiveCASConflictLeavesHistoryUntouched(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		entry := sweeperTestEntryWithHistory("ns", "agent1", "ks1", false)
		view.Upsert(entry)

		ctx := t.Context()
		retention := 7 * 24 * time.Hour
		liveTTL := entry.IdleAfter + entry.ArchiveAfter + retention
		rec := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusIdle, LastActiveAt: time.Now()}
		require.NoError(t, store.Create(ctx, rec, 0, liveTTL))

		el, hkv := newTestStores(t)
		appendHistoryEvents(t, ctx, el, "ns", "ks1", "s1", 4)
		setTestCheckpoint(t, ctx, hkv, "ns", "ks1", "s1", 4)
		hist := NewHistoryStore(el, hkv)

		time.Sleep(entry.ArchiveAfter + time.Second)

		sweeper := NewSweeper(logr.Discard(), view, store, hist, time.Second, retention, time.Now)
		sweeper.beforeCAS = func() {
			// A live turn revives the session, landing between the
			// sweeper's Get and its Archive CAS.
			live, version, err := store.Get(ctx, "ns", "agent1", "s1")
			require.NoError(t, err)
			live.Status = StatusActive
			live.LastActiveAt = time.Now()
			require.NoError(t, store.Update(ctx, live, version, time.Hour))
		}

		sweeper.sweepOnce(ctx)

		got, _, err := store.Get(ctx, "ns", "agent1", "s1")
		require.NoError(t, err)
		assert.Equal(t, StatusActive, got.Status, "the revived record must still be live, not archived")

		events, err := hist.Read(ctx, "ns", "ks1", "s1", 0, 100)
		require.NoError(t, err)
		assert.Len(t, events, 4, "history must be untouched when the archive CAS conflicts")

		_, err = hist.GetCheckpoint(ctx, "ns", "ks1", "s1")
		assert.NoError(t, err, "checkpoint must be untouched when the archive CAS conflicts")
	})
}

// TestSweeperKnobTrimsLiveSessionToCheckpointBoundary pins the
// HistoryTrim-knob path: a live (not-yet-idle) session on an agent with
// HistoryTrim=true is trimmed, on every sweep visit, below its checkpoint's
// CoveredThroughSeq+1.
func TestSweeperKnobTrimsLiveSessionToCheckpointBoundary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		entry := sweeperTestEntryWithHistory("ns", "agent1", "ks1", true)
		view.Upsert(entry)

		ctx := t.Context()
		rec := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusActive, LastActiveAt: time.Now()}
		require.NoError(t, store.Create(ctx, rec, 0, time.Hour))

		el, hkv := newTestStores(t)
		appendHistoryEvents(t, ctx, el, "ns", "ks1", "s1", 5)
		setTestCheckpoint(t, ctx, hkv, "ns", "ks1", "s1", 3)
		hist := NewHistoryStore(el, hkv)

		sweeper := NewSweeper(logr.Discard(), view, store, hist, time.Second, 7*24*time.Hour, time.Now)
		sweeper.sweepOnce(ctx)

		events, err := hist.Read(ctx, "ns", "ks1", "s1", 0, 100)
		require.NoError(t, err)
		require.Len(t, events, 2, "events below the checkpoint boundary must be trimmed")
		assert.EqualValues(t, 4, events[0].Seq)
		assert.EqualValues(t, 5, events[1].Seq)

		got, _, err := store.Get(ctx, "ns", "agent1", "s1")
		require.NoError(t, err)
		assert.Equal(t, StatusActive, got.Status, "the knob trim must not itself change session status")
	})
}

// TestSweeperKnobFalseLeavesLiveSessionHistoryUntouched is the negative
// counterpart: with HistoryTrim=false, the sweeper never trims a live
// session's history even when a checkpoint exists.
func TestSweeperKnobFalseLeavesLiveSessionHistoryUntouched(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		entry := sweeperTestEntryWithHistory("ns", "agent1", "ks1", false)
		view.Upsert(entry)

		ctx := t.Context()
		rec := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusActive, LastActiveAt: time.Now()}
		require.NoError(t, store.Create(ctx, rec, 0, time.Hour))

		el, hkv := newTestStores(t)
		appendHistoryEvents(t, ctx, el, "ns", "ks1", "s1", 5)
		setTestCheckpoint(t, ctx, hkv, "ns", "ks1", "s1", 3)
		hist := NewHistoryStore(el, hkv)

		sweeper := NewSweeper(logr.Discard(), view, store, hist, time.Second, 7*24*time.Hour, time.Now)
		sweeper.sweepOnce(ctx)

		events, err := hist.Read(ctx, "ns", "ks1", "s1", 0, 100)
		require.NoError(t, err)
		assert.Len(t, events, 5, "with HistoryTrim=false the sweeper must never trim live history")
	})
}

// TestSweeperNoKeyspaceAgentNeverTouchesHistoryStore pins constraint 6: an
// agent with StateKeyspace == "" (history disabled) must never cause the
// sweeper to call into the HistoryStore at all, through any lifecycle
// transition.
func TestSweeperNoKeyspaceAgentNeverTouchesHistoryStore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		view := NewAgentView()
		entry := sweeperTestEntry("ns", "agent1") // StateKeyspace == "", HistoryTrim == false
		view.Upsert(entry)

		ctx := t.Context()
		retention := 7 * 24 * time.Hour
		liveTTL := entry.IdleAfter + entry.ArchiveAfter + retention
		rec := SessionRecord{ID: "s1", Agent: "agent1", Namespace: "ns", Status: StatusIdle, LastActiveAt: time.Now()}
		require.NoError(t, store.Create(ctx, rec, 0, liveTTL))

		el, hkv := newTestStores(t)
		countedEl := &countingEventLog{EventLog: el}
		countedKV := &countingKV{KVStore: hkv}
		hist := NewHistoryStore(countedEl, countedKV)

		sweeper := NewSweeper(logr.Discard(), view, store, hist, time.Second, retention, time.Now)
		// Drive it past archive due, so the only reason history stays
		// untouched is the StateKeyspace=="" guard, not "never reached the
		// archive path".
		time.Sleep(entry.ArchiveAfter + time.Second)
		sweeper.sweepOnce(ctx)

		assert.Zero(t, countedEl.headCalls, "Head must never be called for a no-keyspace agent")
		assert.Zero(t, countedEl.trimCalls, "Trim must never be called for a no-keyspace agent")
		assert.Zero(t, countedKV.getCalls, "checkpoint Get must never be called for a no-keyspace agent")
		assert.Zero(t, countedKV.deleteCalls, "checkpoint Delete must never be called for a no-keyspace agent")

		list, _, err := store.List(ctx, "ns", "agent1", "", 10)
		require.NoError(t, err)
		assert.Empty(t, list, "the archive transition itself must still have happened")
	})
}

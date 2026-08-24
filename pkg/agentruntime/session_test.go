// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
	_ "github.com/fission/fission/pkg/statestore/memory"
)

// newTestKV opens a fresh in-memory statestore KVStore for one test.
func newTestKV(t *testing.T) statestore.KVStore {
	t.Helper()
	caps, err := statestore.Open(t.Context(), statestore.Config{Driver: "memory"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })
	kv, err := caps.KV()
	require.NoError(t, err)
	return kv
}

func fixedClock(ts time.Time) func() time.Time {
	return func() time.Time { return ts }
}

func TestSessionStoreCreateGetRoundtrip(t *testing.T) {
	t.Parallel()
	kv := newTestKV(t)
	ts := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	store := NewSessionStore(kv, fixedClock(ts))
	ctx := t.Context()

	rec := SessionRecord{
		ID:           "sess-1",
		Agent:        "myagent",
		Namespace:    "ns1",
		Status:       StatusActive,
		CreatedAt:    ts,
		LastActiveAt: ts,
	}
	require.NoError(t, store.Create(ctx, rec, 0, time.Hour))

	got, version, err := store.Get(ctx, "ns1", "myagent", "sess-1")
	require.NoError(t, err)
	assert.EqualValues(t, 1, version)
	assert.Equal(t, StatusActive, got.Status)
	assert.True(t, ts.Equal(got.CreatedAt), "CreatedAt should roundtrip through the injected clock")
	assert.True(t, ts.Equal(got.LastActiveAt), "LastActiveAt should roundtrip through the injected clock")
	assert.Equal(t, rec.ID, got.ID)
	assert.Equal(t, rec.Agent, got.Agent)
	assert.Equal(t, rec.Namespace, got.Namespace)
}

// TestSessionStoreUpdateRejectsArchived pins fix #26c: Update refuses a record
// already in StatusArchived, so a future caller cannot land a dead session
// back into the live keyspace (and its live-key budget). The live record is
// otherwise untouched.
func TestSessionStoreUpdateRejectsArchived(t *testing.T) {
	t.Parallel()
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	ctx := t.Context()

	rec := SessionRecord{ID: "sess-1", Agent: "a", Namespace: "ns", Status: StatusActive}
	require.NoError(t, store.Create(ctx, rec, 0, time.Hour))
	_, version, err := store.Get(ctx, "ns", "a", "sess-1")
	require.NoError(t, err)

	rec.Status = StatusArchived
	err = store.Update(ctx, rec, version, time.Hour)
	require.ErrorIs(t, err, ErrUpdateArchived)

	got, _, err := store.Get(ctx, "ns", "a", "sess-1")
	require.NoError(t, err)
	assert.Equal(t, StatusActive, got.Status, "the live record must be left untouched by the rejected Update")
}

func TestSessionStoreCreateDuplicateExists(t *testing.T) {
	t.Parallel()
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	ctx := t.Context()

	rec := SessionRecord{ID: "sess-1", Agent: "a", Namespace: "ns", Status: StatusActive}
	require.NoError(t, store.Create(ctx, rec, 0, time.Hour))

	err := store.Create(ctx, rec, 0, time.Hour)
	require.ErrorIs(t, err, ErrSessionExists)
}

func TestSessionStoreCreateQuotaExceeded(t *testing.T) {
	t.Parallel()
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	ctx := t.Context()

	rec1 := SessionRecord{ID: "s1", Agent: "a", Namespace: "ns", Status: StatusActive}
	rec2 := SessionRecord{ID: "s2", Agent: "a", Namespace: "ns", Status: StatusActive}
	require.NoError(t, store.Create(ctx, rec1, 1, time.Hour))

	err := store.Create(ctx, rec2, 1, time.Hour)
	require.ErrorIs(t, err, ErrSessionQuota)
}

func TestSessionStoreArchiveFreesQuota(t *testing.T) {
	t.Parallel()
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	ctx := t.Context()

	rec1 := SessionRecord{ID: "s1", Agent: "a", Namespace: "ns", Status: StatusActive}
	require.NoError(t, store.Create(ctx, rec1, 1, time.Hour))

	_, version, err := store.Get(ctx, "ns", "a", "s1")
	require.NoError(t, err)
	require.NoError(t, store.Archive(ctx, rec1, version, 7*24*time.Hour))

	// The live keyspace no longer holds s1, so a second create against the
	// same MaxSessions=1 budget must succeed — the sibling-keyspace property.
	rec2 := SessionRecord{ID: "s2", Agent: "a", Namespace: "ns", Status: StatusActive}
	require.NoError(t, store.Create(ctx, rec2, 1, time.Hour))
}

func TestSessionStoreUpdateStaleVersionConflict(t *testing.T) {
	t.Parallel()
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	ctx := t.Context()

	rec := SessionRecord{ID: "s1", Agent: "a", Namespace: "ns", Status: StatusActive}
	require.NoError(t, store.Create(ctx, rec, 0, time.Hour))

	_, version, err := store.Get(ctx, "ns", "a", "s1")
	require.NoError(t, err)

	rec.Status = StatusIdle
	require.NoError(t, store.Update(ctx, rec, version, time.Hour))

	// version is now stale (the successful Update above bumped it).
	err = store.Update(ctx, rec, version, time.Hour)
	require.ErrorIs(t, err, statestore.ErrVersionConflict)
}

func TestSessionStoreArchiveRemovesFromListAndExpiresAfterRetention(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		ctx := t.Context()

		rec := SessionRecord{ID: "s1", Agent: "a", Namespace: "ns", Status: StatusActive}
		require.NoError(t, store.Create(ctx, rec, 0, time.Hour))

		_, version, err := store.Get(ctx, "ns", "a", "s1")
		require.NoError(t, err)
		require.NoError(t, store.Archive(ctx, rec, version, time.Hour))

		list, _, err := store.List(ctx, "ns", "a", "", 10)
		require.NoError(t, err)
		assert.Empty(t, list, "archived record must not appear in List")

		// The archived copy is present before its retention TTL fires.
		v, err := kv.Get(ctx, archivedScope("ns", "a"), "s1")
		require.NoError(t, err)
		assert.NotEmpty(t, v.Data)

		time.Sleep(2 * time.Hour)

		_, err = kv.Get(ctx, archivedScope("ns", "a"), "s1")
		require.ErrorIs(t, err, statestore.ErrNotFound, "archived copy must expire after retention")
	})
}

func TestSessionStoreOrphanLiveTTLWithNoSweeper(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		kv := newTestKV(t)
		store := NewSessionStore(kv, time.Now)
		ctx := t.Context()

		rec := SessionRecord{ID: "s1", Agent: "a", Namespace: "ns", Status: StatusActive}
		require.NoError(t, store.Create(ctx, rec, 0, time.Minute))

		_, _, err := store.Get(ctx, "ns", "a", "s1")
		require.NoError(t, err)

		time.Sleep(2 * time.Minute)

		_, _, err = store.Get(ctx, "ns", "a", "s1")
		require.ErrorIs(t, err, statestore.ErrNotFound, "an orphaned record must self-expire on its liveTTL with no sweeper")
	})
}

func TestSessionStoreArchiveVsReviveRaceAborts(t *testing.T) {
	t.Parallel()
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	ctx := t.Context()

	rec := SessionRecord{ID: "s1", Agent: "a", Namespace: "ns", Status: StatusActive}
	require.NoError(t, store.Create(ctx, rec, 0, time.Hour))

	_, version, err := store.Get(ctx, "ns", "a", "s1")
	require.NoError(t, err)

	// A concurrent turn revives (updates) the session between the sweeper's
	// Get and its Archive call, bumping the version out from under it.
	revived := rec
	revived.Status = StatusActive
	revived.LastActiveAt = time.Now()
	require.NoError(t, store.Update(ctx, revived, version, time.Hour))

	err = store.Archive(ctx, rec, version, 24*time.Hour)
	require.ErrorIs(t, err, statestore.ErrVersionConflict)

	// The live record must remain intact — Archive aborted, did not delete it.
	got, _, err := store.Get(ctx, "ns", "a", "s1")
	require.NoError(t, err)
	assert.Equal(t, StatusActive, got.Status)

	// The archived copy Archive wrote speculatively must have been cleaned up.
	_, err = kv.Get(ctx, archivedScope("ns", "a"), "s1")
	require.ErrorIs(t, err, statestore.ErrNotFound)
}

// TestSessionStoreArchiveRaceBetweenTwoArchiversLeavesOneRecord covers the
// other CAS-delete-conflict cause: not a revive, but a second Archive call
// (a second replica's sweeper) racing the same pre-read version. Both calls
// write the archived copy unconditionally before their CAS-delete, so the
// second call's Set overwrites the first's archived copy with identical
// content — benign. The dangerous part is what happens on its CAS-delete
// conflict: the live key is now genuinely absent (the first Archive call
// already deleted it), not revived. Blindly deleting the archived copy on
// any delete conflict destroys the just-committed archived record from BOTH
// keyspaces, losing the session entirely.
func TestSessionStoreArchiveRaceBetweenTwoArchiversLeavesOneRecord(t *testing.T) {
	t.Parallel()
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	ctx := t.Context()

	rec := SessionRecord{ID: "s1", Agent: "a", Namespace: "ns", Status: StatusIdle}
	require.NoError(t, store.Create(ctx, rec, 0, time.Hour))

	_, version, err := store.Get(ctx, "ns", "a", "s1")
	require.NoError(t, err)

	// Two replicas both read version and independently decide to archive.
	err1 := store.Archive(ctx, rec, version, 7*24*time.Hour)
	err2 := store.Archive(ctx, rec, version, 7*24*time.Hour)

	require.NoError(t, err1, "the first Archive call must win outright")
	require.NoError(t, err2, "the second Archive call must treat 'already archived by a peer' as a no-op success, not an error")

	// The archived record must have survived both racing calls.
	v, err := kv.Get(ctx, archivedScope("ns", "a"), "s1")
	require.NoError(t, err, "archived record must survive two racing Archive calls at the same version")
	assert.NotEmpty(t, v.Data)

	// The live key must be gone (both agree the session is archived).
	_, _, err = store.Get(ctx, "ns", "a", "s1")
	assert.ErrorIs(t, err, statestore.ErrNotFound)
}

func TestSessionStoreListPagination(t *testing.T) {
	t.Parallel()
	kv := newTestKV(t)
	store := NewSessionStore(kv, time.Now)
	ctx := t.Context()

	for _, id := range []string{"a", "b", "c"} {
		rec := SessionRecord{ID: id, Agent: "agent1", Namespace: "ns", Status: StatusActive}
		require.NoError(t, store.Create(ctx, rec, 0, time.Hour))
	}

	page1, next1, err := store.List(ctx, "ns", "agent1", "", 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	require.NotEmpty(t, next1)

	page2, next2, err := store.List(ctx, "ns", "agent1", next1, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Empty(t, next2)

	var ids []string
	for _, r := range page1 {
		ids = append(ids, r.ID)
	}
	for _, r := range page2 {
		ids = append(ids, r.ID)
	}
	assert.ElementsMatch(t, []string{"a", "b", "c"}, ids)
}

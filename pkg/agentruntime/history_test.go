// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
	_ "github.com/fission/fission/pkg/statestore/memory"
	"github.com/fission/fission/pkg/statesvc/stateapi"
)

// newTestStores opens a fresh in-memory statestore and returns its EventLog
// and KVStore capabilities for one test.
func newTestStores(t *testing.T) (statestore.EventLog, statestore.KVStore) {
	t.Helper()
	caps, err := statestore.Open(t.Context(), statestore.Config{Driver: "memory"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })
	el, err := caps.EventLog()
	require.NoError(t, err)
	kv, err := caps.KV()
	require.NoError(t, err)
	return el, kv
}

func TestHistoryStoreReadHeadRoundtrip(t *testing.T) {
	t.Parallel()
	el, kv := newTestStores(t)
	hist := NewHistoryStore(el, kv)
	ctx := t.Context()

	// Append directly through the internal, scope-qualified stream name —
	// exactly the shape the SDK's statesvc-side append produces — never
	// through HistoryStore, which has no append path.
	stream := stateapi.StreamName("ns1", "myks", HistoryClientStream("sess-1"))
	head, err := el.Append(ctx, stream, statestore.AppendAny, []statestore.Event{
		{Type: "turn.start", Payload: []byte(`{"n":1}`)},
		{Type: "turn.end", Payload: []byte(`{"n":2}`)},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, head)

	gotHead, err := hist.Head(ctx, "ns1", "myks", "sess-1")
	require.NoError(t, err)
	assert.EqualValues(t, 2, gotHead)

	events, err := hist.Read(ctx, "ns1", "myks", "sess-1", 0, 100)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, "turn.start", events[0].Type)
	assert.Equal(t, "turn.end", events[1].Type)
	assert.EqualValues(t, 1, events[0].Seq)
	assert.EqualValues(t, 2, events[1].Seq)

	// fromSeq excludes already-seen events.
	tail, err := hist.Read(ctx, "ns1", "myks", "sess-1", 1, 100)
	require.NoError(t, err)
	require.Len(t, tail, 1)
	assert.Equal(t, "turn.end", tail[0].Type)
}

func TestHistoryStoreReadHeadEmptySession(t *testing.T) {
	t.Parallel()
	el, kv := newTestStores(t)
	hist := NewHistoryStore(el, kv)
	ctx := t.Context()

	head, err := hist.Head(ctx, "ns1", "myks", "no-such-session")
	require.NoError(t, err)
	assert.EqualValues(t, 0, head)

	events, err := hist.Read(ctx, "ns1", "myks", "no-such-session", 0, 100)
	require.NoError(t, err)
	assert.Empty(t, events)
}

// TestHistoryStoreScopeIsolation pins that Read/Head are scoped per
// (ns, keyspace, sessionID): events appended under a different namespace or
// keyspace never leak into another session's read.
func TestHistoryStoreScopeIsolation(t *testing.T) {
	t.Parallel()
	el, kv := newTestStores(t)
	hist := NewHistoryStore(el, kv)
	ctx := t.Context()

	streamA := stateapi.StreamName("ns1", "ks-a", HistoryClientStream("sess-1"))
	_, err := el.Append(ctx, streamA, statestore.AppendAny, []statestore.Event{{Type: "a"}})
	require.NoError(t, err)

	streamB := stateapi.StreamName("ns1", "ks-b", HistoryClientStream("sess-1"))
	_, err = el.Append(ctx, streamB, statestore.AppendAny, []statestore.Event{{Type: "b"}})
	require.NoError(t, err)

	eventsA, err := hist.Read(ctx, "ns1", "ks-a", "sess-1", 0, 100)
	require.NoError(t, err)
	require.Len(t, eventsA, 1)
	assert.Equal(t, "a", eventsA[0].Type)
}

func TestHistoryStoreGetCheckpointRoundtrip(t *testing.T) {
	t.Parallel()
	el, kv := newTestStores(t)
	hist := NewHistoryStore(el, kv)
	ctx := t.Context()

	ts := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	rec := CheckpointRecord{
		V:                 1,
		CoveredThroughSeq: 42,
		Kind:              "summary",
		Payload:           jsontext.Value(`{"summary":"so far"}`),
		CreatedAt:         ts,
		Turn:              7,
	}
	data, err := json.Marshal(rec)
	require.NoError(t, err)
	err = kv.Set(ctx, stateScope("ns1", "myks"), CheckpointKeyPrefix+"sess-1", data, statestore.SetOptions{})
	require.NoError(t, err)

	got, err := hist.GetCheckpoint(ctx, "ns1", "myks", "sess-1")
	require.NoError(t, err)
	assert.Equal(t, rec.V, got.V)
	assert.Equal(t, rec.CoveredThroughSeq, got.CoveredThroughSeq)
	assert.Equal(t, rec.Kind, got.Kind)
	assert.Equal(t, string(rec.Payload), string(got.Payload))
	assert.True(t, ts.Equal(got.CreatedAt))
	assert.Equal(t, rec.Turn, got.Turn)
}

// TestHistoryStoreGetCheckpointNotFound pins that GetCheckpoint passes
// statestore.ErrNotFound through unmapped when no checkpoint exists yet.
func TestHistoryStoreGetCheckpointNotFound(t *testing.T) {
	t.Parallel()
	el, kv := newTestStores(t)
	hist := NewHistoryStore(el, kv)
	ctx := t.Context()

	_, err := hist.GetCheckpoint(ctx, "ns1", "myks", "no-such-session")
	require.ErrorIs(t, err, statestore.ErrNotFound)
}

func TestHistoryStoreDeleteCheckpointIdempotent(t *testing.T) {
	t.Parallel()
	el, kv := newTestStores(t)
	hist := NewHistoryStore(el, kv)
	ctx := t.Context()

	// Absent key: DeleteCheckpoint must not error.
	require.NoError(t, hist.DeleteCheckpoint(ctx, "ns1", "myks", "sess-1"))

	data, err := json.Marshal(CheckpointRecord{V: 1, Kind: "summary"})
	require.NoError(t, err)
	require.NoError(t, kv.Set(ctx, stateScope("ns1", "myks"), CheckpointKeyPrefix+"sess-1", data, statestore.SetOptions{}))

	// Present key: first delete removes it.
	require.NoError(t, hist.DeleteCheckpoint(ctx, "ns1", "myks", "sess-1"))
	_, err = hist.GetCheckpoint(ctx, "ns1", "myks", "sess-1")
	require.ErrorIs(t, err, statestore.ErrNotFound)

	// Second delete on the now-absent key is still a no-op success.
	require.NoError(t, hist.DeleteCheckpoint(ctx, "ns1", "myks", "sess-1"))
}

func TestHistoryStoreTrimBelow(t *testing.T) {
	t.Parallel()
	el, kv := newTestStores(t)
	hist := NewHistoryStore(el, kv)
	ctx := t.Context()

	stream := stateapi.StreamName("ns1", "myks", HistoryClientStream("sess-1"))
	head, err := el.Append(ctx, stream, statestore.AppendAny, []statestore.Event{
		{Type: "e1"}, {Type: "e2"}, {Type: "e3"}, {Type: "e4"},
	})
	require.NoError(t, err)
	require.EqualValues(t, 4, head)

	// Drop events with Seq < 3, leaving the tail (Seq 3, 4) visible.
	require.NoError(t, hist.TrimBelow(ctx, "ns1", "myks", "sess-1", 3))

	events, err := hist.Read(ctx, "ns1", "myks", "sess-1", 0, 100)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.EqualValues(t, 3, events[0].Seq)
	assert.EqualValues(t, 4, events[1].Seq)

	// The append point (head) is unaffected by Trim.
	gotHead, err := hist.Head(ctx, "ns1", "myks", "sess-1")
	require.NoError(t, err)
	assert.EqualValues(t, 4, gotHead)
}

func TestHistoryClientStream(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "agentlog/sess-1", HistoryClientStream("sess-1"))
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
)

func newEventLog(t *testing.T) statestore.EventLog {
	t.Helper()
	s := newStore()
	el, err := s.EventLog()
	require.NoError(t, err)
	return el
}

// E1: Append with expectedSeq admits exactly one writer per sequence slot;
// streams are gap-free and append-ordered.
func TestMemoryEventLog_E1_AppendSingleWriterPerSeq(t *testing.T) {
	t.Parallel()
	el := newEventLog(t)
	ctx := t.Context()

	head, err := el.Append(ctx, "wfrun/1", 0, []statestore.Event{{Type: "sched"}})
	require.NoError(t, err)
	require.EqualValues(t, 1, head)

	// A second appender with the stale expectedSeq loses.
	_, err = el.Append(ctx, "wfrun/1", 0, []statestore.Event{{Type: "dup"}})
	require.ErrorIs(t, err, statestore.ErrVersionConflict)

	evs, err := el.Read(ctx, "wfrun/1", 0, 10)
	require.NoError(t, err)
	require.Len(t, evs, 1) // the loser never landed; stream stays gap-free
	require.EqualValues(t, 1, evs[0].Seq)
	require.False(t, evs[0].At.IsZero()) // At assigned by the store
}

// E1 under concurrency: many racing appenders to the same empty slot, exactly
// one wins.
func TestMemoryEventLog_E1_ConcurrentAppendOneWinner(t *testing.T) {
	t.Parallel()
	el := newEventLog(t)
	ctx := t.Context()

	const writers = 16
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
	evs, err := el.Read(ctx, "race", 0, 100)
	require.NoError(t, err)
	require.Len(t, evs, 1)
}

func TestMemoryEventLog_AppendManyReadTrim(t *testing.T) {
	t.Parallel()
	el := newEventLog(t)
	ctx := t.Context()

	// Append three events in one call: seqs 1,2,3.
	head, err := el.Append(ctx, "s", 0, []statestore.Event{{Type: "a"}, {Type: "b"}, {Type: "c"}})
	require.NoError(t, err)
	require.EqualValues(t, 3, head)

	// Continue from head 3 -> seqs 4,5.
	head, err = el.Append(ctx, "s", 3, []statestore.Event{{Type: "d"}, {Type: "e"}})
	require.NoError(t, err)
	require.EqualValues(t, 5, head)

	// Read from seq 2, limit 2 -> seqs 3,4.
	evs, err := el.Read(ctx, "s", 2, 2)
	require.NoError(t, err)
	require.Len(t, evs, 2)
	assert.EqualValues(t, 3, evs[0].Seq)
	assert.EqualValues(t, 4, evs[1].Seq)

	// Trim below 4 -> seqs 1,2,3 gone; head unchanged so Append still needs 5.
	require.NoError(t, el.Trim(ctx, "s", 4))
	evs, err = el.Read(ctx, "s", 0, 100)
	require.NoError(t, err)
	require.Len(t, evs, 2)
	assert.EqualValues(t, 4, evs[0].Seq)
	assert.EqualValues(t, 5, evs[1].Seq)

	_, err = el.Append(ctx, "s", 5, []statestore.Event{{Type: "f"}})
	require.NoError(t, err, "trim must not move the head")
}

func TestMemoryEventLog_EmptyAppendIsNoOp(t *testing.T) {
	t.Parallel()
	el := newEventLog(t)
	head, err := el.Append(t.Context(), "s", 0, nil)
	require.NoError(t, err)
	require.EqualValues(t, 0, head)
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
)

const qn = "asyncinv/ns"

func newQueue(t *testing.T) (statestore.Queue, *Store) {
	t.Helper()
	s := newStore()
	q, err := s.Queue()
	require.NoError(t, err)
	return q, s
}

func TestMemoryQueue_EnqueueLeaseAck(t *testing.T) {
	t.Parallel()
	q, _ := newQueue(t)
	ctx := t.Context()

	id, err := q.Enqueue(ctx, qn, statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, id)

	l, err := q.Lease(ctx, qn, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)
	require.Equal(t, id, l[0].ID)
	require.Equal(t, []byte("m"), l[0].Body)
	require.Equal(t, 1, l[0].Attempts)

	require.NoError(t, q.Ack(ctx, l[0].Receipt))

	// Nothing leasable after ack.
	l, err = q.Lease(ctx, qn, 10, time.Minute)
	require.NoError(t, err)
	require.Empty(t, l)
}

// Q1: a message reaches a terminal settle at most once.
func TestMemoryQueue_Q1_SettledAtMostOnce(t *testing.T) {
	t.Parallel()
	q, _ := newQueue(t)
	ctx := t.Context()
	_, err := q.Enqueue(ctx, qn, statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
	require.NoError(t, err)
	l, err := q.Lease(ctx, qn, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)

	require.NoError(t, q.Ack(ctx, l[0].Receipt))
	// A second settle with the same receipt cannot land again.
	require.ErrorIs(t, q.Ack(ctx, l[0].Receipt), statestore.ErrInvalidReceipt)
	require.ErrorIs(t, q.Kill(ctx, l[0].Receipt, "x"), statestore.ErrInvalidReceipt)
}

// Q2: only the current lease decides the outcome — a stale delivery's settle is
// rejected while a newer lease's delivery is in flight (the epoch guard).
func TestMemoryQueue_Q2_NoOrphanedCurrentDelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, _ := newQueue(t)
		ctx := t.Context()
		_, err := q.Enqueue(ctx, qn, statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
		require.NoError(t, err)

		l1, err := q.Lease(ctx, qn, 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, l1, 1)

		time.Sleep(2 * time.Minute) // lease expires; l1's delivery is now stale

		l2, err := q.Lease(ctx, qn, 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, l2, 1)
		require.Equal(t, 2, l2[0].Attempts)

		// The stale receipt (old epoch) cannot settle the current delivery.
		require.ErrorIs(t, q.Ack(ctx, l1[0].Receipt), statestore.ErrInvalidReceipt)
		// The current lease decides.
		require.NoError(t, q.Ack(ctx, l2[0].Receipt))
	})
}

// Q3: dead-lettering via Nack happens only when the attempt budget is spent.
func TestMemoryQueue_Q3_DeadImpliesExhausted(t *testing.T) {
	t.Parallel()
	s := newStore()
	s.maxAttempts = 2
	q, err := s.Queue()
	require.NoError(t, err)
	ctx := t.Context()

	_, err = q.Enqueue(ctx, qn, statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
	require.NoError(t, err)

	// Attempt 1: lease -> nack (budget not spent -> requeued).
	l, err := q.Lease(ctx, qn, 1, time.Minute)
	require.NoError(t, err)
	require.Equal(t, 1, l[0].Attempts)
	require.NoError(t, q.Nack(ctx, l[0].Receipt, 0))
	dl, err := q.DeadLetters(ctx, qn, statestore.Page{})
	require.NoError(t, err)
	require.Empty(t, dl, "not dead before the budget is spent")

	// Attempt 2 (== maxAttempts): lease -> nack -> dead.
	l, err = q.Lease(ctx, qn, 1, time.Minute)
	require.NoError(t, err)
	require.Equal(t, 2, l[0].Attempts)
	require.NoError(t, q.Nack(ctx, l[0].Receipt, 0))
	dl, err = q.DeadLetters(ctx, qn, statestore.Page{})
	require.NoError(t, err)
	require.Len(t, dl, 1)
	require.GreaterOrEqual(t, dl[0].Attempts, s.maxAttempts) // dead ⇒ exhausted
}

// Q4: at most MaxAttempts deliveries ever start.
func TestMemoryQueue_Q4_AttemptsBounded(t *testing.T) {
	t.Parallel()
	s := newStore()
	s.maxAttempts = 3
	q, err := s.Queue()
	require.NoError(t, err)
	ctx := t.Context()
	_, err = q.Enqueue(ctx, qn, statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
	require.NoError(t, err)

	leases := 0
	for range 10 {
		l, err := q.Lease(ctx, qn, 1, time.Minute)
		require.NoError(t, err)
		if len(l) == 0 {
			break
		}
		leases++
		require.LessOrEqual(t, l[0].Attempts, s.maxAttempts)
		require.NoError(t, q.Nack(ctx, l[0].Receipt, 0))
	}
	require.Equal(t, s.maxAttempts, leases, "exactly MaxAttempts deliveries start, then it is dead")
}

// T1: conservation — enqueued == queued + leased + acked + dead, always.
func TestMemoryQueue_T1_Conservation(t *testing.T) {
	t.Parallel()
	q, s := newQueue(t)
	ctx := t.Context()

	for i := range 5 {
		_, err := q.Enqueue(ctx, qn, statestore.Message{Body: []byte{byte('a' + i)}}, statestore.EnqueueOptions{})
		require.NoError(t, err)
	}
	l, err := q.Lease(ctx, qn, 3, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 3)
	require.NoError(t, q.Ack(ctx, l[0].Receipt))
	require.NoError(t, q.Kill(ctx, l[1].Receipt, "boom"))
	// l[2] stays leased; two remain queued.

	st := s.ConservationStats()
	assert.EqualValues(t, 5, st.Enqueued)
	assert.EqualValues(t, 5, st.Queued+st.Leased+st.Acked+st.Dead)
	assert.Zero(t, st.Enqueued-(st.Queued+st.Leased+st.Acked+st.Dead), "conservation drift must be zero")
}

func TestMemoryQueue_Delay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, _ := newQueue(t)
		ctx := t.Context()
		_, err := q.Enqueue(ctx, qn, statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{Delay: time.Hour})
		require.NoError(t, err)

		l, err := q.Lease(ctx, qn, 1, time.Minute)
		require.NoError(t, err)
		require.Empty(t, l, "not visible before the delay elapses")

		time.Sleep(time.Hour + time.Second)
		l, err = q.Lease(ctx, qn, 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, l, 1)
	})
}

func TestMemoryQueue_DedupCollapse(t *testing.T) {
	t.Parallel()
	q, s := newQueue(t)
	ctx := t.Context()
	id1, err := q.Enqueue(ctx, qn, statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{DedupKey: "k"})
	require.NoError(t, err)
	id2, err := q.Enqueue(ctx, qn, statestore.Message{Body: []byte("m2")}, statestore.EnqueueOptions{DedupKey: "k"})
	require.NoError(t, err)
	require.Equal(t, id1, id2, "same dedup key collapses to the same message")
	require.EqualValues(t, 1, s.ConservationStats().Enqueued)
}

func TestMemoryQueue_NackRequeueBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, _ := newQueue(t)
		ctx := t.Context()
		_, err := q.Enqueue(ctx, qn, statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
		require.NoError(t, err)
		l, err := q.Lease(ctx, qn, 1, time.Minute)
		require.NoError(t, err)
		require.NoError(t, q.Nack(ctx, l[0].Receipt, 30*time.Second))

		l, err = q.Lease(ctx, qn, 1, time.Minute)
		require.NoError(t, err)
		require.Empty(t, l, "requeued message is invisible during backoff")

		time.Sleep(31 * time.Second)
		l, err = q.Lease(ctx, qn, 1, time.Minute)
		require.NoError(t, err)
		require.Len(t, l, 1)
	})
}

func TestMemoryQueue_KillImmediateAndRedrive(t *testing.T) {
	t.Parallel()
	q, _ := newQueue(t)
	ctx := t.Context()
	id, err := q.Enqueue(ctx, qn, statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
	require.NoError(t, err)
	l, err := q.Lease(ctx, qn, 1, time.Minute)
	require.NoError(t, err)
	require.NoError(t, q.Kill(ctx, l[0].Receipt, "http_4xx")) // permanent, before budget spent

	dl, err := q.DeadLetters(ctx, qn, statestore.Page{})
	require.NoError(t, err)
	require.Len(t, dl, 1)
	require.Equal(t, id, dl[0].ID)
	require.Equal(t, "http_4xx", dl[0].Reason)

	// Redrive returns it to the queue with attempts reset.
	require.NoError(t, q.Redrive(ctx, qn, []string{id}))
	dl, err = q.DeadLetters(ctx, qn, statestore.Page{})
	require.NoError(t, err)
	require.Empty(t, dl)
	l, err = q.Lease(ctx, qn, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l, 1)
	require.Equal(t, 1, l[0].Attempts, "attempts reset on redrive")
}

func TestMemoryQueue_InvalidReceipt(t *testing.T) {
	t.Parallel()
	q, _ := newQueue(t)
	require.ErrorIs(t, q.Ack(t.Context(), "not-a-receipt"), statestore.ErrInvalidReceipt)
}

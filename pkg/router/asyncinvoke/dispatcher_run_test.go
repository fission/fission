// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
)

// delivererFunc adapts a function to the Deliverer interface.
type delivererFunc func(context.Context, Envelope, string, int) DeliveryResult

func (f delivererFunc) Deliver(ctx context.Context, e Envelope, id string, attempt int) DeliveryResult {
	return f(ctx, e, id, attempt)
}

func enqueueEnvelope(t *testing.T, q statestore.Queue, env Envelope) string {
	t.Helper()
	body, err := env.Encode()
	require.NoError(t, err)
	id, err := q.Enqueue(t.Context(), DefaultQueue, statestore.Message{Body: body}, statestore.EnqueueOptions{})
	require.NoError(t, err)
	return id
}

func drained(t *testing.T, q statestore.Queue) bool {
	st, err := q.Stats(t.Context(), DefaultQueue)
	require.NoError(t, err)
	return st.Visible == 0 && st.Leased == 0 && st.Dead == 0
}

// TestRunDrainsQueue: the loop leases, delivers, and acks every message.
func TestRunDrainsQueue(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	for range 5 {
		enqueueEnvelope(t, q, Envelope{Version: EnvelopeVersion, Namespace: "ns", Function: "fn", EnqueueTime: time.Now()})
	}
	var delivered atomic.Int64
	d := New(Options{
		Queue:        q,
		PollInterval: time.Millisecond,
		Logger:       logr.Discard(),
		Deliverer: delivererFunc(func(context.Context, Envelope, string, int) DeliveryResult {
			delivered.Add(1)
			return DeliveryResult{StatusCode: 200}
		}),
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	require.Eventually(t, func() bool { return drained(t, q) }, 2*time.Second, 5*time.Millisecond)
	assert.GreaterOrEqual(t, delivered.Load(), int64(5))
}

// TestRunDeadLettersRepeatedFailure: a message that always 5xxes is dead-lettered
// with the retries-exhausted reason once the attempt budget is spent.
func TestRunDeadLettersRepeatedFailure(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	id := enqueueEnvelope(t, q, Envelope{
		Version: EnvelopeVersion, Namespace: "ns", Function: "fn", EnqueueTime: time.Now(),
		Policy: Policy{NoJitter: true, BackoffBase: time.Millisecond, BackoffCap: time.Millisecond, MaxAge: time.Hour},
	})
	d := New(Options{
		Queue: q, PollInterval: time.Millisecond, Logger: logr.Discard(),
		Deliverer: scriptedDeliverer{DeliveryResult{StatusCode: 500}},
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	require.Eventually(t, func() bool {
		dead, err := q.DeadLetters(t.Context(), DefaultQueue, statestore.Page{})
		require.NoError(t, err)
		return len(dead) == 1
	}, 3*time.Second, 5*time.Millisecond)

	dead, err := q.DeadLetters(t.Context(), DefaultQueue, statestore.Page{})
	require.NoError(t, err)
	require.Len(t, dead, 1)
	assert.Equal(t, id, dead[0].ID)
	assert.Equal(t, statestore.ReasonRetriesExhausted, dead[0].Reason)
}

// TestRunRetryThenSucceed: a message that fails once then succeeds is delivered
// at least twice and ends acked (drained).
func TestRunRetryThenSucceed(t *testing.T) {
	t.Parallel()
	q := memQueue(t)
	enqueueEnvelope(t, q, Envelope{
		Version: EnvelopeVersion, Namespace: "ns", Function: "fn", EnqueueTime: time.Now(),
		Policy: Policy{NoJitter: true, BackoffBase: time.Millisecond, BackoffCap: time.Millisecond, MaxAge: time.Hour},
	})
	var attempts atomic.Int64
	d := New(Options{
		Queue: q, PollInterval: time.Millisecond, Logger: logr.Discard(),
		Deliverer: delivererFunc(func(context.Context, Envelope, string, int) DeliveryResult {
			if attempts.Add(1) == 1 {
				return DeliveryResult{StatusCode: 500}
			}
			return DeliveryResult{StatusCode: 200}
		}),
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	// Wait for the retry to have happened AND the message to be fully drained
	// (acked). drained() alone is insufficient: between the failed delivery and its
	// retry the message is backoff-delayed — Queued but neither visible, leased,
	// nor dead — so a bare drained() check can observe that window and pass before
	// the retry delivery occurs.
	require.Eventually(t, func() bool {
		return attempts.Load() >= 2 && drained(t, q)
	}, 3*time.Second, 5*time.Millisecond)
	assert.GreaterOrEqual(t, attempts.Load(), int64(2), "delivered at least twice (retry then success)")
}

// TestBackoffDelaysRedelivery runs the loop under virtual time and asserts a
// nacked message is not re-delivered before its backoff elapses (the RFC-0024
// timing property, checked in a testing/synctest bubble).
func TestBackoffDelaysRedelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := memQueue(t)
		enqueueEnvelope(t, q, Envelope{
			Version: EnvelopeVersion, Namespace: "ns", Function: "fn", EnqueueTime: time.Now(),
			Policy: Policy{NoJitter: true, BackoffBase: time.Hour, BackoffCap: time.Hour, MaxAge: 24 * time.Hour},
		})
		var attempts atomic.Int64
		d := New(Options{
			Queue: q, PollInterval: time.Second, Logger: logr.Discard(),
			Deliverer: delivererFunc(func(context.Context, Envelope, string, int) DeliveryResult {
				attempts.Add(1)
				return DeliveryResult{StatusCode: 500}
			}),
		})
		ctx, cancel := context.WithCancel(t.Context())
		go func() { _ = d.Run(ctx) }()

		// First delivery happens, then the message is nacked with a 1h backoff.
		synctest.Wait()
		require.EqualValues(t, 1, attempts.Load(), "one delivery so far")

		// 30 minutes in — still within the backoff, no re-delivery.
		time.Sleep(30 * time.Minute)
		synctest.Wait()
		require.EqualValues(t, 1, attempts.Load(), "no re-delivery before backoff elapses")

		// Past the 1h backoff — re-delivered.
		time.Sleep(31 * time.Minute)
		synctest.Wait()
		require.GreaterOrEqual(t, attempts.Load(), int64(2), "re-delivered after backoff")

		cancel()
		synctest.Wait()
	})
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/fission/fission/pkg/statestore"
)

// --- test doubles ---

type nackRec struct {
	receipt    string
	retryAfter time.Duration
}
type killRec struct{ receipt, reason string }

// recordingQueue records settle calls; the non-settle Queue methods are never
// invoked by process() and panic (nil embed) if they ever are.
type recordingQueue struct {
	statestore.Queue
	mu    sync.Mutex
	acks  []string
	nacks []nackRec
	kills []killRec
}

func (r *recordingQueue) Ack(_ context.Context, receipt string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.acks = append(r.acks, receipt)
	return nil
}
func (r *recordingQueue) Nack(_ context.Context, receipt string, retryAfter time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nacks = append(r.nacks, nackRec{receipt, retryAfter})
	return nil
}
func (r *recordingQueue) Kill(_ context.Context, receipt, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kills = append(r.kills, killRec{receipt, reason})
	return nil
}

type scriptedDeliverer struct {
	result DeliveryResult
}

func (s scriptedDeliverer) Deliver(context.Context, Envelope, string, int) DeliveryResult {
	return s.result
}

func newTestDispatcher(q statestore.Queue, d Deliverer, now time.Time) *Dispatcher {
	return New(Options{
		Queue: q, Deliverer: d, Logger: logr.Discard(),
		Now:  func() time.Time { return now },
		Rand: func() float64 { return 0.5 }, // deterministic jitter
	})
}

func leasedMsg(t *testing.T, env Envelope, attempts int) statestore.LeasedMessage {
	t.Helper()
	body, err := env.Encode()
	require.NoError(t, err)
	return statestore.LeasedMessage{ID: "asyncinv/x", Receipt: "receipt-x", Body: body, Attempts: attempts}
}

// --- classify (settle matrix) ---

func TestClassifyTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		res  DeliveryResult
		want action
	}{
		{"200 ack", DeliveryResult{StatusCode: 200}, actionAck},
		{"204 ack", DeliveryResult{StatusCode: 204}, actionAck},
		{"299 ack", DeliveryResult{StatusCode: 299}, actionAck},
		{"400 kill", DeliveryResult{StatusCode: 400}, actionKill},
		{"404 kill", DeliveryResult{StatusCode: 404}, actionKill},
		{"408 retry", DeliveryResult{StatusCode: 408}, actionRetry},
		{"429 retry", DeliveryResult{StatusCode: 429}, actionRetry},
		{"499 kill", DeliveryResult{StatusCode: 499}, actionKill},
		{"500 retry", DeliveryResult{StatusCode: 500}, actionRetry},
		{"502 retry", DeliveryResult{StatusCode: 502}, actionRetry},
		{"transport error retry", DeliveryResult{Err: errors.New("dial")}, actionRetry},
		{"error wins over 2xx", DeliveryResult{StatusCode: 200, Err: errors.New("x")}, actionRetry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, classify(tc.res))
		})
	}
}

// TestClassifyProperty checks the whole status-code space against an independent
// oracle so no boundary (100s/300s/408/429) is misfiled.
func TestClassifyProperty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		code := rapid.IntRange(0, 599).Draw(t, "code")
		hasErr := rapid.Bool().Draw(t, "hasErr")
		res := DeliveryResult{StatusCode: code}
		if hasErr {
			res.Err = errors.New("transport")
		}
		want := actionRetry
		switch {
		case hasErr:
			want = actionRetry
		case code >= 200 && code < 300:
			want = actionAck
		case code == 408 || code == 429:
			want = actionRetry
		case code >= 400 && code < 500:
			want = actionKill
		}
		require.Equal(t, want, classify(res))
	})
}

// --- backoff bounds ---

func TestBackoffProperty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		baseMs := rapid.IntRange(1, 60_000).Draw(t, "baseMs")
		capMs := rapid.IntRange(baseMs, 3_600_000).Draw(t, "capMs")
		jitter := rapid.Float64Range(0, 0.999999).Draw(t, "jitter")
		p := Policy{
			BackoffBase: time.Duration(baseMs) * time.Millisecond,
			BackoffCap:  time.Duration(capMs) * time.Millisecond,
		}
		d := New(Options{Logger: logr.Discard(), Rand: func() float64 { return jitter }})

		// No jitter: monotone non-decreasing in attempt, always within [0, cap].
		pNoJit := p
		pNoJit.NoJitter = true
		var prev time.Duration
		for attempt := 1; attempt <= 40; attempt++ {
			b := d.backoff(pNoJit, attempt)
			require.GreaterOrEqual(t, b, time.Duration(0))
			require.LessOrEqual(t, b, p.BackoffCap)
			require.GreaterOrEqual(t, b, prev, "non-decreasing without jitter")
			prev = b
		}
		// Full jitter: always within [0, cap].
		for attempt := 1; attempt <= 40; attempt++ {
			b := d.backoff(p, attempt)
			require.GreaterOrEqual(t, b, time.Duration(0))
			require.LessOrEqual(t, b, p.BackoffCap)
		}
	})
}

// --- process settle decisions ---

func TestProcessAckOn2xx(t *testing.T) {
	t.Parallel()
	rq := &recordingQueue{}
	now := time.Unix(1_000_000, 0)
	d := newTestDispatcher(rq, scriptedDeliverer{DeliveryResult{StatusCode: 200}}, now)
	d.process(context.Background(), leasedMsg(t, Envelope{EnqueueTime: now, Function: "fn", Namespace: "ns"}, 1))
	assert.Equal(t, []string{"receipt-x"}, rq.acks)
	assert.Empty(t, rq.nacks)
	assert.Empty(t, rq.kills)
}

func TestProcessKillOn4xx(t *testing.T) {
	t.Parallel()
	rq := &recordingQueue{}
	now := time.Unix(1_000_000, 0)
	d := newTestDispatcher(rq, scriptedDeliverer{DeliveryResult{StatusCode: 403}}, now)
	d.process(context.Background(), leasedMsg(t, Envelope{EnqueueTime: now}, 1))
	require.Len(t, rq.kills, 1)
	assert.Equal(t, ReasonHTTP4xx, rq.kills[0].reason)
	assert.Empty(t, rq.acks)
	assert.Empty(t, rq.nacks)
}

func TestProcessNackOn5xxWithinBudget(t *testing.T) {
	t.Parallel()
	rq := &recordingQueue{}
	now := time.Unix(1_000_000, 0)
	d := newTestDispatcher(rq, scriptedDeliverer{DeliveryResult{StatusCode: 503}}, now)
	// attempts=1, default max 3 → requeue.
	d.process(context.Background(), leasedMsg(t, Envelope{EnqueueTime: now}, 1))
	require.Len(t, rq.nacks, 1)
	assert.Empty(t, rq.kills)
}

func TestProcessKillWhenBudgetExhausted(t *testing.T) {
	t.Parallel()
	rq := &recordingQueue{}
	now := time.Unix(1_000_000, 0)
	d := newTestDispatcher(rq, scriptedDeliverer{DeliveryResult{StatusCode: 503}}, now)
	// attempts=3 == default max → dead-letter with retries-exhausted.
	d.process(context.Background(), leasedMsg(t, Envelope{EnqueueTime: now}, DefaultMaxAttempts))
	require.Len(t, rq.kills, 1)
	assert.Equal(t, statestore.ReasonRetriesExhausted, rq.kills[0].reason)
	assert.Empty(t, rq.nacks)
}

func TestProcessKillOnTransportErrorAtBudget(t *testing.T) {
	t.Parallel()
	rq := &recordingQueue{}
	now := time.Unix(1_000_000, 0)
	d := newTestDispatcher(rq, scriptedDeliverer{DeliveryResult{Err: errors.New("dial")}}, now)
	d.process(context.Background(), leasedMsg(t, Envelope{EnqueueTime: now}, DefaultMaxAttempts))
	require.Len(t, rq.kills, 1)
	assert.Equal(t, statestore.ReasonRetriesExhausted, rq.kills[0].reason)
}

func TestProcessKillOnExpired(t *testing.T) {
	t.Parallel()
	rq := &recordingQueue{}
	now := time.Unix(1_000_000, 0)
	// Enqueued 7h ago, default MaxAge 6h → expired before delivery.
	d := newTestDispatcher(rq, scriptedDeliverer{DeliveryResult{StatusCode: 200}}, now)
	d.process(context.Background(), leasedMsg(t, Envelope{EnqueueTime: now.Add(-7 * time.Hour)}, 1))
	require.Len(t, rq.kills, 1)
	assert.Equal(t, ReasonExpired, rq.kills[0].reason)
	assert.Empty(t, rq.acks, "expired invocations are never delivered")
}

func TestProcessKillOnRetryPastMaxAge(t *testing.T) {
	t.Parallel()
	rq := &recordingQueue{}
	now := time.Unix(1_000_000, 0)
	d := newTestDispatcher(rq, scriptedDeliverer{DeliveryResult{StatusCode: 503}}, now)
	// Not yet expired, but the next backoff would land past MaxAge → dead-letter
	// as expired rather than requeue work that can only expire.
	env := Envelope{
		EnqueueTime: now.Add(-6*time.Hour + 500*time.Millisecond),
		Policy:      Policy{NoJitter: true, BackoffBase: time.Second},
	}
	d.process(context.Background(), leasedMsg(t, env, 1))
	require.Len(t, rq.kills, 1)
	assert.Equal(t, ReasonExpired, rq.kills[0].reason)
	assert.Empty(t, rq.nacks)
}

func TestProcessKillOnUndecodable(t *testing.T) {
	t.Parallel()
	rq := &recordingQueue{}
	now := time.Unix(1_000_000, 0)
	d := newTestDispatcher(rq, scriptedDeliverer{DeliveryResult{StatusCode: 200}}, now)
	msg := statestore.LeasedMessage{ID: "asyncinv/x", Receipt: "receipt-x", Body: []byte("not json"), Attempts: 1}
	d.process(context.Background(), msg)
	require.Len(t, rq.kills, 1)
	assert.Equal(t, ReasonUndecodable, rq.kills[0].reason)
}

// TestDeliveryTimeoutBelowLease asserts A7: the per-delivery timeout is always
// strictly less than the lease duration, for any function timeout.
func TestDeliveryTimeoutBelowLease(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		ftSec := rapid.IntRange(0, 100_000).Draw(t, "ftSec")
		d := New(Options{Logger: logr.Discard()})
		got := d.deliveryTimeout(Envelope{FunctionTimeout: ftSec})
		require.Less(t, got, d.leaseDuration, "delivery timeout must stay below the lease (A7)")
	})
}

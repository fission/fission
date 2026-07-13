// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/fission/fission/pkg/statestore"
)

// qState is the lifecycle state of a queued message, mirroring queue.tla.
type qState int

const (
	qQueued qState = iota // waiting, may be leased when visible
	qLeased               // leased to a consumer until expiry or settle
	qAcked                // terminally settled: success
	qDead                 // terminally settled: dead-lettered
)

// qmsg is one message. epoch is bumped on every lease; a settle is valid only
// against the current epoch (the guard that upholds invariant Q2).
type qmsg struct {
	id         string
	body       []byte
	state      qState
	visibleAt  time.Time
	expiry     time.Time // lease expiry, when leased
	attempts   int       // deliveries started so far
	epoch      int64
	dedupKey   string
	reason     string // dead-letter reason
	enqueuedAt time.Time
	diedAt     time.Time
}

// queueState is one named queue: messages in insertion order plus a monotonic
// id counter and a running lease-expiration count (surfaced as a metric later).
type queueState struct {
	msgs             []*qmsg
	seq              int64
	leaseExpirations int64
}

func (s *Store) queue(name string) *queueState {
	q := s.queues[name]
	if q == nil {
		q = &queueState{}
		s.queues[name] = q
	}
	return q
}

// Enqueue implements statestore.Queue. With a DedupKey set, an existing
// not-yet-settled message with the same key collapses the enqueue.
func (s *Store) Enqueue(_ context.Context, queue string, msg statestore.Message, o statestore.EnqueueOptions) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", statestore.ErrClosed
	}
	q := s.queue(queue)
	if o.DedupKey != "" {
		for _, m := range q.msgs {
			if m.dedupKey == o.DedupKey && (m.state == qQueued || m.state == qLeased) {
				return m.id, nil
			}
		}
	}
	now := time.Now()
	q.seq++
	m := &qmsg{
		id:         fmt.Sprintf("%s/%d", queue, q.seq),
		body:       append([]byte(nil), msg.Body...),
		state:      qQueued,
		visibleAt:  now.Add(o.Delay),
		dedupKey:   o.DedupKey,
		enqueuedAt: now,
	}
	q.msgs = append(q.msgs, m)
	return m.id, nil
}

// reapExpired processes leases whose visibility timeout has passed. A message
// whose attempt budget is spent (every delivery expired without a settle — e.g.
// a repeatedly-crashing worker) is dead-lettered, matching SQS maxReceiveCount
// semantics; any other expired lease is returned to the queue for re-lease. This
// is what stops an exhausted-by-expiry message from being stranded, and it
// mirrors queue.tla's Expire action. Returns the number of expirations observed.
// Caller holds s.mu.
func (q *queueState) reapExpired(now time.Time, maxAttempts int) int64 {
	var expirations int64
	for _, m := range q.msgs {
		if m.state != qLeased || now.Before(m.expiry) {
			continue
		}
		expirations++
		if m.attempts >= maxAttempts {
			m.state = qDead
			m.reason = statestore.ReasonLeaseExpired
			m.diedAt = now
			m.dedupKey = ""
		} else {
			m.state = qQueued
			m.visibleAt = now
		}
	}
	return expirations
}

// leasable reports whether a queued message is currently visible for lease.
func (m *qmsg) leasable(now time.Time, maxAttempts int) bool {
	return m.state == qQueued && !now.Before(m.visibleAt) && m.attempts < maxAttempts
}

// Lease implements statestore.Queue: up to n currently-visible messages, each
// leased for leaseFor, with the lease epoch bumped so prior deliveries go stale.
func (s *Store) Lease(_ context.Context, queue string, n int, leaseFor time.Duration) ([]statestore.LeasedMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, statestore.ErrClosed
	}
	now := time.Now()
	q := s.queue(queue)
	q.leaseExpirations += q.reapExpired(now, s.maxAttempts)
	var out []statestore.LeasedMessage
	for _, m := range q.msgs {
		if len(out) >= n {
			break
		}
		if !m.leasable(now, s.maxAttempts) {
			continue
		}
		m.state = qLeased
		m.epoch++
		m.attempts++
		m.expiry = now.Add(leaseFor)
		out = append(out, statestore.LeasedMessage{
			ID:       m.id,
			Receipt:  statestore.EncodeReceipt(m.id, m.epoch),
			Body:     append([]byte(nil), m.body...),
			Attempts: m.attempts,
		})
	}
	return out, nil
}

// settle resolves a receipt to its message and verifies the epoch guard. It
// returns ErrInvalidReceipt for a malformed, unknown, non-leased, or
// stale-epoch receipt (invariants Q1, Q2). Caller holds s.mu.
func (q *queueState) settle(receipt string) (*qmsg, error) {
	id, epoch, ok := statestore.DecodeReceipt(receipt)
	if !ok {
		return nil, statestore.ErrInvalidReceipt
	}
	for _, m := range q.msgs {
		if m.id == id {
			if m.state != qLeased || m.epoch != epoch {
				return nil, statestore.ErrInvalidReceipt
			}
			return m, nil
		}
	}
	return nil, statestore.ErrInvalidReceipt
}

// settleAcross finds the message for a receipt across all queues (settle methods
// take only a receipt, not a queue name). Caller holds s.mu.
func (s *Store) settleAcross(receipt string) (*qmsg, error) {
	for _, q := range s.queues {
		if m, err := q.settle(receipt); err == nil {
			return m, nil
		}
	}
	return nil, statestore.ErrInvalidReceipt
}

// Ack implements statestore.Queue: settle the current delivery as succeeded.
func (s *Store) Ack(_ context.Context, receipt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return statestore.ErrClosed
	}
	m, err := s.settleAcross(receipt)
	if err != nil {
		return err
	}
	m.state = qAcked
	m.dedupKey = ""
	return nil
}

// Nack implements statestore.Queue: requeue after retryAfter, or dead-letter
// when the attempt budget is spent (invariant Q3).
func (s *Store) Nack(_ context.Context, receipt string, retryAfter time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return statestore.ErrClosed
	}
	m, err := s.settleAcross(receipt)
	if err != nil {
		return err
	}
	if m.attempts >= s.maxAttempts {
		m.state = qDead
		m.reason = statestore.ReasonRetriesExhausted
		m.diedAt = time.Now()
		m.dedupKey = ""
		return nil
	}
	m.state = qQueued
	m.visibleAt = time.Now().Add(retryAfter)
	return nil
}

// Kill implements statestore.Queue: dead-letter the current delivery immediately
// (a permanent failure), regardless of remaining attempts.
func (s *Store) Kill(_ context.Context, receipt string, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return statestore.ErrClosed
	}
	m, err := s.settleAcross(receipt)
	if err != nil {
		return err
	}
	m.state = qDead
	m.reason = reason
	m.diedAt = time.Now()
	m.dedupKey = ""
	return nil
}

// DeadLetters implements statestore.Queue: a page of dead-lettered messages,
// ordered by id, paginated by page.Token (the last id of the previous page).
func (s *Store) DeadLetters(_ context.Context, queue string, page statestore.Page) ([]statestore.DeadMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, statestore.ErrClosed
	}
	q := s.queue(queue)
	// Surface messages exhausted purely by lease expiry even if no Lease call has
	// run since, so DeadLetters reflects the true dead set.
	q.leaseExpirations += q.reapExpired(time.Now(), s.maxAttempts)
	var dead []statestore.DeadMessage
	for _, m := range q.msgs {
		if m.state != qDead || (page.Token != "" && m.id <= page.Token) {
			continue
		}
		dead = append(dead, statestore.DeadMessage{
			ID:         m.id,
			Body:       append([]byte(nil), m.body...),
			Reason:     m.reason,
			Attempts:   m.attempts,
			EnqueuedAt: m.enqueuedAt,
			DiedAt:     m.diedAt,
		})
	}
	sort.Slice(dead, func(i, j int) bool { return dead[i].ID < dead[j].ID })
	if page.Limit > 0 && len(dead) > page.Limit {
		dead = dead[:page.Limit]
	}
	return dead, nil
}

// Redrive implements statestore.Queue: return dead-lettered messages to the
// queue with attempts reset.
func (s *Store) Redrive(_ context.Context, queue string, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return statestore.ErrClosed
	}
	q := s.queue(queue)
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	now := time.Now()
	for _, m := range q.msgs {
		if m.state == qDead && want[m.id] {
			m.state = qQueued
			m.attempts = 0
			m.visibleAt = now
			m.reason = ""
			m.diedAt = time.Time{}
		}
	}
	return nil
}

// compile-time guard: the memory Store is the conservation reporter it returns
// from Queue(), so the drift gauge actually observes it.
var _ statestore.ConservationReporter = (*Store)(nil)

// ConservationStats is the reporter the metrics layer reads for the conservation
// drift gauge (invariant T1): every enqueued message is in exactly one state, so
// Enqueued == Queued + Leased + Acked + Dead by construction. The context is
// unused (in-memory read).
func (s *Store) ConservationStats(context.Context) statestore.ConservationStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st statestore.ConservationStats
	for _, q := range s.queues {
		for _, m := range q.msgs {
			st.Enqueued++
			switch m.state {
			case qQueued:
				st.Queued++
			case qLeased:
				st.Leased++
			case qAcked:
				st.Acked++
			case qDead:
				st.Dead++
			}
		}
		st.LeaseExpirations += q.leaseExpirations
	}
	return st
}

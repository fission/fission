// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/fission/fission/pkg/statestore"
)

// Queue lifecycle states (the state column of state_queue).
const (
	stQueued = "queued"
	stLeased = "leased"
	stAcked  = "acked"
	stDead   = "dead"
)

type queueStore struct{ s *Store }

// newMessageID returns a globally-unique message id (durable across restarts, so
// no per-process counter can collide).
func newMessageID(queue string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return queue + "/" + hex.EncodeToString(b[:])
}

// reap dead-letters leases whose budget is spent (exhausted purely by expiry —
// SQS maxReceiveCount) and requeues the rest. It runs on e, which is the pool
// (from DeadLetters) or the lease transaction (from Lease); the two updates are
// disjoint on attempts.
func (q *queueStore) reap(ctx context.Context, e execer, queue string, now int64) error {
	if _, err := q.s.execOn(ctx, e,
		`UPDATE state_queue SET state = ?, reason = ?, died_at = ?, dedup_key = NULL
		 WHERE queue = ? AND state = ? AND expiry <= ? AND attempts >= ?`,
		stDead, statestore.ReasonLeaseExpired, now, queue, stLeased, now, q.s.maxAttempts,
	); err != nil {
		return err
	}
	_, err := q.s.execOn(ctx, e,
		`UPDATE state_queue SET state = ?, visible_at = ?
		 WHERE queue = ? AND state = ? AND expiry <= ? AND attempts < ?`,
		stQueued, now, queue, stLeased, now, q.s.maxAttempts,
	)
	return err
}

// Enqueue implements statestore.Queue, collapsing a not-yet-settled DedupKey.
func (q *queueStore) Enqueue(ctx context.Context, queue string, msg statestore.Message, o statestore.EnqueueOptions) (string, error) {
	now := nowNanos()
	if o.DedupKey != "" {
		// Best-effort collapse: two truly-concurrent enqueues with the same key on
		// Postgres can both miss and both insert (dedup is a hint on an
		// at-least-once queue; consumers must still be idempotent). SQLite
		// serializes, so it always collapses.
		var id string
		err := q.s.queryRow(ctx,
			`SELECT id FROM state_queue WHERE queue = ? AND dedup_key = ? AND state IN (?, ?) LIMIT 1`,
			queue, o.DedupKey, stQueued, stLeased,
		).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	id := newMessageID(queue)
	var dedup sql.NullString
	if o.DedupKey != "" {
		dedup = sql.NullString{String: o.DedupKey, Valid: true}
	}
	_, err := q.s.exec(ctx,
		`INSERT INTO state_queue (id, queue, body, state, visible_at, attempts, epoch, dedup_key, enqueued_at)
		 VALUES (?, ?, ?, ?, ?, 0, 0, ?, ?)`,
		id, queue, msg.Body, stQueued, now+o.Delay.Nanoseconds(), dedup, now,
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

// Lease implements statestore.Queue: reap expirations, then lease up to n visible
// messages, bumping each lease's epoch.
func (q *queueStore) Lease(ctx context.Context, queue string, n int, leaseFor time.Duration) ([]statestore.LeasedMessage, error) {
	now := nowNanos()
	var out []statestore.LeasedMessage
	err := q.s.inTx(ctx, func(tx *sql.Tx) error {
		if err := q.reap(ctx, tx, queue, now); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, q.s.rebind(
			`SELECT id, body, epoch, attempts FROM state_queue
			 WHERE queue = ? AND state = ? AND visible_at <= ? AND attempts < ?
			 ORDER BY enqueued_at, id LIMIT ?`+q.s.dialect.LockClause),
			queue, stQueued, now, q.s.maxAttempts, n,
		)
		if err != nil {
			return err
		}
		type cand struct {
			id       string
			body     []byte
			epoch    int64
			attempts int
		}
		var cands []cand
		for rows.Next() {
			var c cand
			if err := rows.Scan(&c.id, &c.body, &c.epoch, &c.attempts); err != nil {
				_ = rows.Close()
				return err
			}
			cands = append(cands, c)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()

		expiry := now + leaseFor.Nanoseconds()
		leaseSQL := q.s.rebind(`UPDATE state_queue SET state = ?, epoch = ?, attempts = ?, expiry = ? WHERE id = ?`)
		for _, c := range cands {
			newEpoch := c.epoch + 1
			newAttempts := c.attempts + 1
			if _, err := tx.ExecContext(ctx, leaseSQL,
				stLeased, newEpoch, newAttempts, expiry, c.id,
			); err != nil {
				return err
			}
			out = append(out, statestore.LeasedMessage{
				ID:       c.id,
				Receipt:  statestore.EncodeReceipt(c.id, newEpoch),
				Body:     c.body,
				Attempts: newAttempts,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Ack implements statestore.Queue.
func (q *queueStore) Ack(ctx context.Context, receipt string) error {
	id, epoch, ok := statestore.DecodeReceipt(receipt)
	if !ok {
		return statestore.ErrInvalidReceipt
	}
	res, err := q.s.exec(ctx,
		`UPDATE state_queue SET state = ?, dedup_key = NULL WHERE id = ? AND state = ? AND epoch = ?`,
		stAcked, id, stLeased, epoch,
	)
	return settleResult(res, err)
}

// Nack implements statestore.Queue: requeue after retryAfter, or dead-letter once
// the attempt budget is spent (invariant Q3).
func (q *queueStore) Nack(ctx context.Context, receipt string, retryAfter time.Duration) error {
	id, epoch, ok := statestore.DecodeReceipt(receipt)
	if !ok {
		return statestore.ErrInvalidReceipt
	}
	now := nowNanos()
	var affected int64
	err := q.s.inTx(ctx, func(tx *sql.Tx) error {
		dead, err := tx.ExecContext(ctx, q.s.rebind(
			`UPDATE state_queue SET state = ?, reason = ?, died_at = ?, dedup_key = NULL
			 WHERE id = ? AND state = ? AND epoch = ? AND attempts >= ?`),
			stDead, statestore.ReasonRetriesExhausted, now, id, stLeased, epoch, q.s.maxAttempts,
		)
		if err != nil {
			return err
		}
		requeue, err := tx.ExecContext(ctx, q.s.rebind(
			`UPDATE state_queue SET state = ?, visible_at = ?
			 WHERE id = ? AND state = ? AND epoch = ? AND attempts < ?`),
			stQueued, now+retryAfter.Nanoseconds(), id, stLeased, epoch, q.s.maxAttempts,
		)
		if err != nil {
			return err
		}
		a, err := dead.RowsAffected()
		if err != nil {
			return err
		}
		b, err := requeue.RowsAffected()
		if err != nil {
			return err
		}
		affected = a + b
		return nil
	})
	if err != nil {
		return err
	}
	if affected == 0 {
		return statestore.ErrInvalidReceipt
	}
	return nil
}

// Kill implements statestore.Queue: dead-letter the current delivery immediately.
func (q *queueStore) Kill(ctx context.Context, receipt string, reason string) error {
	id, epoch, ok := statestore.DecodeReceipt(receipt)
	if !ok {
		return statestore.ErrInvalidReceipt
	}
	res, err := q.s.exec(ctx,
		`UPDATE state_queue SET state = ?, reason = ?, died_at = ?, dedup_key = NULL
		 WHERE id = ? AND state = ? AND epoch = ?`,
		stDead, reason, nowNanos(), id, stLeased, epoch,
	)
	return settleResult(res, err)
}

// settleResult maps an UPDATE result to a settle error: no rows changed means the
// receipt was stale, wrong-epoch, or already settled.
func settleResult(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return statestore.ErrInvalidReceipt
	}
	return nil
}

// DeadLetters implements statestore.Queue.
func (q *queueStore) DeadLetters(ctx context.Context, queue string, page statestore.Page) ([]statestore.DeadMessage, error) {
	if err := q.reap(ctx, q.s.db, queue, nowNanos()); err != nil {
		return nil, err
	}
	col := q.s.dialect.Collate
	query := `SELECT id, body, reason, attempts, enqueued_at, died_at FROM state_queue
		 WHERE queue = ? AND state = ? AND id > ?` + col + ` ORDER BY id` + col
	args := []any{queue, stDead, page.Token}
	if page.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, page.Limit)
	}
	rows, err := q.s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []statestore.DeadMessage
	for rows.Next() {
		var (
			dm         statestore.DeadMessage
			reason     sql.NullString
			enqueuedAt int64
			diedAt     sql.NullInt64
		)
		if err := rows.Scan(&dm.ID, &dm.Body, &reason, &dm.Attempts, &enqueuedAt, &diedAt); err != nil {
			return nil, err
		}
		dm.Reason = reason.String
		dm.EnqueuedAt = unixNanos(enqueuedAt)
		dm.DiedAt = nullableTime(diedAt)
		out = append(out, dm)
	}
	return out, rows.Err()
}

// Redrive implements statestore.Queue: return dead messages to the queue with
// attempts reset.
func (q *queueStore) Redrive(ctx context.Context, queue string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{stQueued, nowNanos(), queue, stDead}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := q.s.exec(ctx,
		`UPDATE state_queue SET state = ?, attempts = 0, visible_at = ?, reason = NULL, died_at = NULL
		 WHERE queue = ? AND state = ? AND id IN (`+placeholders+`)`,
		args...,
	)
	return err
}

// compile-time guard: the Store (the Capabilities value) is the conservation
// reporter NewScoped registers, so the drift gauge actually observes it.
var _ statestore.ConservationReporter = (*Store)(nil)

// ConservationStats implements statestore.ConservationReporter (invariant T1). On
// a read failure it records a scrape error and returns a zero value: because
// Drift() would then read 0 (the healthy value), the separate scrape-error
// counter is what tells operators the gauge is stale rather than clean.
func (s *Store) ConservationStats(ctx context.Context) statestore.ConservationStats {
	var st statestore.ConservationStats
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM state_queue GROUP BY state`)
	if err != nil {
		statestore.RecordConservationScrapeError(ctx)
		return st
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			state string
			count int64
		)
		if err := rows.Scan(&state, &count); err != nil {
			statestore.RecordConservationScrapeError(ctx)
			return statestore.ConservationStats{}
		}
		st.Enqueued += count
		switch state {
		case stQueued:
			st.Queued += count
		case stLeased:
			st.Leased += count
		case stAcked:
			st.Acked += count
		case stDead:
			st.Dead += count
		}
	}
	if err := rows.Err(); err != nil {
		statestore.RecordConservationScrapeError(ctx)
		return statestore.ConservationStats{}
	}
	return st
}

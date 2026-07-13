// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"strconv"
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

// encodeReceipt / decodeReceipt build the lease-scoped settle handle embedding
// (id, epoch); the settle guard checks state='leased' AND epoch matches, so a
// stale-epoch receipt can never settle a re-leased message (invariant Q2).
func encodeReceipt(id string, epoch int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id + "\x00" + strconv.FormatInt(epoch, 10)))
}

func decodeReceipt(receipt string) (id string, epoch int64, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(receipt)
	if err != nil {
		return "", 0, false
	}
	sep := strings.LastIndexByte(string(raw), 0)
	if sep < 0 {
		return "", 0, false
	}
	epoch, err = strconv.ParseInt(string(raw[sep+1:]), 10, 64)
	if err != nil {
		return "", 0, false
	}
	return string(raw[:sep]), epoch, true
}

// reapExpired dead-letters leases whose budget is spent (exhausted purely by
// expiry — SQS maxReceiveCount) and requeues the rest. Caller may run it outside
// a transaction; the two updates are disjoint on attempts.
func (q *queueStore) reapExpired(ctx context.Context, queue string, now int64) error {
	if _, err := q.s.exec(ctx,
		`UPDATE state_queue SET state = ?, reason = ?, died_at = ?, dedup_key = NULL
		 WHERE queue = ? AND state = ? AND expiry <= ? AND attempts >= ?`,
		stDead, "retries exhausted (lease expired)", now, queue, stLeased, now, q.s.maxAttempts,
	); err != nil {
		return err
	}
	_, err := q.s.exec(ctx,
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
		var id string
		err := q.s.queryRow(ctx,
			`SELECT id FROM state_queue WHERE queue = ? AND dedup_key = ? AND state IN (?, ?) LIMIT 1`,
			queue, o.DedupKey, stQueued, stLeased,
		).Scan(&id)
		if err == nil {
			return id, nil
		}
		if err != sql.ErrNoRows {
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
		if err := q.reapInTx(ctx, tx, queue, now); err != nil {
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
		for _, c := range cands {
			newEpoch := c.epoch + 1
			newAttempts := c.attempts + 1
			if _, err := tx.ExecContext(ctx, q.s.rebind(
				`UPDATE state_queue SET state = ?, epoch = ?, attempts = ?, expiry = ? WHERE id = ?`),
				stLeased, newEpoch, newAttempts, expiry, c.id,
			); err != nil {
				return err
			}
			out = append(out, statestore.LeasedMessage{
				ID:       c.id,
				Receipt:  encodeReceipt(c.id, newEpoch),
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

// reapInTx is reapExpired within an existing transaction.
func (q *queueStore) reapInTx(ctx context.Context, tx *sql.Tx, queue string, now int64) error {
	if _, err := tx.ExecContext(ctx, q.s.rebind(
		`UPDATE state_queue SET state = ?, reason = ?, died_at = ?, dedup_key = NULL
		 WHERE queue = ? AND state = ? AND expiry <= ? AND attempts >= ?`),
		stDead, "retries exhausted (lease expired)", now, queue, stLeased, now, q.s.maxAttempts,
	); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, q.s.rebind(
		`UPDATE state_queue SET state = ?, visible_at = ?
		 WHERE queue = ? AND state = ? AND expiry <= ? AND attempts < ?`),
		stQueued, now, queue, stLeased, now, q.s.maxAttempts,
	)
	return err
}

// Ack implements statestore.Queue.
func (q *queueStore) Ack(ctx context.Context, receipt string) error {
	id, epoch, ok := decodeReceipt(receipt)
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
	id, epoch, ok := decodeReceipt(receipt)
	if !ok {
		return statestore.ErrInvalidReceipt
	}
	now := nowNanos()
	var affected int64
	err := q.s.inTx(ctx, func(tx *sql.Tx) error {
		dead, err := tx.ExecContext(ctx, q.s.rebind(
			`UPDATE state_queue SET state = ?, reason = ?, died_at = ?, dedup_key = NULL
			 WHERE id = ? AND state = ? AND epoch = ? AND attempts >= ?`),
			stDead, "retries exhausted", now, id, stLeased, epoch, q.s.maxAttempts,
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
		a, _ := dead.RowsAffected()
		b, _ := requeue.RowsAffected()
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
	id, epoch, ok := decodeReceipt(receipt)
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
	if err := q.reapExpired(ctx, queue, nowNanos()); err != nil {
		return nil, err
	}
	limit := page.Limit
	if limit <= 0 {
		limit = 1000
	}
	rows, err := q.s.query(ctx,
		`SELECT id, body, reason, attempts, enqueued_at, died_at FROM state_queue
		 WHERE queue = ? AND state = ? AND id > ? ORDER BY id LIMIT ?`,
		queue, stDead, page.Token, limit,
	)
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

// ConservationStats implements statestore.ConservationReporter (invariant T1).
func (s *Store) ConservationStats() statestore.ConservationStats {
	var st statestore.ConservationStats
	rows, err := s.db.QueryContext(context.Background(), `SELECT state, COUNT(*) FROM state_queue GROUP BY state`)
	if err != nil {
		return st
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			state string
			count int64
		)
		if err := rows.Scan(&state, &count); err != nil {
			return st
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
	return st
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/fission/fission/pkg/statestore"
)

type eventLog struct{ s *Store }

// Append implements statestore.EventLog with optimistic concurrency on the head
// sequence (invariant E1): it succeeds only when expectedSeq equals the stream's
// current head, tracked in state_streams so Trim never moves the append point.
func (e *eventLog) Append(ctx context.Context, stream string, expectedSeq int64, events []statestore.Event) (int64, error) {
	var head int64
	err := e.s.inTx(ctx, func(tx *sql.Tx) error {
		var cur int64
		qerr := tx.QueryRowContext(ctx, e.s.rebind(`SELECT head FROM state_streams WHERE stream = ?`), stream).Scan(&cur)
		if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
			return qerr
		}
		if cur != expectedSeq {
			head = cur
			return statestore.ErrVersionConflict
		}
		now := nowNanos()
		for _, ev := range events {
			cur++
			if _, ierr := tx.ExecContext(ctx, e.s.rebind(
				`INSERT INTO state_events (stream, seq, type, payload, at) VALUES (?, ?, ?, ?, ?)`),
				stream, cur, ev.Type, ev.Payload, now,
			); ierr != nil {
				return ierr
			}
		}
		if _, uerr := tx.ExecContext(ctx, e.s.rebind(
			`INSERT INTO state_streams (stream, head) VALUES (?, ?)
			 ON CONFLICT (stream) DO UPDATE SET head = excluded.head`),
			stream, cur,
		); uerr != nil {
			return uerr
		}
		head = cur
		return nil
	})
	return head, err
}

// Read implements statestore.EventLog: up to limit events with seq > fromSeq.
func (e *eventLog) Read(ctx context.Context, stream string, fromSeq int64, limit int) ([]statestore.Event, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := e.s.query(ctx,
		`SELECT seq, type, payload, at FROM state_events WHERE stream = ? AND seq > ? ORDER BY seq LIMIT ?`,
		stream, fromSeq, limit,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []statestore.Event
	for rows.Next() {
		var (
			ev  statestore.Event
			at  int64
			pay []byte
		)
		if err := rows.Scan(&ev.Seq, &ev.Type, &pay, &at); err != nil {
			return nil, err
		}
		ev.Payload = pay
		ev.At = unixNanos(at)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Trim implements statestore.EventLog: drop events with seq < belowSeq.
func (e *eventLog) Trim(ctx context.Context, stream string, belowSeq int64) error {
	_, err := e.s.exec(ctx, `DELETE FROM state_events WHERE stream = ? AND seq < ?`, stream, belowSeq)
	return err
}

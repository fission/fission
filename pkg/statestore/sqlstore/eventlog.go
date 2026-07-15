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
//
// The head is advanced by a single atomic UPDATE ... WHERE head = expectedSeq,
// which is the compare-and-swap: under Postgres two racing appenders serialize on
// the row lock and the loser's WHERE re-check fails, so exactly one writer
// reserves the [expectedSeq+1, expectedSeq+N] slots (the events then insert with
// no primary-key contention) and the loser gets ErrVersionConflict — never a raw
// unique-violation a consumer wouldn't recognize.
//
// expectedSeq = AppendAny drops the WHERE head = ? re-check (RFC-0027 topic
// publishers): the UPDATE is then an unconditional atomic increment, racing
// appenders serialize on the same row lock, and EVERY writer reserves its own
// disjoint slot range — no conflict, no client retry loop. The reserved base is
// read back inside the transaction (own-write visibility), so the insert seqs
// are exact under concurrency.
func (e *eventLog) Append(ctx context.Context, stream string, expectedSeq int64, events []statestore.Event) (int64, error) {
	var head int64
	err := e.s.inTx(ctx, func(tx *sql.Tx) error {
		// Ensure the stream row exists (idempotent), then advance its head.
		if _, ierr := tx.ExecContext(ctx, e.s.rebind(
			`INSERT INTO state_streams (stream, head) VALUES (?, 0) ON CONFLICT (stream) DO NOTHING`),
			stream,
		); ierr != nil {
			return ierr
		}
		var base int64
		if expectedSeq == statestore.AppendAny {
			if _, uerr := tx.ExecContext(ctx, e.s.rebind(
				`UPDATE state_streams SET head = head + ? WHERE stream = ?`),
				int64(len(events)), stream,
			); uerr != nil {
				return uerr
			}
			if serr := tx.QueryRowContext(ctx, e.s.rebind(
				`SELECT head FROM state_streams WHERE stream = ?`), stream).Scan(&head); serr != nil {
				return serr
			}
			base = head - int64(len(events))
		} else {
			res, uerr := tx.ExecContext(ctx, e.s.rebind(
				`UPDATE state_streams SET head = head + ? WHERE stream = ? AND head = ?`),
				int64(len(events)), stream, expectedSeq,
			)
			if uerr != nil {
				return uerr
			}
			n, aerr := res.RowsAffected()
			if aerr != nil {
				return aerr
			}
			if n == 0 {
				// CAS lost: report the current head so the caller can re-read.
				if serr := tx.QueryRowContext(ctx, e.s.rebind(`SELECT head FROM state_streams WHERE stream = ?`), stream).Scan(&head); serr != nil {
					return serr
				}
				return statestore.ErrVersionConflict
			}
			base = expectedSeq
			head = expectedSeq + int64(len(events))
		}
		now := nowNanos()
		seq := base
		insertSQL := e.s.rebind(`INSERT INTO state_events (stream, seq, type, payload, at) VALUES (?, ?, ?, ?, ?)`)
		for _, ev := range events {
			seq++
			if _, ierr := tx.ExecContext(ctx, insertSQL, stream, seq, ev.Type, ev.Payload, now); ierr != nil {
				return ierr
			}
		}
		return nil
	})
	return head, err
}

// Head implements statestore.EventLog: the stream's current head sequence, 0 for
// an absent stream, with no side effects (it does not create the stream row).
func (e *eventLog) Head(ctx context.Context, stream string) (int64, error) {
	var head int64
	err := e.s.queryRow(ctx, `SELECT head FROM state_streams WHERE stream = ?`, stream).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return head, nil
}

// Read implements statestore.EventLog: up to limit events with seq > fromSeq, in
// order. limit <= 0 returns all matching events (parity with the memory driver).
func (e *eventLog) Read(ctx context.Context, stream string, fromSeq int64, limit int) ([]statestore.Event, error) {
	query := `SELECT seq, type, payload, at FROM state_events WHERE stream = ? AND seq > ? ORDER BY seq`
	args := []any{stream, fromSeq}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := e.s.query(ctx, query, args...)
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

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"context"
	"fmt"
)

// migration is one ordered, additive schema step. The version is recorded so a
// migration runs at most once.
type migration struct {
	version int
	stmts   []string
}

// migrations returns the ordered schema for the dialect. DDL is additive only;
// never edit an existing migration — append a new one.
//
// Times are stored as INT64 unix-nanoseconds (portable, timezone-free), NULL
// meaning "unset". The queue uses a single table with an explicit state column
// (queued/leased/acked/dead) so its observable behavior matches the memory
// driver (the executable spec) one-for-one; state_streams tracks each event
// stream's head separately from its rows so Trim never moves the append point.
func (d Dialect) migrations() []migration {
	blob, i64 := d.BlobType, d.IntType
	return []migration{
		{
			version: 1,
			stmts: []string{
				fmt.Sprintf(`CREATE TABLE IF NOT EXISTS state_kv (
					namespace  TEXT   NOT NULL,
					owner      TEXT   NOT NULL,
					keyspace   TEXT   NOT NULL,
					key        TEXT   NOT NULL,
					value      %s     NOT NULL,
					version    %s     NOT NULL,
					expires_at %s,
					PRIMARY KEY (namespace, owner, keyspace, key)
				)`, blob, i64, i64),
				fmt.Sprintf(`CREATE TABLE IF NOT EXISTS state_streams (
					stream TEXT NOT NULL PRIMARY KEY,
					head   %s   NOT NULL
				)`, i64),
				fmt.Sprintf(`CREATE TABLE IF NOT EXISTS state_events (
					stream  TEXT NOT NULL,
					seq     %s   NOT NULL,
					type    TEXT NOT NULL,
					payload %s,
					at      %s   NOT NULL,
					PRIMARY KEY (stream, seq)
				)`, i64, blob, i64),
				fmt.Sprintf(`CREATE TABLE IF NOT EXISTS state_queue (
					id          TEXT NOT NULL PRIMARY KEY,
					queue       TEXT NOT NULL,
					body        %s,
					state       TEXT NOT NULL,
					visible_at  %s   NOT NULL,
					expiry      %s,
					attempts    %s   NOT NULL DEFAULT 0,
					epoch       %s   NOT NULL DEFAULT 0,
					dedup_key   TEXT,
					reason      TEXT,
					enqueued_at %s   NOT NULL,
					died_at     %s
				)`, blob, i64, i64, i64, i64, i64, i64),
				`CREATE INDEX IF NOT EXISTS idx_state_queue_lease ON state_queue (queue, state, visible_at)`,
				`CREATE INDEX IF NOT EXISTS idx_state_queue_dedup ON state_queue (queue, dedup_key)`,
			},
		},
	}
}

// migrate applies any not-yet-applied migrations. On Postgres the dialect's
// advisory lock serializes concurrent starters; on SQLite the single writer
// makes that unnecessary.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS statestore_migrations (version %s NOT NULL PRIMARY KEY, applied_at %s NOT NULL)`,
		s.dialect.IntType, s.dialect.IntType,
	)); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if s.dialect.AdvisoryLock != nil {
		if err := s.dialect.AdvisoryLock(ctx, tx); err != nil {
			return err
		}
	}

	for _, m := range s.dialect.migrations() {
		var exists int
		if err := tx.QueryRowContext(ctx, s.rebind(`SELECT COUNT(*) FROM statestore_migrations WHERE version = ?`), m.version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		for _, stmt := range m.stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO statestore_migrations (version, applied_at) VALUES (?, ?)`), m.version, nowNanos()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

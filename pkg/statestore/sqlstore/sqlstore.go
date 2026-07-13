// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package sqlstore is the shared SQL implementation of the statestore
// capabilities, used by the Postgres and SQLite drivers. The two differ only in
// a small Dialect (placeholder style, column types, and the lease row-lock
// clause) and their connection setup; all the CAS, append-CAS, and
// epoch-guarded lease/settle logic lives here once and is verified once by the
// shared conformance suite.
package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/fission/fission/pkg/statestore"
)

// Dialect captures the handful of SQL differences between backends.
type Dialect struct {
	// Name is "postgres" or "sqlite".
	Name string
	// BlobType is the column type for opaque bytes (BYTEA / BLOB).
	BlobType string
	// IntType is the column type for 64-bit integers (BIGINT / INTEGER).
	IntType string
	// LockClause is appended to the lease SELECT to skip contended rows
	// ("FOR UPDATE SKIP LOCKED" on Postgres; empty on single-writer SQLite).
	LockClause string
	// Collate is appended to key/id comparisons and ORDER BY so ordering and
	// prefix cursors are byte-exact (matching the memory driver) regardless of the
	// database's locale: ` COLLATE "C"` on Postgres, empty on SQLite (whose default
	// BINARY collation is already byte order).
	Collate string
	// AdvisoryLock, if non-nil, is run before migrations to serialize concurrent
	// starters (Postgres pg_advisory_xact_lock); nil on single-writer SQLite.
	AdvisoryLock func(ctx context.Context, tx *sql.Tx) error
}

// defaultMaxAttempts is the queue attempt budget before a Nack or an exhausted
// lease expiry dead-letters a message (RFC-0024's default retry policy).
const defaultMaxAttempts = 3

// Store is the shared SQL-backed Capabilities.
type Store struct {
	db          *sql.DB
	dialect     Dialect
	maxAttempts int
}

// Open runs migrations and returns a Store over db. Callers (the postgres/sqlite
// driver packages) own opening and configuring db (pool size, pragmas).
func Open(ctx context.Context, db *sql.DB, d Dialect) (*Store, error) {
	s := &Store{db: db, dialect: d, maxAttempts: defaultMaxAttempts}
	if err := s.migrate(ctx); err != nil {
		return nil, fmt.Errorf("statestore/sqlstore: migrate: %w", err)
	}
	return s, nil
}

// SetMaxAttempts overrides the queue attempt budget (for tests / per-deployment
// tuning). n <= 0 is ignored.
func (s *Store) SetMaxAttempts(n int) {
	if n > 0 {
		s.maxAttempts = n
	}
}

func (s *Store) KV() (statestore.KVStore, error)        { return &kvStore{s}, nil }
func (s *Store) EventLog() (statestore.EventLog, error) { return &eventLog{s}, nil }
func (s *Store) Queue() (statestore.Queue, error)       { return &queueStore{s}, nil }

// Ping reports whether the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close closes the underlying pool.
func (s *Store) Close() error { return s.db.Close() }

// rebind translates the ?-placeholders the shared queries are written with into
// the dialect's style ($1, $2, … on Postgres; unchanged on SQLite).
func (s *Store) rebind(query string) string {
	if s.dialect.Name != "postgres" {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.rebind(query), args...)
}

func (s *Store) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.rebind(query), args...)
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.rebind(query), args...)
}

// inTx runs fn inside a transaction, committing on nil and rolling back on error.
func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

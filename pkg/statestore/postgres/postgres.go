// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package postgres is the reference statestore driver for the external
// (user-managed) deployment mode. It shares all its logic with the embedded
// SQLite driver via pkg/statestore/sqlstore; only the connection setup and
// dialect (BYTEA/BIGINT columns, SELECT ... FOR UPDATE SKIP LOCKED leases, and
// an advisory lock around migrations) differ.
package postgres

import (
	"context"
	"database/sql"
	"errors"

	// jackc/pgx/v5/stdlib registers the "pgx" database/sql driver.
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/sqlstore"
)

// migrationLockKey is an arbitrary fixed key for pg_advisory_xact_lock, so any
// component may run the migration set and concurrent starters serialize rather
// than racing (resolves RFC-0021 open question 3).
const migrationLockKey int64 = 0x1502_0021 // "statestore RFC-0021"

func init() {
	statestore.Register("postgres", func(ctx context.Context, c statestore.Config) (statestore.Capabilities, error) {
		return New(ctx, c.DSN)
	})
}

var dialect = sqlstore.Dialect{
	Name:       "postgres",
	BlobType:   "BYTEA",
	IntType:    "BIGINT",
	LockClause: " FOR UPDATE SKIP LOCKED",
	AdvisoryLock: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockKey)
		return err
	},
}

// New opens a Postgres-backed statestore at dsn (a libpq/pgx connection string).
func New(ctx context.Context, dsn string) (statestore.Capabilities, error) {
	if dsn == "" {
		return nil, errors.New("statestore/postgres: empty DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	// Conservative pool defaults; a real deployment tunes these against its
	// Postgres. (Env-driven tuning can be added when a consumer needs it.)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	store, err := sqlstore.Open(ctx, db, dialect)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

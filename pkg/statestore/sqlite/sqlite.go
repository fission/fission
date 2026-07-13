// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package sqlite is the embedded-mode statestore driver: a pure-Go
// (modernc.org/sqlite, no cgo) SQL store backing the single-replica embedded
// store pod. It shares all its logic with the Postgres driver via
// pkg/statestore/sqlstore; only the connection setup and dialect differ.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	// modernc.org/sqlite registers the "sqlite" database/sql driver.
	_ "modernc.org/sqlite"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/sqlstore"
)

func init() {
	statestore.Register("sqlite", func(ctx context.Context, c statestore.Config) (statestore.Capabilities, error) {
		return New(ctx, c.DSN)
	})
}

// dialect is single-writer: no row-lock clause (one connection serializes
// writes) and no migration advisory lock (one owner of the file).
var dialect = sqlstore.Dialect{
	Name:       "sqlite",
	BlobType:   "BLOB",
	IntType:    "INTEGER",
	LockClause: "",
}

// New opens an embedded SQLite statestore at dsn (a file path; empty means an
// ephemeral in-memory database). Access is serialized to one connection, giving
// BEGIN IMMEDIATE single-writer semantics without SKIP LOCKED.
func New(ctx context.Context, dsn string) (statestore.Capabilities, error) {
	if dsn == "" {
		dsn = ":memory:"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("statestore/sqlite: open: %w", err)
	}
	// Single writer: one connection serializes all access (so the queue lease
	// needs no SKIP LOCKED) and keeps an in-memory database alive for the pool's
	// lifetime.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("statestore/sqlite: pragma %q: %w", pragma, err)
		}
	}

	store, err := sqlstore.Open(ctx, db, dialect)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/statestoretest"
)

var sqliteDialect = Dialect{Name: "sqlite", BlobType: "BLOB", IntType: "INTEGER"}

// openSQLite mirrors pkg/statestore/sqlite.New's connection setup exactly.
//
// Faithfulness matters more than brevity here: the driver's List semantics
// depend on case_sensitive_like, and its single-writer serialization on the
// connection limits. A harness that skipped them would exercise a store the
// product never runs, and would have reported a conformance failure that says
// nothing about migrations.
func openSQLite(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA case_sensitive_like=ON",
	} {
		_, err := db.ExecContext(t.Context(), pragma)
		require.NoErrorf(t, err, "pragma %q", pragma)
	}
	return db
}

// openAt returns a sqlite DB migrated to exactly `through`, simulating a
// cluster running that release.
//
// It applies the migration statements directly and records them in the
// bookkeeping table, which is what a real install at that version looks like:
// migrate() then sees them as applied and only runs what came after.
func openAt(t *testing.T, dsn string, through int) *sql.DB {
	t.Helper()
	db := openSQLite(t, dsn)
	ctx := t.Context()

	_, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS statestore_migrations (version INTEGER NOT NULL PRIMARY KEY, applied_at INTEGER NOT NULL)`)
	require.NoError(t, err)

	for _, m := range sqliteDialect.migrations() {
		if m.version > through {
			break
		}
		for _, stmt := range m.stmts {
			_, err := db.ExecContext(ctx, stmt)
			require.NoErrorf(t, err, "applying migration %d", m.version)
		}
		_, err := db.ExecContext(ctx,
			`INSERT INTO statestore_migrations (version, applied_at) VALUES (?, 0)`, m.version)
		require.NoError(t, err)
	}
	return db
}

func appliedVersions(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT version FROM statestore_migrations ORDER BY version`)
	require.NoError(t, err)
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		require.NoError(t, rows.Scan(&v))
		out = append(out, v)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestUpgradeFromEachPriorSchema is RFC-0028 phase 4's conformance half: a
// store that was migrated by an OLDER release must come up and behave
// correctly after the current release finishes migrating it.
//
// The static guards (migrate_contract_test.go) prove the DDL is additive by
// inspection. This proves the result actually works: for every prior schema
// version, stage a database at that version, open it with the current code —
// which applies whatever is missing — and run the full driver conformance
// suite against it.
//
// Without this, "additive only" is a property of the SQL text rather than of
// the running system, and a migration could satisfy the regexes while still
// leaving the driver unable to read what it wrote.
func TestUpgradeFromEachPriorSchema(t *testing.T) {
	t.Parallel()
	all := sqliteDialect.migrations()
	require.NotEmpty(t, all)
	latest := all[len(all)-1].version

	for through := 1; through < latest; through++ {
		t.Run("from_v"+string(rune('0'+through)), func(t *testing.T) {
			t.Parallel()
			// One STAGED database per factory call, not one shared across the
			// suite: the conformance suite assumes a fresh store per call and
			// its cases collide otherwise.
			statestoretest.RunConformance(t, func(t *testing.T) statestore.Capabilities {
				dsn := filepath.Join(t.TempDir(), "state.db")

				staged := openAt(t, dsn, through)
				require.Len(t, appliedVersions(t, staged), through,
					"the staged database must look like a real install at that version")
				require.NoError(t, staged.Close())

				// The upgrade: current code opens a database an older release left.
				db := openSQLite(t, dsn)
				t.Cleanup(func() { _ = db.Close() })

				store, err := Open(t.Context(), db, sqliteDialect)
				require.NoError(t, err, "opening a database staged at v%d must migrate it, not fail", through)
				return store
			})
		})
	}
}

// TestUpgradeAppliesOnlyWhatIsMissing pins that migrate() is incremental
// rather than re-running everything.
//
// Re-running an applied migration happens to be harmless today because every
// statement is CREATE TABLE IF NOT EXISTS — but that is a property of the
// current DDL, not of the mechanism, and the first ALTER would end that. The
// bookkeeping table is what makes it safe, so it is worth asserting directly.
func TestUpgradeAppliesOnlyWhatIsMissing(t *testing.T) {
	t.Parallel()
	all := sqliteDialect.migrations()
	latest := all[len(all)-1].version
	dsn := filepath.Join(t.TempDir(), "state.db")

	staged := openAt(t, dsn, 1)
	require.Equal(t, []int{1}, appliedVersions(t, staged))
	require.NoError(t, staged.Close())

	db := openSQLite(t, dsn)
	defer db.Close()

	_, err := Open(t.Context(), db, sqliteDialect)
	require.NoError(t, err)

	got := appliedVersions(t, db)
	assert.Len(t, got, latest, "every migration must be recorded after the upgrade")
	for i, v := range got {
		assert.Equal(t, i+1, v, "recorded versions must be 1..N in order")
	}
}

// TestUpgradeIsIdempotent: the hook and the control plane both open the store
// on every start, so a second open must be a no-op rather than re-running DDL.
func TestUpgradeIsIdempotent(t *testing.T) {
	t.Parallel()
	dsn := filepath.Join(t.TempDir(), "state.db")

	for range 3 {
		db := openSQLite(t, dsn)
		_, err := Open(context.Background(), db, sqliteDialect)
		require.NoError(t, err, "re-opening an already-migrated database must succeed")
		require.NoError(t, db.Close())
	}

	db := openSQLite(t, dsn)
	defer db.Close()
	got := appliedVersions(t, db)
	assert.Len(t, got, len(sqliteDialect.migrations()),
		"repeated opens must not duplicate bookkeeping rows")
}

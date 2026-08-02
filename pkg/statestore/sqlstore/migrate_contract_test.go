// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RFC-0028's state contract: control-plane components at N and N-1 coexist
// during a rolling upgrade, so an N-1 driver must keep working against a schema
// an N driver has already migrated. That holds only while schema change is
// EXPAND-then-contract — additive now, removal a release later, never both at
// once.
//
// migrate.go states both rules in a comment ("DDL is additive only; never edit
// an existing migration — append a new one") and nothing enforced either. These
// tests do.

// testDialects mirrors the two real dialects (pkg/statestore/sqlite,
// pkg/statestore/postgres) without importing them, which would be a cycle.
func testDialects() []Dialect {
	return []Dialect{
		{Name: "sqlite", BlobType: "BLOB", IntType: "INTEGER"},
		{Name: "postgres", BlobType: "BYTEA", IntType: "BIGINT", Collate: ` COLLATE "C"`},
	}
}

// destructiveDDL matches statements that remove or repurpose existing schema.
// Each breaks an N-1 driver still reading the old shape mid-upgrade.
var destructiveDDL = []struct {
	name string
	re   *regexp.Regexp
}{
	{"DROP TABLE", regexp.MustCompile(`(?is)\bDROP\s+TABLE\b`)},
	{"DROP COLUMN", regexp.MustCompile(`(?is)\bDROP\s+COLUMN\b`)},
	{"DROP INDEX", regexp.MustCompile(`(?is)\bDROP\s+INDEX\b`)},
	{"RENAME", regexp.MustCompile(`(?is)\bRENAME\s+(TABLE|COLUMN|TO)\b`)},
	// A type change rewrites what an old driver reads; expand/contract means a
	// NEW column plus a later removal, never an in-place alter.
	{"ALTER COLUMN TYPE", regexp.MustCompile(`(?is)\bALTER\s+(TABLE\s+\S+\s+)?(ALTER\s+)?COLUMN\b.*\bTYPE\b`)},
	{"TRUNCATE", regexp.MustCompile(`(?is)\bTRUNCATE\b`)},
}

var (
	addColumnRe = regexp.MustCompile(`(?is)\bADD\s+COLUMN\b`)
	notNullRe   = regexp.MustCompile(`(?is)\bNOT\s+NULL\b`)
	defaultRe   = regexp.MustCompile(`(?is)\bDEFAULT\b`)
)

func TestMigrationsAreAdditiveOnly(t *testing.T) {
	t.Parallel()
	for _, d := range testDialects() {
		t.Run(d.Name, func(t *testing.T) {
			t.Parallel()
			for _, m := range d.migrations() {
				for i, stmt := range m.stmts {
					for _, bad := range destructiveDDL {
						assert.Falsef(t, bad.re.MatchString(stmt),
							"migration %d statement %d contains %s, which breaks an N-1 driver reading the old shape "+
								"mid-upgrade. Expand/contract: add the new shape now, remove the old one a release later.\n%s",
							m.version, i, bad.name, stmt)
					}
					// Go's RE2 has no negative lookahead, so this one is a
					// two-step check rather than a pattern: a NOT NULL column
					// added without a DEFAULT breaks an N-1 driver that does
					// not know to populate it.
					if addColumnRe.MatchString(stmt) && notNullRe.MatchString(stmt) && !defaultRe.MatchString(stmt) {
						t.Errorf("migration %d statement %d adds a NOT NULL column with no DEFAULT; "+
							"an N-1 driver inserting without that column would fail.\n%s", m.version, i, stmt)
					}
				}
			}
		})
	}
}

func TestMigrationVersionsAreOrderedAndContiguous(t *testing.T) {
	t.Parallel()
	for _, d := range testDialects() {
		t.Run(d.Name, func(t *testing.T) {
			t.Parallel()
			ms := d.migrations()
			require.NotEmpty(t, ms)
			for i, m := range ms {
				assert.Equalf(t, i+1, m.version,
					"migrations must be 1..N in order with no gaps: index %d has version %d. "+
						"The applied-version bookkeeping compares by number, so a gap or a reorder silently skips one.",
					i, m.version)
				assert.NotEmptyf(t, m.stmts, "migration %d has no statements", m.version)
			}
		})
	}
}

// TestMigrationsAreAppendOnly is the one that catches the mistake the comment
// warns about but nothing prevented: EDITING an already-released migration.
//
// A cluster that ran version N will never re-run it — the bookkeeping table
// records it as applied — so an edit reaches only fresh installs. The two then
// diverge permanently, and nothing reports it: the edited install and the
// existing one both believe they are at the same schema version.
//
// Adding a NEW migration is expected and only needs a line here. Changing a
// checksum below means an existing migration was altered; append instead.
func TestMigrationsAreAppendOnly(t *testing.T) {
	t.Parallel()

	// version -> sha256 of the joined statements, per dialect.
	golden := map[string]map[int]string{
		"sqlite": {
			1: "sha256:2e10ff688eb25ac73abba0094027304608f6524d6272f54d19d7d7f63b53e6a0",
			2: "sha256:6afe6f1cc5f6aac71cc82657e8962d0a2e92a408abbb896e9e939f7a0f5fc43d",
		},
		"postgres": {
			1: "sha256:4c3072401bd6d5d60aa52941edae910fe82a7ebba8ca2ceee526a78e37cfa840",
			// Identical to sqlite's: migration 2 uses no dialect-specific types.
			2: "sha256:6afe6f1cc5f6aac71cc82657e8962d0a2e92a408abbb896e9e939f7a0f5fc43d",
		},
	}

	for _, d := range testDialects() {
		t.Run(d.Name, func(t *testing.T) {
			t.Parallel()
			want := golden[d.Name]
			for _, m := range d.migrations() {
				sum := migrationChecksum(m)
				pinned, ok := want[m.version]
				if !ok {
					t.Fatalf("migration %d is not pinned. Adding a migration is fine — add its checksum to the golden map:\n  %d: %q,", m.version, m.version, sum)
				}
				if pinned == "" {
					// Bootstrapping: report the value to paste in rather than
					// silently accepting whatever is there.
					t.Logf("migration %d checksum (paste into the golden map): %q", m.version, sum)
					continue
				}
				assert.Equalf(t, pinned, sum,
					"migration %d was EDITED. A cluster that already applied it will never re-run it, so the edit "+
						"reaches only fresh installs and the two schemas diverge permanently with nothing reporting it. "+
						"Append a new migration instead.", m.version)
			}
		})
	}
}

func migrationChecksum(m migration) string {
	sum := sha256.Sum256([]byte(strings.Join(m.stmts, "\n")))
	return fmt.Sprintf("sha256:%x", sum)
}

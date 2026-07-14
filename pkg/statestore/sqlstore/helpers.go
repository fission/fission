// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"database/sql"
	"strings"
	"time"
)

// nowNanos is the current wall clock as unix nanoseconds — the on-disk time unit.
func nowNanos() int64 { return time.Now().UnixNano() }

// unixNanos converts stored unix-nanoseconds back to a time.Time.
func unixNanos(n int64) time.Time { return time.Unix(0, n) }

// nullableTime converts a nullable unix-nanos column to a time.Time (zero when
// the column is NULL).
func nullableTime(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return time.Unix(0, n.Int64)
}

// nullNanos wraps an optional unix-nanos timestamp for a nullable column.
func nullNanos(v int64, valid bool) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: valid}
}

// expiredAt reports whether a nullable expiry has elapsed by now (inclusive of
// the boundary, so expiry is exact on read — invariant K2).
func expiredAt(expires sql.NullInt64, now int64) bool {
	return expires.Valid && now >= expires.Int64
}

// escapeLikePrefix turns a literal prefix into a LIKE pattern (with ESCAPE '\'),
// so keys containing % or _ don't act as wildcards.
func escapeLikePrefix(prefix string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(prefix) + "%"
}

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

type kvStore struct{ s *Store }

// Get implements statestore.KVStore.
func (k *kvStore) Get(ctx context.Context, sc statestore.Scope, key string) (statestore.Value, error) {
	var (
		data    []byte
		version int64
		expires sql.NullInt64
	)
	err := k.s.queryRow(ctx,
		`SELECT value, version, expires_at FROM state_kv WHERE namespace = ? AND owner = ? AND keyspace = ? AND key = ?`,
		sc.Namespace, sc.Owner, sc.Keyspace, key,
	).Scan(&data, &version, &expires)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return statestore.Value{}, statestore.ErrNotFound
	case err != nil:
		return statestore.Value{}, err
	case expiredAt(expires, nowNanos()):
		return statestore.Value{}, statestore.ErrNotFound
	}
	return statestore.Value{Data: data, Version: version}, nil
}

// Set implements statestore.KVStore with the CAS-on-version semantics (an absent
// or expired key is version 0).
//
// Each case is a SINGLE atomic statement so there is no read-then-write window:
// under Postgres READ COMMITTED a concurrent writer would otherwise pass a stale
// version check and lose the update (SQLite's single writer hides it, but the
// contract must hold on both). The row lock the UPDATE/upsert takes serializes
// concurrent CAS on the same key, and the WHERE re-check on the committed row is
// what makes CAS linearizable (invariant K1).
func (k *kvStore) Set(ctx context.Context, sc statestore.Scope, key string, val []byte, o statestore.SetOptions) error {
	now := nowNanos()
	var expires sql.NullInt64
	if o.TTL > 0 {
		expires = nullNanos(now+o.TTL.Nanoseconds(), true)
	}

	switch {
	case o.IfVersion == nil:
		// Unconditional upsert. An expired existing row counts as absent, so its
		// version resets to 1 (parity with the memory driver).
		_, err := k.s.exec(ctx,
			`INSERT INTO state_kv (namespace, owner, keyspace, key, value, version, expires_at)
			 VALUES (?, ?, ?, ?, ?, 1, ?)
			 ON CONFLICT (namespace, owner, keyspace, key) DO UPDATE SET
			   value = excluded.value,
			   version = CASE WHEN state_kv.expires_at IS NOT NULL AND state_kv.expires_at <= ?
			                  THEN 1 ELSE state_kv.version + 1 END,
			   expires_at = excluded.expires_at`,
			sc.Namespace, sc.Owner, sc.Keyspace, key, val, expires, now,
		)
		return err

	case *o.IfVersion == 0:
		// Create-only: succeed if the key is absent or expired; conflict if a live
		// row exists.
		res, err := k.s.exec(ctx,
			`INSERT INTO state_kv (namespace, owner, keyspace, key, value, version, expires_at)
			 VALUES (?, ?, ?, ?, ?, 1, ?)
			 ON CONFLICT (namespace, owner, keyspace, key) DO UPDATE SET
			   value = excluded.value, version = 1, expires_at = excluded.expires_at
			 WHERE state_kv.expires_at IS NOT NULL AND state_kv.expires_at <= ?`,
			sc.Namespace, sc.Owner, sc.Keyspace, key, val, expires, now,
		)
		return conflictIfNoRows(res, err)

	default:
		// CAS on *o.IfVersion: match a live row at exactly that version.
		res, err := k.s.exec(ctx,
			`UPDATE state_kv SET value = ?, version = version + 1, expires_at = ?
			 WHERE namespace = ? AND owner = ? AND keyspace = ? AND key = ?
			   AND version = ? AND (expires_at IS NULL OR expires_at > ?)`,
			val, expires, sc.Namespace, sc.Owner, sc.Keyspace, key, *o.IfVersion, now,
		)
		return conflictIfNoRows(res, err)
	}
}

// conflictIfNoRows maps an atomic write that changed no rows to
// ErrVersionConflict (the CAS/create-only check failed on the committed row).
func conflictIfNoRows(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return statestore.ErrVersionConflict
	}
	return nil
}

// Delete implements statestore.KVStore. ifVersion <= 0 deletes unconditionally
// (idempotent for an absent key); a positive ifVersion is an atomic CAS delete
// (a live row at exactly that version), so a concurrent writer cannot slip
// between a version check and the delete.
func (k *kvStore) Delete(ctx context.Context, sc statestore.Scope, key string, ifVersion int64) error {
	if ifVersion > 0 {
		res, err := k.s.exec(ctx,
			`DELETE FROM state_kv
			 WHERE namespace = ? AND owner = ? AND keyspace = ? AND key = ?
			   AND version = ? AND (expires_at IS NULL OR expires_at > ?)`,
			sc.Namespace, sc.Owner, sc.Keyspace, key, ifVersion, nowNanos(),
		)
		return conflictIfNoRows(res, err)
	}
	_, err := k.s.exec(ctx,
		`DELETE FROM state_kv WHERE namespace = ? AND owner = ? AND keyspace = ? AND key = ?`,
		sc.Namespace, sc.Owner, sc.Keyspace, key,
	)
	return err
}

// List implements statestore.KVStore: lexicographic (byte-exact) keys under
// prefix, paginated by page.Token (the last key returned), excluding expired
// keys. page.Limit <= 0 returns all matching keys (parity with the memory
// driver). The Collate clause makes ordering byte-exact regardless of DB locale.
func (k *kvStore) List(ctx context.Context, sc statestore.Scope, prefix string, page statestore.Page) (statestore.KeyPage, error) {
	col := k.s.dialect.Collate
	query := `SELECT key FROM state_kv
		 WHERE namespace = ? AND owner = ? AND keyspace = ? AND key LIKE ? ESCAPE '\'
		   AND key > ?` + col + ` AND (expires_at IS NULL OR expires_at > ?)
		 ORDER BY key` + col
	args := []any{sc.Namespace, sc.Owner, sc.Keyspace, escapeLikePrefix(prefix), page.Token, nowNanos()}
	if page.Limit > 0 {
		// Fetch one extra row to detect whether a further page exists.
		query += ` LIMIT ?`
		args = append(args, page.Limit+1)
	}
	rows, err := k.s.query(ctx, query, args...)
	if err != nil {
		return statestore.KeyPage{}, err
	}
	defer func() { _ = rows.Close() }()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return statestore.KeyPage{}, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return statestore.KeyPage{}, err
	}
	// A (limit+1)th row means there is a further page; its key is the cursor.
	if page.Limit > 0 && len(keys) > page.Limit {
		return statestore.KeyPage{Keys: keys[:page.Limit], Next: keys[page.Limit-1]}, nil
	}
	return statestore.KeyPage{Keys: keys}, nil
}

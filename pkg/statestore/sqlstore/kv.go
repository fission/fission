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
func (k *kvStore) Set(ctx context.Context, sc statestore.Scope, key string, val []byte, o statestore.SetOptions) error {
	return k.s.inTx(ctx, func(tx *sql.Tx) error {
		now := nowNanos()
		var (
			curVersion int64
			expires    sql.NullInt64
			exists     bool
		)
		err := tx.QueryRowContext(ctx, k.s.rebind(
			`SELECT version, expires_at FROM state_kv WHERE namespace = ? AND owner = ? AND keyspace = ? AND key = ?`),
			sc.Namespace, sc.Owner, sc.Keyspace, key,
		).Scan(&curVersion, &expires)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			exists = false
		case err != nil:
			return err
		default:
			exists = !expiredAt(expires, now)
		}

		if o.IfVersion != nil {
			effective := int64(0)
			if exists {
				effective = curVersion
			}
			if effective != *o.IfVersion {
				return statestore.ErrVersionConflict
			}
		}

		newVersion := int64(1)
		if exists {
			newVersion = curVersion + 1
		}
		var newExpires sql.NullInt64
		if o.TTL > 0 {
			newExpires = nullNanos(now+o.TTL.Nanoseconds(), true)
		}
		_, err = tx.ExecContext(ctx, k.s.rebind(
			`INSERT INTO state_kv (namespace, owner, keyspace, key, value, version, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (namespace, owner, keyspace, key)
			 DO UPDATE SET value = excluded.value, version = excluded.version, expires_at = excluded.expires_at`),
			sc.Namespace, sc.Owner, sc.Keyspace, key, val, newVersion, newExpires,
		)
		return err
	})
}

// Delete implements statestore.KVStore.
func (k *kvStore) Delete(ctx context.Context, sc statestore.Scope, key string, ifVersion int64) error {
	return k.s.inTx(ctx, func(tx *sql.Tx) error {
		if ifVersion > 0 {
			var (
				curVersion int64
				expires    sql.NullInt64
			)
			err := tx.QueryRowContext(ctx, k.s.rebind(
				`SELECT version, expires_at FROM state_kv WHERE namespace = ? AND owner = ? AND keyspace = ? AND key = ?`),
				sc.Namespace, sc.Owner, sc.Keyspace, key,
			).Scan(&curVersion, &expires)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && expiredAt(expires, nowNanos())) {
				return statestore.ErrVersionConflict
			}
			if err != nil {
				return err
			}
			if curVersion != ifVersion {
				return statestore.ErrVersionConflict
			}
		}
		_, err := tx.ExecContext(ctx, k.s.rebind(
			`DELETE FROM state_kv WHERE namespace = ? AND owner = ? AND keyspace = ? AND key = ?`),
			sc.Namespace, sc.Owner, sc.Keyspace, key,
		)
		return err
	})
}

// List implements statestore.KVStore: lexicographic keys under prefix, paginated
// by page.Token (the last key returned), excluding expired keys.
func (k *kvStore) List(ctx context.Context, sc statestore.Scope, prefix string, page statestore.Page) (statestore.KeyPage, error) {
	limit := page.Limit
	if limit <= 0 {
		limit = 1000
	}
	rows, err := k.s.query(ctx,
		`SELECT key FROM state_kv
		 WHERE namespace = ? AND owner = ? AND keyspace = ? AND key LIKE ? ESCAPE '\'
		   AND key > ? AND (expires_at IS NULL OR expires_at > ?)
		 ORDER BY key LIMIT ?`,
		sc.Namespace, sc.Owner, sc.Keyspace, escapeLikePrefix(prefix), page.Token, nowNanos(), limit+1,
	)
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
	if len(keys) > limit {
		return statestore.KeyPage{Keys: keys[:limit], Next: keys[limit-1]}, nil
	}
	return statestore.KeyPage{Keys: keys}, nil
}

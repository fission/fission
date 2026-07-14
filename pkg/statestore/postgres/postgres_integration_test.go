// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/postgres"
	"github.com/fission/fission/pkg/statestore/statestoretest"
)

// pgDSN returns a Postgres DSN: the STATESTORE_POSTGRES_TEST_DSN env var if set,
// otherwise a throwaway container started via dockertest (Docker required).
func pgDSN(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("STATESTORE_POSTGRES_TEST_DSN"); dsn != "" {
		return dsn
	}
	pool, err := dockertest.NewPool("")
	require.NoError(t, err)
	require.NoError(t, pool.Client.Ping())

	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "postgres",
		Tag:        "16-alpine",
		Env: []string{
			"POSTGRES_USER=fission",
			"POSTGRES_PASSWORD=secret",
			"POSTGRES_DB=statestore",
			"listen_addresses = '*'",
		},
	}, func(c *docker.HostConfig) {
		c.AutoRemove = true
		c.RestartPolicy = docker.RestartPolicy{Name: "no"}
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Purge(resource) })

	dsn := fmt.Sprintf("postgres://fission:secret@localhost:%s/statestore?sslmode=disable", resource.GetPort("5432/tcp"))
	pool.MaxWait = 90 * time.Second
	require.NoError(t, pool.Retry(func() error {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		return db.PingContext(context.Background())
	}))
	return dsn
}

// truncate clears the data tables for a fresh slate between conformance subtests.
func truncate(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec("TRUNCATE state_kv, state_events, state_streams, state_queue")
	require.NoError(t, err)
}

func TestConformance_Postgres(t *testing.T) {
	dsn := pgDSN(t)
	ctx := context.Background()
	factory := func(t *testing.T) statestore.Capabilities {
		caps, err := postgres.New(ctx, dsn)
		require.NoError(t, err)
		truncate(t, dsn) // fresh slate (tables exist after the first migrate)
		t.Cleanup(func() { _ = caps.Close() })
		return caps
	}
	// Postgres does real socket I/O, so it runs the time-independent conformance
	// suite (not the synctest timing suite — that runs against memory/SQLite,
	// which exercise the identical shared sqlstore timing code).
	statestoretest.RunConformance(t, factory)
	statestoretest.RunKVLinearizability(t, factory)
}

// TestPostgres_TimingSmoke verifies TTL and lease expiry against real Postgres
// with short real durations (the synctest timing suite can't run over a socket).
func TestPostgres_TimingSmoke(t *testing.T) {
	dsn := pgDSN(t)
	ctx := context.Background()
	caps, err := postgres.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = caps.Close() })
	truncate(t, dsn)

	kv, err := caps.KV()
	require.NoError(t, err)
	sc := statestore.Scope{Namespace: "ns", Owner: "function/smoke", Keyspace: "k"}
	require.NoError(t, kv.Set(ctx, sc, "ttl", []byte("v"), statestore.SetOptions{TTL: 200 * time.Millisecond}))
	_, err = kv.Get(ctx, sc, "ttl")
	require.NoError(t, err)
	time.Sleep(400 * time.Millisecond)
	_, err = kv.Get(ctx, sc, "ttl")
	require.ErrorIs(t, err, statestore.ErrNotFound, "TTL exact on read against real Postgres")

	q, err := caps.Queue()
	require.NoError(t, err)
	_, err = q.Enqueue(ctx, "smoke", statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
	require.NoError(t, err)
	l1, err := q.Lease(ctx, "smoke", 1, 200*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, l1, 1)
	time.Sleep(400 * time.Millisecond) // lease expires
	l2, err := q.Lease(ctx, "smoke", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l2, 1)
	require.ErrorIs(t, q.Ack(ctx, l1[0].Receipt), statestore.ErrInvalidReceipt, "stale-epoch settle rejected")
	require.NoError(t, q.Ack(ctx, l2[0].Receipt))
}

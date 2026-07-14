// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/client"
	"github.com/fission/fission/pkg/statestore/httpapi"
	"github.com/fission/fission/pkg/statestore/memory"
	"github.com/fission/fission/pkg/statestore/statestoretest"
)

// clientCaps builds a client wired to an in-process server over a real socket,
// so it exercises the full wire round-trip. Each call gets a fresh backing store,
// giving per-subtest isolation.
func clientCaps(t *testing.T) statestore.Capabilities {
	t.Helper()
	backing, err := memory.New()
	require.NoError(t, err)
	srv := httptest.NewServer(httpapi.NewHandler(backing))
	t.Cleanup(srv.Close)
	c := client.New(srv.URL, nil)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// The HTTP client driver must pass the same conformance suite as every other
// driver — this is what makes "consumers are identical across modes" a tested
// claim rather than a slogan. It cannot run the synctest timing suite (a real
// socket can't be bubbled); TestClient_TimingSmoke covers the wire passthrough of
// TTL and lease durations with real time.
func TestConformance_Client(t *testing.T) {
	statestoretest.RunConformance(t, clientCaps)
	statestoretest.RunKVLinearizability(t, clientCaps)
}

func TestClient_TimingSmoke(t *testing.T) {
	caps := clientCaps(t)
	ctx := t.Context()
	kv, err := caps.KV()
	require.NoError(t, err)
	sc := statestore.Scope{Namespace: "ns", Owner: "function/smoke", Keyspace: "k"}
	require.NoError(t, kv.Set(ctx, sc, "ttl", []byte("v"), statestore.SetOptions{TTL: 200 * time.Millisecond}))
	_, err = kv.Get(ctx, sc, "ttl")
	require.NoError(t, err)
	time.Sleep(400 * time.Millisecond)
	_, err = kv.Get(ctx, sc, "ttl")
	require.ErrorIs(t, err, statestore.ErrNotFound, "TTL forwarded and enforced across the wire")

	q, err := caps.Queue()
	require.NoError(t, err)
	_, err = q.Enqueue(ctx, "smoke", statestore.Message{Body: []byte("m")}, statestore.EnqueueOptions{})
	require.NoError(t, err)
	l1, err := q.Lease(ctx, "smoke", 1, 200*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, l1, 1)
	time.Sleep(400 * time.Millisecond)
	l2, err := q.Lease(ctx, "smoke", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, l2, 1)
	require.ErrorIs(t, q.Ack(ctx, l1[0].Receipt), statestore.ErrInvalidReceipt)
	require.NoError(t, q.Ack(ctx, l2[0].Receipt))
}

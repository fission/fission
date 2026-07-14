// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestoresvc_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/client"
	"github.com/fission/fission/pkg/statestore/memory"
	"github.com/fission/fission/pkg/statestore/statestoresvc"
)

// TestStatestoreHead_HMACRoundTrip starts the embedded head with an injected
// memory store and HMAC enforcement, then verifies that a signed client can use
// the capability API, an unsigned client is rejected on /v1, and /readyz is
// reachable unauthenticated.
func TestStatestoreHead_HMACRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	master := "test-master-secret-01234567890abcd"
	t.Setenv("FISSION_INTERNAL_AUTH_SECRET", master)

	mem, err := memory.New()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	g, _ := errgroup.WithContext(ctx)
	go func() {
		_ = statestoresvc.Start(ctx, nil, logr.Discard(), g, statestoresvc.Options{Listener: ln, Caps: mem})
	}()

	base := "http://" + ln.Addr().String()
	signed := client.New(base, &http.Client{
		Transport: hmacauth.ServiceSigner([]byte(master), hmacauth.ServiceStatestore, http.DefaultTransport, time.Now),
		Timeout:   5 * time.Second,
	})

	// Wait for the server to come up (readyz is bypassed from auth).
	require.Eventually(t, func() bool { return signed.Ping(ctx) == nil }, 5*time.Second, 20*time.Millisecond)

	scope := statestore.Scope{Namespace: "ns", Owner: "function/f", Keyspace: "k"}
	kv, err := signed.KV()
	require.NoError(t, err)
	require.NoError(t, kv.Set(ctx, scope, "k", []byte("v"), statestore.SetOptions{}))
	got, err := kv.Get(ctx, scope, "k")
	require.NoError(t, err)
	require.Equal(t, []byte("v"), got.Data)

	// An unsigned client is rejected on the capability API...
	unsigned := client.New(base, &http.Client{Timeout: 5 * time.Second})
	ukv, err := unsigned.KV()
	require.NoError(t, err)
	require.Error(t, ukv.Set(ctx, scope, "x", []byte("v"), statestore.SetOptions{}), "unsigned /v1 request must be rejected")
	// ...but the health probe stays reachable unauthenticated.
	require.NoError(t, unsigned.Ping(ctx))
}

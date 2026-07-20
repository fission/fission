// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statesvc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/memory"
)

var testMaster = []byte("test-master-secret")

// newTestServer builds a statesvc handler over a fresh memory driver with the
// given claimed keyspaces, returning the server and the scoped index.
func newTestServer(t *testing.T, fns map[types.NamespacedName]*fv1.StateConfig) (*httptest.Server, *FunctionIndex) {
	t.Helper()
	inner, err := memory.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })

	index := NewFunctionIndex()
	for nn, sc := range fns {
		index.Upsert(nn, sc)
	}
	scoped := statestore.NewScoped(inner, index)
	kv, err := scoped.KV()
	require.NoError(t, err)

	auth := newAuthenticator(testMaster, nil, hmacauth.VerifierOpts{SkewSec: 60, MaxBodyBytes: 1 << 20})
	h := newHandler(kv, index, auth, func() bool { return true }, logr.Discard())
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, index
}

func stateToken(ns, keyspace string) string {
	return hmacauth.EncodeKeyForEnv(hmacauth.DeriveStateKeyspaceKey(testMaster, ns, keyspace))
}

// doState fires an authenticated request on the bearer path.
func doState(t *testing.T, srv *httptest.Server, method, path, ns, keyspace, token string, body []byte, hdrs map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set(HeaderStateNamespace, ns)
	req.Header.Set(HeaderStateKeyspace, keyspace)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

var (
	fnA = types.NamespacedName{Namespace: "ns-a", Name: "fn-a"}
	fnB = types.NamespacedName{Namespace: "ns-b", Name: "fn-b"}
)

func twoFns() map[types.NamespacedName]*fv1.StateConfig {
	return map[types.NamespacedName]*fv1.StateConfig{
		fnA: {},
		fnB: {},
	}
}

func TestHandlerCRUDRoundTrip(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tok := stateToken("ns-a", "fn-a")

	resp := doState(t, srv, http.MethodGet, "/v1/state/counter", "ns-a", "fn-a", tok, nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp = doState(t, srv, http.MethodPut, "/v1/state/counter", "ns-a", "fn-a", tok, []byte("41"), nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	resp = doState(t, srv, http.MethodGet, "/v1/state/counter", "ns-a", "fn-a", tok, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "1", resp.Header.Get(HeaderStateVersion))
	b, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "41", string(b))

	resp = doState(t, srv, http.MethodDelete, "/v1/state/counter", "ns-a", "fn-a", tok, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp = doState(t, srv, http.MethodGet, "/v1/state/counter", "ns-a", "fn-a", tok, nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandlerCASMatrix(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tok := stateToken("ns-a", "fn-a")

	// If-Match 0 = create-only.
	resp := doState(t, srv, http.MethodPut, "/v1/state/k", "ns-a", "fn-a", tok, []byte("v1"), map[string]string{"If-Match": "0"})
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp = doState(t, srv, http.MethodPut, "/v1/state/k", "ns-a", "fn-a", tok, []byte("x"), map[string]string{"If-Match": "0"})
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)

	// CAS on the right version succeeds; stale version conflicts.
	resp = doState(t, srv, http.MethodPut, "/v1/state/k", "ns-a", "fn-a", tok, []byte("v2"), map[string]string{"If-Match": "1"})
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp = doState(t, srv, http.MethodPut, "/v1/state/k", "ns-a", "fn-a", tok, []byte("v3"), map[string]string{"If-Match": "1"})
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)

	// Explicit CAS endpoint.
	body, _ := json.Marshal(casRequest{ExpectVersion: 2, Value: []byte("v4")})
	resp = doState(t, srv, http.MethodPost, "/v1/state/k/cas", "ns-a", "fn-a", tok, body, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	body, _ = json.Marshal(casRequest{ExpectVersion: 2, Value: []byte("v5")})
	resp = doState(t, srv, http.MethodPost, "/v1/state/k/cas", "ns-a", "fn-a", tok, body, nil)
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)

	// Versioned delete: stale conflicts, current succeeds.
	resp = doState(t, srv, http.MethodDelete, "/v1/state/k", "ns-a", "fn-a", tok, nil, map[string]string{"If-Match": "1"})
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
	resp = doState(t, srv, http.MethodDelete, "/v1/state/k", "ns-a", "fn-a", tok, nil, map[string]string{"If-Match": "3"})
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

// TestHandlerScopeForgery is S1's example-level check (the fuzzer generalizes
// it): function A's token must not open function B's keyspace, under header
// splicing or cross-namespace claims.
func TestHandlerScopeForgery(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tokA := stateToken("ns-a", "fn-a")

	// A's token with B's claims: rejected (derivation mismatch).
	resp := doState(t, srv, http.MethodGet, "/v1/state/x", "ns-b", "fn-b", tokA, nil, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	// A's token, A's namespace, B's keyspace: rejected.
	resp = doState(t, srv, http.MethodGet, "/v1/state/x", "ns-a", "fn-b", tokA, nil, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	// Garbage token: rejected.
	resp = doState(t, srv, http.MethodGet, "/v1/state/x", "ns-a", "fn-a", "deadbeef", nil, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	// Missing scope headers: bad request.
	resp = doState(t, srv, http.MethodGet, "/v1/state/x", "", "", tokA, nil, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	// Valid token for an UNCLAIMED keyspace (no Function claims it): the
	// defense-in-depth index guard rejects even a correctly-derived token.
	resp = doState(t, srv, http.MethodGet, "/v1/state/x", "ns-a", "ghost", stateToken("ns-a", "ghost"), nil, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestHandlerQuotaRejections(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, map[types.NamespacedName]*fv1.StateConfig{
		fnA: {MaxValueBytes: 8, MaxKeys: 2},
	})
	tok := stateToken("ns-a", "fn-a")

	// Value too large: 413 with the machine-readable code.
	resp := doState(t, srv, http.MethodPut, "/v1/state/big", "ns-a", "fn-a", tok, []byte("123456789"), nil)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	var e apiError
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&e))
	assert.Equal(t, "quota_value_bytes", e.Code)

	// Key budget: third live key is 429.
	for i := range 2 {
		resp = doState(t, srv, http.MethodPut, fmt.Sprintf("/v1/state/k%d", i), "ns-a", "fn-a", tok, []byte("v"), nil)
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
	}
	resp = doState(t, srv, http.MethodPut, "/v1/state/k2", "ns-a", "fn-a", tok, []byte("v"), nil)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	e = apiError{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&e))
	assert.Equal(t, "quota_keys", e.Code)
}

func TestHandlerList(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tok := stateToken("ns-a", "fn-a")
	for _, k := range []string{"a1", "a2", "a3", "b1"} {
		resp := doState(t, srv, http.MethodPut, "/v1/state/"+k, "ns-a", "fn-a", tok, []byte("v"), nil)
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
	}

	resp := doState(t, srv, http.MethodGet, "/v1/state?prefix=a&limit=2", "ns-a", "fn-a", tok, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var lr listResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&lr))
	assert.Equal(t, []string{"a1", "a2"}, lr.Keys)
	require.NotEmpty(t, lr.Cursor)

	resp = doState(t, srv, http.MethodGet, "/v1/state?prefix=a&limit=2&cursor="+lr.Cursor, "ns-a", "fn-a", tok, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	lr = listResponse{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&lr))
	assert.Equal(t, []string{"a3"}, lr.Keys)
	assert.Empty(t, lr.Cursor)
}

func TestHandlerDefaultTTLHeaderApplied(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tok := stateToken("ns-a", "fn-a")
	// Bad TTL header is a 400, not a silent default.
	resp := doState(t, srv, http.MethodPut, "/v1/state/k", "ns-a", "fn-a", tok, []byte("v"), map[string]string{HeaderStateTTL: "soon"})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	// Valid TTL accepted.
	resp = doState(t, srv, http.MethodPut, "/v1/state/k", "ns-a", "fn-a", tok, []byte("v"), map[string]string{HeaderStateTTL: "1h"})
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

// TestHandlerAdminHMACPath drives the CLI/operator channel: HMAC-signed
// requests (no bearer) with ServiceStateAPI, including access to unclaimed
// keyspaces (orphan inspection) — and fail-closed behavior without a secret.
func TestHandlerAdminHMACPath(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())

	signed := &http.Client{Transport: hmacauth.NewServiceSigningTransport(testMaster, hmacauth.ServiceStateAPI, http.DefaultTransport, "/v1/state")}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, srv.URL+"/v1/state/admin-key", bytes.NewReader([]byte("v")))
	require.NoError(t, err)
	req.Header.Set(HeaderStateNamespace, "ns-a")
	req.Header.Set(HeaderStateKeyspace, "ghost") // unclaimed: admin may inspect
	resp, err := signed.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Unsigned, bearer-less request: 401.
	req2, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/v1/state/admin-key", nil)
	require.NoError(t, err)
	req2.Header.Set(HeaderStateNamespace, "ns-a")
	req2.Header.Set(HeaderStateKeyspace, "ghost")
	resp2, err := srv.Client().Do(req2)
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
}

func TestHandlerHealthEndpointsUnauthenticated(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, nil)
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := srv.Client().Get(srv.URL + path)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, path)
	}
}

func TestFunctionIndexMinQuotaAcrossClaimants(t *testing.T) {
	t.Parallel()
	ix := NewFunctionIndex()
	shared := &fv1.StateConfig{Keyspace: "shared", MaxKeys: 100, MaxValueBytes: 1024}
	stricter := &fv1.StateConfig{Keyspace: "shared", MaxKeys: 10, MaxValueBytes: 4096, DefaultTTL: &metav1.Duration{Duration: 60000000000}}
	ix.Upsert(types.NamespacedName{Namespace: "ns", Name: "f1"}, shared)
	ix.Upsert(types.NamespacedName{Namespace: "ns", Name: "f2"}, stricter)

	q := ix.Resolve(statestore.Scope{Namespace: "ns", Owner: StateOwner, Keyspace: "shared"})
	assert.EqualValues(t, 10, q.MaxKeys, "min across claimants")
	assert.EqualValues(t, 1024, q.MaxValueBytes, "min across claimants")
	assert.True(t, ix.ClaimedByOther(types.NamespacedName{Namespace: "ns", Name: "f1"}, "ns", "shared"))

	ix.Delete(types.NamespacedName{Namespace: "ns", Name: "f2"})
	assert.False(t, ix.ClaimedByOther(types.NamespacedName{Namespace: "ns", Name: "f1"}, "ns", "shared"))
	assert.True(t, ix.Known("ns", "shared"))
	ix.Delete(types.NamespacedName{Namespace: "ns", Name: "f1"})
	assert.False(t, ix.Known("ns", "shared"))
}

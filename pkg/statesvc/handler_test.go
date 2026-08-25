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
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/statestore/memory"
	"github.com/fission/fission/pkg/statesvc/stateapi"
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
	el, err := scoped.EventLog()
	require.NoError(t, err)

	auth := newAuthenticator(testMaster, nil, hmacauth.VerifierOpts{SkewSec: 60, MaxBodyBytes: 1 << 20})
	h := newHandler(kv, el, index, auth, func() bool { return true }, logr.Discard())
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
	req.Header.Set(stateapi.HeaderNamespace, ns)
	req.Header.Set(stateapi.HeaderKeyspace, keyspace)
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
	assert.Equal(t, "1", resp.Header.Get(stateapi.HeaderVersion))
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
	body, _ := json.Marshal(stateapi.CASRequest{ExpectVersion: 2, Value: []byte("v4")})
	resp = doState(t, srv, http.MethodPost, "/v1/state/k/cas", "ns-a", "fn-a", tok, body, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	body, _ = json.Marshal(stateapi.CASRequest{ExpectVersion: 2, Value: []byte("v5")})
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
	var e stateapi.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&e))
	assert.Equal(t, "quota_value_bytes", e.Code)

	// Key budget: third live key is 429.
	for i := range 2 {
		resp = doState(t, srv, http.MethodPut, fmt.Sprintf("/v1/state/k%d", i), "ns-a", "fn-a", tok, []byte("v"), nil)
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
	}
	resp = doState(t, srv, http.MethodPut, "/v1/state/k2", "ns-a", "fn-a", tok, []byte("v"), nil)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	e = stateapi.Error{}
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
	var lr stateapi.ListResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&lr))
	assert.Equal(t, []string{"a1", "a2"}, lr.Keys)
	require.NotEmpty(t, lr.Cursor)

	resp = doState(t, srv, http.MethodGet, "/v1/state?prefix=a&limit=2&cursor="+lr.Cursor, "ns-a", "fn-a", tok, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	lr = stateapi.ListResponse{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&lr))
	assert.Equal(t, []string{"a3"}, lr.Keys)
	assert.Empty(t, lr.Cursor)
}

func TestHandlerDefaultTTLHeaderApplied(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tok := stateToken("ns-a", "fn-a")
	// Bad TTL header is a 400, not a silent default.
	resp := doState(t, srv, http.MethodPut, "/v1/state/k", "ns-a", "fn-a", tok, []byte("v"), map[string]string{stateapi.HeaderTTL: "soon"})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	// Valid TTL accepted.
	resp = doState(t, srv, http.MethodPut, "/v1/state/k", "ns-a", "fn-a", tok, []byte("v"), map[string]string{stateapi.HeaderTTL: "1h"})
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

// TestHandlerAdminHMACPath drives the CLI/operator channel: HMAC-signed
// requests (no bearer) with ServiceStateAPI and QUERY-borne scope claims
// (signature-covered), including access to unclaimed keyspaces (orphan
// inspection) — plus fail-closed behavior without a signature and rejection
// of header-borne admin scope (the replay-retarget hardening).
func TestHandlerAdminHMACPath(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())

	signed := &http.Client{Transport: hmacauth.NewServiceSigningTransport(testMaster, hmacauth.ServiceStateAPI, http.DefaultTransport, "/v1/state")}

	// Signed PUT with scope in the query: accepted, even for an unclaimed
	// keyspace (admin may inspect orphans).
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut,
		srv.URL+"/v1/state/admin-key?scope-namespace=ns-a&scope-keyspace=ghost", bytes.NewReader([]byte("v")))
	require.NoError(t, err)
	resp, err := signed.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Signed request with scope only in HEADERS: rejected — headers are not
	// signature-covered, so accepting them would allow replay retargeting.
	req2, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/v1/state/admin-key", nil)
	require.NoError(t, err)
	req2.Header.Set(stateapi.HeaderNamespace, "ns-a")
	req2.Header.Set(stateapi.HeaderKeyspace, "ghost")
	resp2, err := signed.Do(req2)
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp2.StatusCode)

	// Unsigned, bearer-less request: 401 from the verifier.
	req3, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		srv.URL+"/v1/state/admin-key?scope-namespace=ns-a&scope-keyspace=ghost", nil)
	require.NoError(t, err)
	resp3, err := srv.Client().Do(req3)
	require.NoError(t, err)
	defer func() { _ = resp3.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp3.StatusCode)
}

// TestHandlerDevPassThroughMode covers the no-master-secret branch (dev
// clusters, matching the router-internal/statestoresvc convention): bearer
// requests are accepted on their claims alone, but the admin HMAC path FAILS
// CLOSED (401) — the "fail closed like MCP" decision from the RFC's open
// questions — and the known-keyspace index guard still applies.
func TestHandlerDevPassThroughMode(t *testing.T) {
	t.Parallel()
	inner, err := memory.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })
	index := NewFunctionIndex()
	index.Upsert(fnA, &fv1.StateConfig{})
	scoped := statestore.NewScoped(inner, index)
	kv, err := scoped.KV()
	require.NoError(t, err)
	el, err := scoped.EventLog()
	require.NoError(t, err)
	auth := newAuthenticator(nil, nil, hmacauth.VerifierOpts{}) // no master secret
	srv := httptest.NewServer(newHandler(kv, el, index, auth, func() bool { return true }, logr.Discard()))
	t.Cleanup(srv.Close)

	// Bearer accepted on claims alone (any token), for a claimed keyspace.
	resp := doState(t, srv, http.MethodPut, "/v1/state/k", "ns-a", "fn-a", "anything-goes", []byte("v"), nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// But the index guard still rejects an unclaimed keyspace.
	resp = doState(t, srv, http.MethodGet, "/v1/state/k", "ns-a", "ghost", "tok", nil, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Admin path fails closed: no signature to verify against a nil secret.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		srv.URL+"/v1/state/k?scope-namespace=ns-a&scope-keyspace=fn-a", nil)
	require.NoError(t, err)
	resp2, err := srv.Client().Do(req)
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

// postEventLog fires an authenticated POST /v1/eventlog/{route} request with a
// JSON-marshaled body.
func postEventLog(t *testing.T, srv *httptest.Server, route, ns, keyspace, token string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	return doState(t, srv, http.MethodPost, "/v1/eventlog/"+route, ns, keyspace, token, b, nil)
}

func TestHandlerEventLogAppendReadHeadRoundTrip(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tok := stateToken("ns-a", "fn-a")

	resp := postEventLog(t, srv, "head", "ns-a", "fn-a", tok, stateapi.EventHeadRequest{Stream: "agentlog/s-1"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var headResp stateapi.EventHeadResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&headResp))
	assert.EqualValues(t, 0, headResp.Head, "absent stream has head 0")

	resp = postEventLog(t, srv, "append", "ns-a", "fn-a", tok, stateapi.EventAppendRequest{
		Stream:      "agentlog/s-1",
		ExpectedSeq: 0,
		Events: []stateapi.EventInput{
			{Type: "tool_call", Payload: []byte("e1")},
			{Type: "tool_call", Payload: []byte("e2")},
		},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var appendResp stateapi.EventAppendResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&appendResp))
	assert.EqualValues(t, 2, appendResp.Head)

	resp = postEventLog(t, srv, "append", "ns-a", "fn-a", tok, stateapi.EventAppendRequest{
		Stream:      "agentlog/s-1",
		ExpectedSeq: 2,
		Events:      []stateapi.EventInput{{Type: "tool_result", Payload: []byte("e3")}},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	appendResp = stateapi.EventAppendResponse{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&appendResp))
	assert.EqualValues(t, 3, appendResp.Head)

	resp = postEventLog(t, srv, "read", "ns-a", "fn-a", tok, stateapi.EventReadRequest{Stream: "agentlog/s-1", FromSeq: 0, Limit: 100})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var readResp stateapi.EventReadResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&readResp))
	require.Len(t, readResp.Events, 3)
	assert.EqualValues(t, 1, readResp.Events[0].Seq)
	assert.Equal(t, "tool_call", readResp.Events[0].Type)
	assert.Equal(t, []byte("e1"), readResp.Events[0].Payload)
	assert.EqualValues(t, 3, readResp.Events[2].Seq)
	assert.Equal(t, "tool_result", readResp.Events[2].Type)
	assert.False(t, readResp.Events[0].At.IsZero())

	resp = postEventLog(t, srv, "head", "ns-a", "fn-a", tok, stateapi.EventHeadRequest{Stream: "agentlog/s-1"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	headResp = stateapi.EventHeadResponse{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&headResp))
	assert.EqualValues(t, 3, headResp.Head)
}

func TestHandlerEventLogCASConflictReturns412WithHead(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tok := stateToken("ns-a", "fn-a")

	resp := postEventLog(t, srv, "append", "ns-a", "fn-a", tok, stateapi.EventAppendRequest{
		Stream: "agentlog/s-1", ExpectedSeq: 0,
		Events: []stateapi.EventInput{{Type: "t", Payload: []byte("e1")}},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Stale expectedSeq: the real head is 1, not 0.
	resp = postEventLog(t, srv, "append", "ns-a", "fn-a", tok, stateapi.EventAppendRequest{
		Stream: "agentlog/s-1", ExpectedSeq: 0,
		Events: []stateapi.EventInput{{Type: "t", Payload: []byte("e2")}},
	})
	require.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
	var e stateapi.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&e))
	assert.Equal(t, stateapi.CodeVersionConflict, e.Code)
	require.NotNil(t, e.Head, "conflict envelope must carry the current head")
	assert.EqualValues(t, 1, *e.Head)
}

func TestHandlerEventLogNegativeExpectedSeqRejected(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tok := stateToken("ns-a", "fn-a")

	// expectedSeq < 0 (statestore.AppendAny) must never be reachable through
	// this API: CAS is mandatory on the wire.
	resp := postEventLog(t, srv, "append", "ns-a", "fn-a", tok, stateapi.EventAppendRequest{
		Stream: "agentlog/s-1", ExpectedSeq: -1,
		Events: []stateapi.EventInput{{Type: "t", Payload: []byte("e1")}},
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandlerEventLogCrossScopeIsolation(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tokA := stateToken("ns-a", "fn-a")
	tokB := stateToken("ns-b", "fn-b")

	resp := postEventLog(t, srv, "append", "ns-a", "fn-a", tokA, stateapi.EventAppendRequest{
		Stream: "agentlog/s-1", ExpectedSeq: 0,
		Events: []stateapi.EventInput{{Type: "t", Payload: []byte("a1")}, {Type: "t", Payload: []byte("a2")}},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Same client-visible stream name, different keyspace: independent stream,
	// still at head 0.
	resp = postEventLog(t, srv, "head", "ns-b", "fn-b", tokB, stateapi.EventHeadRequest{Stream: "agentlog/s-1"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var headResp stateapi.EventHeadResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&headResp))
	assert.EqualValues(t, 0, headResp.Head)

	resp = postEventLog(t, srv, "head", "ns-a", "fn-a", tokA, stateapi.EventHeadRequest{Stream: "agentlog/s-1"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	headResp = stateapi.EventHeadResponse{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&headResp))
	assert.EqualValues(t, 2, headResp.Head)
}

func TestHandlerEventLogReadLimitClamped(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tok := stateToken("ns-a", "fn-a")

	events := make([]stateapi.EventInput, 3)
	for i := range events {
		events[i] = stateapi.EventInput{Type: "t", Payload: []byte{byte(i)}}
	}
	resp := postEventLog(t, srv, "append", "ns-a", "fn-a", tok, stateapi.EventAppendRequest{Stream: "s", ExpectedSeq: 0, Events: events})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// A limit far above MaxReadLimit is clamped, not rejected.
	resp = postEventLog(t, srv, "read", "ns-a", "fn-a", tok, stateapi.EventReadRequest{Stream: "s", FromSeq: 0, Limit: 100000})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var readResp stateapi.EventReadResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&readResp))
	assert.Len(t, readResp.Events, 3)

	// limit <= 0 defaults rather than erroring or returning nothing.
	resp = postEventLog(t, srv, "read", "ns-a", "fn-a", tok, stateapi.EventReadRequest{Stream: "s", FromSeq: 0, Limit: 0})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	readResp = stateapi.EventReadResponse{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&readResp))
	assert.Len(t, readResp.Events, 3)
}

// TestHandlerEventLogReadLimitHardCapAt500 proves the clamp lands exactly on
// MaxReadLimit, not just "doesn't error": appends more than 500 events, then
// asserts a page requested with an over-cap limit returns exactly 500.
func TestHandlerEventLogReadLimitHardCapAt500(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tok := stateToken("ns-a", "fn-a")

	const total = stateapi.MaxReadLimit + 50
	var seq int64
	for seq < total {
		batch := min(int64(stateapi.MaxAppendEvents), total-seq)
		events := make([]stateapi.EventInput, batch)
		for i := range events {
			events[i] = stateapi.EventInput{Type: "t", Payload: []byte("x")}
		}
		resp := postEventLog(t, srv, "append", "ns-a", "fn-a", tok, stateapi.EventAppendRequest{Stream: "s", ExpectedSeq: seq, Events: events})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		seq += batch
	}

	resp := postEventLog(t, srv, "read", "ns-a", "fn-a", tok, stateapi.EventReadRequest{Stream: "s", FromSeq: 0, Limit: 100000})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var readResp stateapi.EventReadResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&readResp))
	assert.Len(t, readResp.Events, stateapi.MaxReadLimit)
}

func TestHandlerEventLogPayloadOverMaxValueBytesCap(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, map[types.NamespacedName]*fv1.StateConfig{
		fnA: {MaxValueBytes: 8},
	})
	tok := stateToken("ns-a", "fn-a")

	resp := postEventLog(t, srv, "append", "ns-a", "fn-a", tok, stateapi.EventAppendRequest{
		Stream: "s", ExpectedSeq: 0,
		Events: []stateapi.EventInput{{Type: "t", Payload: []byte("123456789")}}, // 9 > 8
	})
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	var e stateapi.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&e))
	assert.Equal(t, stateapi.CodeQuotaValueBytes, e.Code)
}

func TestHandlerEventLogTooManyEventsRejected(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tok := stateToken("ns-a", "fn-a")

	events := make([]stateapi.EventInput, stateapi.MaxAppendEvents+1)
	for i := range events {
		events[i] = stateapi.EventInput{Type: "t", Payload: []byte("x")}
	}
	resp := postEventLog(t, srv, "append", "ns-a", "fn-a", tok, stateapi.EventAppendRequest{Stream: "s", ExpectedSeq: 0, Events: events})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandlerEventLogEmptyEventsRejected(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tok := stateToken("ns-a", "fn-a")

	resp := postEventLog(t, srv, "append", "ns-a", "fn-a", tok, stateapi.EventAppendRequest{Stream: "s", ExpectedSeq: 0, Events: nil})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandlerEventLogBadStreamRejected(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t, twoFns())
	tok := stateToken("ns-a", "fn-a")

	tests := []struct {
		name   string
		stream string
	}{
		{"dot-dot traversal", "../x"},
		{"leading slash", "/agentlog"},
		{"201 chars", strings.Repeat("a", 201)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := postEventLog(t, srv, "append", "ns-a", "fn-a", tok, stateapi.EventAppendRequest{
				Stream: tt.stream, ExpectedSeq: 0,
				Events: []stateapi.EventInput{{Type: "t", Payload: []byte("x")}},
			})
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "append")

			resp = postEventLog(t, srv, "head", "ns-a", "fn-a", tok, stateapi.EventHeadRequest{Stream: tt.stream})
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "head")

			resp = postEventLog(t, srv, "read", "ns-a", "fn-a", tok, stateapi.EventReadRequest{Stream: tt.stream})
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "read")
		})
	}
}

// TestHandlerEventLogFullBatchAtCapSucceeds is the body-cap formula
// regression test: MaxAppendEvents events, each exactly at the keyspace's
// MaxValueBytes ceiling, must fit under the append route's LimitReader
// (MaxAppendEvents*maxValueBytes*4/3 + 64<<10) — the KV route's
// maxBytes*2+4096 formula would truncate this legitimate request.
func TestHandlerEventLogFullBatchAtCapSucceeds(t *testing.T) {
	t.Parallel()
	const maxValueBytes = 4096
	srv, _ := newTestServer(t, map[types.NamespacedName]*fv1.StateConfig{
		fnA: {MaxValueBytes: maxValueBytes},
	})
	tok := stateToken("ns-a", "fn-a")

	events := make([]stateapi.EventInput, stateapi.MaxAppendEvents)
	payload := bytes.Repeat([]byte("x"), maxValueBytes)
	for i := range events {
		events[i] = stateapi.EventInput{Type: "t", Payload: payload}
	}
	resp := postEventLog(t, srv, "append", "ns-a", "fn-a", tok, stateapi.EventAppendRequest{Stream: "s", ExpectedSeq: 0, Events: events})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var appendResp stateapi.EventAppendResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&appendResp))
	assert.EqualValues(t, stateapi.MaxAppendEvents, appendResp.Head)
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
	// TTL merge: a lax claimant with no TTL must not erase the stricter
	// claimant's DefaultTTL (the `ttl == 0 ||` guard in lookup).
	assert.Equal(t, time.Minute, ix.DefaultTTL("ns", "shared"), "min-nonzero TTL across claimants")

	// A declared quota ABOVE the platform default is honored — the default is
	// a per-function fallback, never a ceiling.
	ix.Upsert(types.NamespacedName{Namespace: "ns", Name: "big"}, &fv1.StateConfig{Keyspace: "big", MaxKeys: 50000, MaxValueBytes: 1 << 20})
	qBig := ix.Resolve(statestore.Scope{Namespace: "ns", Owner: StateOwner, Keyspace: "big"})
	assert.EqualValues(t, 50000, qBig.MaxKeys)
	assert.EqualValues(t, 1<<20, qBig.MaxValueBytes)

	// Unclaimed keyspace: platform defaults.
	qGhost := ix.Resolve(statestore.Scope{Namespace: "ns", Owner: StateOwner, Keyspace: "ghost"})
	assert.EqualValues(t, fv1.DefaultStateMaxKeys, qGhost.MaxKeys)
	assert.EqualValues(t, fv1.DefaultStateMaxValueBytes, qGhost.MaxValueBytes)
	assert.True(t, ix.ClaimedByOther(types.NamespacedName{Namespace: "ns", Name: "f1"}, "ns", "shared"))

	ix.Delete(types.NamespacedName{Namespace: "ns", Name: "f2"})
	assert.False(t, ix.ClaimedByOther(types.NamespacedName{Namespace: "ns", Name: "f1"}, "ns", "shared"))
	assert.True(t, ix.Known("ns", "shared"))
	ix.Delete(types.NamespacedName{Namespace: "ns", Name: "f1"})
	assert.False(t, ix.Known("ns", "shared"))
}

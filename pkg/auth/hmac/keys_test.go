// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testMaster = "fission-test-master-must-be-long-enough"

func TestDeriveServiceKeyDeterministic(t *testing.T) {
	a := DeriveServiceKey([]byte(testMaster), ServiceStoragesvc)
	b := DeriveServiceKey([]byte(testMaster), ServiceStoragesvc)
	require.Len(t, a, derivedKeyLength)
	assert.Equal(t, a, b, "same master + service must produce the same key")
}

func TestDeriveServiceKeyServiceIsolation(t *testing.T) {
	master := []byte(testMaster)
	storage := DeriveServiceKey(master, ServiceStoragesvc)
	fetcher := DeriveServiceKey(master, ServiceFetcher)
	builder := DeriveServiceKey(master, ServiceBuilder)
	executor := DeriveServiceKey(master, ServiceExecutor)
	router := DeriveServiceKey(master, ServiceRouterInternal)

	keys := [][]byte{storage, fetcher, builder, executor, router}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			assert.NotEqual(t, keys[i], keys[j],
				"derived keys for different services must differ")
		}
	}
}

func TestDeriveServiceKeyEmptyMaster(t *testing.T) {
	assert.Nil(t, DeriveServiceKey(nil, ServiceStoragesvc),
		"empty master must return nil so callers can short-circuit")
	assert.Nil(t, DeriveServiceKey([]byte{}, ServiceStoragesvc))
}

// TestServiceSignerVerifierRoundTrip pins the end-to-end contract:
// a signer using the master + service key produces a request the
// verifier with the same master + service accepts, and rejects when
// either side uses a different service identifier.
func TestServiceSignerVerifierRoundTrip(t *testing.T) {
	master := []byte(testMaster)
	now := func() time.Time { return time.Unix(1715000123, 0) }

	for _, svc := range []Service{
		ServiceStoragesvc, ServiceFetcher, ServiceBuilder,
		ServiceExecutor, ServiceRouterInternal,
	} {
		t.Run(string(svc), func(t *testing.T) {
			handler := ServiceVerifier(master, nil, svc, VerifierOpts{
				SkewSec: 60, Now: now,
			})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
			}))

			rr := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/archive", strings.NewReader("body"))
			signer := ServiceSigner(master, svc, &recordingTransport{rr: rr}, now)
			_, err := signer.RoundTrip(req)
			require.NoError(t, err)
			// Replay the signed request against the verifier-wrapped handler.
			req2 := signedRequestFromRecorder(t, rr)
			rr2 := httptest.NewRecorder()
			handler.ServeHTTP(rr2, req2)
			assert.Equal(t, 200, rr2.Code, "matching service must accept")
		})
	}
}

func TestServiceSignerVerifierServiceMismatch(t *testing.T) {
	master := []byte(testMaster)
	now := func() time.Time { return time.Unix(1715000123, 0) }

	// Verifier accepts ServiceStoragesvc; signer signs as ServiceFetcher.
	handler := ServiceVerifier(master, nil, ServiceStoragesvc, VerifierOpts{
		SkewSec: 60, Now: now,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/archive", strings.NewReader("body"))
	signer := ServiceSigner(master, ServiceFetcher, &recordingTransport{rr: rr}, now)
	_, err := signer.RoundTrip(req)
	require.NoError(t, err)

	req2 := signedRequestFromRecorder(t, rr)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	assert.Equal(t, 401, rr2.Code,
		"verifier expecting ServiceStoragesvc must reject a ServiceFetcher signature")
}

func TestServiceVerifierEmptyMasterPasses(t *testing.T) {
	// Empty master → empty derived key → verifier short-circuits to
	// pass-through. Required for the backwards-compat rollout.
	handler := ServiceVerifier(nil, nil, ServiceStoragesvc, VerifierOpts{
		SkewSec: 60, Now: func() time.Time { return time.Unix(1715000123, 0) },
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/archive", nil)
	handler.ServeHTTP(rr, req)
	assert.Equal(t, 200, rr.Code, "empty master must disable enforcement")
}

// TestDeriveAgentIdentityKeyDeterministic pins the basic HKDF contract for the
// per-agent identity chain: same master + namespace + agent always derives the
// same key, at the standard 32-byte length.
func TestDeriveAgentIdentityKeyDeterministic(t *testing.T) {
	master := []byte(testMaster)
	a := DeriveAgentIdentityKey(master, "ns", "architect")
	b := DeriveAgentIdentityKey(master, "ns", "architect")
	require.Len(t, a, derivedKeyLength)
	assert.Equal(t, a, b, "same master + namespace + agent must produce the same key")
}

// TestDeriveAgentIdentityKeyIsolation is the distinctness matrix for the
// agentident chain: a token derived for one (namespace, agent) can never
// collide with another scope, with a different master, or with the
// DeriveStateKeyspaceKey chain fed the identical (namespace, agent) inputs —
// the "agentident" tag alone must be what keeps the two channels apart.
func TestDeriveAgentIdentityKeyIsolation(t *testing.T) {
	master := []byte(testMaster)
	base := DeriveAgentIdentityKey(master, "ns-a", "architect")

	distinct := [][]byte{
		DeriveAgentIdentityKey(master, "ns-b", "architect"),                 // other namespace
		DeriveAgentIdentityKey(master, "ns-a", "support-desk"),              // other agent
		DeriveAgentIdentityKey([]byte("other-master"), "ns-a", "architect"), // other master
		DeriveStateKeyspaceKey(master, "ns-a", "architect"),                 // sibling chain, same inputs
	}
	for i, k := range distinct {
		assert.NotEqual(t, base, k, "candidate %d must not collide with the base key", i)
	}
}

func TestDeriveAgentIdentityKeyNoFieldSplice(t *testing.T) {
	master := []byte(testMaster)
	// DNS-label namespace/agent names cannot contain ':', so swapped fields
	// spliced across the info-string boundary must not derive equal keys.
	assert.NotEqual(t,
		DeriveAgentIdentityKey(master, "a", "b"),
		DeriveAgentIdentityKey(master, "b", "a"))
}

func TestDeriveAgentIdentityKeyEmptyMaster(t *testing.T) {
	assert.Nil(t, DeriveAgentIdentityKey(nil, "ns", "architect"),
		"empty master must return nil so callers can short-circuit")
	assert.Nil(t, DeriveAgentIdentityKey([]byte{}, "ns", "architect"))
}

func TestDeriveAgentIdentityKeyEncodeDecodeRoundTrip(t *testing.T) {
	key := DeriveAgentIdentityKey([]byte(testMaster), "ns", "architect")
	require.NotEmpty(t, key)
	enc := EncodeKeyForEnv(key)
	assert.Equal(t, key, DecodeKeyFromEnv(enc), "encode->decode must round-trip the derived identity key")
}

// recordingTransport captures the signed request without dialing
// over the network so we can replay it through the verifier.
type recordingTransport struct {
	rr *httptest.ResponseRecorder
}

func (t *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// Persist headers + body for replay.
	body, _ := readAllAndRestore(r)
	t.rr.Header().Set(HeaderTimestamp, r.Header.Get(HeaderTimestamp))
	t.rr.Header().Set(HeaderSignature, r.Header.Get(HeaderSignature))
	t.rr.Header().Set("X-Test-Method", r.Method)
	t.rr.Header().Set("X-Test-RequestURI", r.URL.RequestURI())
	t.rr.Body = bytes.NewBuffer(body)
	return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
}

func readAllAndRestore(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

func signedRequestFromRecorder(t testing.TB, rr *httptest.ResponseRecorder) *http.Request {
	t.Helper()
	method := rr.Header().Get("X-Test-Method")
	uri := rr.Header().Get("X-Test-RequestURI")
	req := httptest.NewRequest(method, uri, bytes.NewReader(rr.Body.Bytes()))
	req.Header.Set(HeaderTimestamp, rr.Header().Get(HeaderTimestamp))
	req.Header.Set(HeaderSignature, rr.Header().Get(HeaderSignature))
	return req
}

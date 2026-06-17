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

func TestDeriveServiceKeyNSRoundTrip(t *testing.T) {
	key := DeriveServiceKeyNS([]byte(testMaster), ServiceFetcher, "team-a")
	require.NotEmpty(t, key)
	sig := Sign(key, "POST", "/specialize", []byte("body"), 1715000123)
	assert.True(t, Verify(key, "POST", "/specialize", []byte("body"), 1715000123, sig))
}

func TestDeriveServiceKeyNSNamespaceIsolation(t *testing.T) {
	master := []byte(testMaster)
	keyA := DeriveServiceKeyNS(master, ServiceFetcher, "team-a")
	keyB := DeriveServiceKeyNS(master, ServiceFetcher, "team-b")
	assert.False(t, bytes.Equal(keyA, keyB), "different namespaces derive different keys")

	sig := Sign(keyA, "POST", "/specialize", nil, 1715000123)
	assert.False(t, Verify(keyB, "POST", "/specialize", nil, 1715000123, sig), "ns-b key must reject an ns-a signature")
}

func TestDeriveServiceKeyNSPreservesMasterScopedKeys(t *testing.T) {
	master := []byte(testMaster)
	// The ":<ns>" suffix must never collide with the plain service key, so adding
	// namespace scoping leaves existing master-scoped channels byte-for-byte
	// unchanged (no KeyVersion bump, no in-flight signature breakage).
	assert.False(t, bytes.Equal(DeriveServiceKey(master, ServiceFetcher), DeriveServiceKeyNS(master, ServiceFetcher, "")),
		"empty-namespace derivation must still differ from the plain service key")
	assert.False(t, bytes.Equal(DeriveServiceKey(master, ServiceFetcher), DeriveServiceKeyNS(master, ServiceFetcher, "team-a")))
}

func TestDeriveServiceKeyNSEmptyMaster(t *testing.T) {
	assert.Nil(t, DeriveServiceKeyNS(nil, ServiceFetcher, "team-a"))
	assert.Nil(t, DeriveServiceKeyNS([]byte{}, ServiceFetcher, "team-a"))
}

// nsVerifier builds the verifier a tenant pod in `namespace` actually runs: it
// holds only its own derived key (VerifierFromKey), never the master.
func nsVerifier(master []byte, service Service, namespace string, now func() time.Time) func(http.Handler) http.Handler {
	return VerifierFromKey(DeriveServiceKeyNS(master, service, namespace), nil, VerifierOpts{SkewSec: 60, Now: now})
}

func TestServiceSignerVerifierNSRoundTrip(t *testing.T) {
	master := []byte(testMaster)
	now := func() time.Time { return time.Unix(1715000123, 0) }

	// The control plane signs with ServiceSignerNS (it holds the master); the
	// team-a pod verifies with its own derived key.
	handler := nsVerifier(master, ServiceFetcher, "team-a", now)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/specialize", strings.NewReader("body"))
	signer := ServiceSignerNS(master, ServiceFetcher, "team-a", &recordingTransport{rr: rr}, now)
	_, err := signer.RoundTrip(req)
	require.NoError(t, err)

	req2 := signedRequestFromRecorder(t, rr)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	assert.Equal(t, 200, rr2.Code, "same service+namespace must accept")
}

func TestServiceVerifierNSNamespaceMismatch(t *testing.T) {
	master := []byte(testMaster)
	now := func() time.Time { return time.Unix(1715000123, 0) }

	// team-b's pod verifies with its own key; a team-a signature must not forge in.
	handler := nsVerifier(master, ServiceFetcher, "team-b", now)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/specialize", strings.NewReader("body"))
	signer := ServiceSignerNS(master, ServiceFetcher, "team-a", &recordingTransport{rr: rr}, now)
	_, err := signer.RoundTrip(req)
	require.NoError(t, err)

	req2 := signedRequestFromRecorder(t, rr)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	assert.Equal(t, 401, rr2.Code, "a team-a signature must be rejected by a team-b verifier")
}

func TestServiceVerifierNamespaceFromHeader(t *testing.T) {
	master := []byte(testMaster)
	now := func() time.Time { return time.Unix(1715000123, 0) }

	handler := ServiceVerifierNamespaceFromHeader(master, nil, ServiceStoragesvc, VerifierOpts{SkewSec: 60, Now: now})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))

	signed := func(key []byte, nsHeader string) *http.Request {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/archive?id=x", nil)
		_, err := NewSigner(key, &recordingTransport{rr: rr}, now).RoundTrip(req)
		require.NoError(t, err)
		req2 := signedRequestFromRecorder(t, rr)
		// The namespace header rides the request but is NOT part of the signature
		// (canonical = method/uri/body/ts), so set it on the verified request.
		if nsHeader != "" {
			req2.Header.Set(HeaderNamespace, nsHeader)
		}
		return req2
	}
	serve := func(req *http.Request) int {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr.Code
	}

	// Truthful claim: header team-a + signed with K(storagesvc, team-a) → accepted.
	assert.Equal(t, 200, serve(signed(DeriveServiceKeyNS(master, ServiceStoragesvc, "team-a"), "team-a")))

	// Spoof attempt: claim team-b but sign with team-a's key → verifier derives the
	// team-b key, which the caller cannot sign with → rejected.
	assert.Equal(t, 401, serve(signed(DeriveServiceKeyNS(master, ServiceStoragesvc, "team-a"), "team-b")))

	// Dual-accept: no header + master-derived signature (an old, pre-migration
	// client) → accepted, so the upgrade isn't a flag day.
	assert.Equal(t, 200, serve(signed(DeriveServiceKey(master, ServiceStoragesvc), "")))
}

func TestVerifierFromKeyRoundTrip(t *testing.T) {
	// A pod that holds ONLY a derived ns key (never the master) verifies with it.
	now := func() time.Time { return time.Unix(1715000123, 0) }
	key := DeriveServiceKeyNS([]byte(testMaster), ServiceFetcher, "team-a")

	handler := VerifierFromKey(key, nil, VerifierOpts{SkewSec: 60, Now: now})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/specialize", strings.NewReader("body"))
	signer := NewSigner(key, &recordingTransport{rr: rr}, now) // SignerFromKey is just NewSigner
	_, err := signer.RoundTrip(req)
	require.NoError(t, err)

	req2 := signedRequestFromRecorder(t, rr)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	assert.Equal(t, 200, rr2.Code, "verifier built from the raw derived key accepts the matching signature")
}

/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package hmac

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignerSetsHeaders(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts := r.Header.Get(HeaderTimestamp)
		sig := r.Header.Get(HeaderSignature)
		require.NotEmpty(t, ts)
		require.NotEmpty(t, sig)

		body, _ := io.ReadAll(r.Body)
		tsNum, err := strconv.ParseInt(ts, 10, 64)
		require.NoError(t, err)
		assert.True(t, Verify(secret, r.Method, r.URL.Path, body, tsNum, sig))
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c := &http.Client{Transport: NewSigner(secret, http.DefaultTransport, time.Now)}
	resp, err := c.Post(srv.URL+"/v1/archive", "application/octet-stream", strings.NewReader("payload"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, 200, resp.StatusCode)
}

func TestSignerHandlesNilBody(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts := r.Header.Get(HeaderTimestamp)
		sig := r.Header.Get(HeaderSignature)
		require.NotEmpty(t, ts)
		require.NotEmpty(t, sig)

		body, _ := io.ReadAll(r.Body)
		tsNum, err := strconv.ParseInt(ts, 10, 64)
		require.NoError(t, err)
		assert.True(t, Verify(secret, r.Method, r.URL.Path, body, tsNum, sig))
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c := &http.Client{Transport: NewSigner(secret, http.DefaultTransport, time.Now)}
	resp, err := c.Get(srv.URL + "/v1/archive")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, 200, resp.StatusCode)
}

// TestSignerBindsQueryParameter pins the security-critical contract from
// the design doc (docs/internal-auth/00-design.md): a captured signature
// for /v1/archive?id=A must NOT verify against /v1/archive?id=B within
// the skew window. The signer canonicalizes RequestURI (path + raw
// query); without that the `id=` parameter is unbound and a captured
// HEAD/GET/DELETE could be replayed against any other archive id.
func TestSignerBindsQueryParameter(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	now := func() time.Time { return time.Unix(1715000000, 0) }

	// Sign GET /v1/archive?id=A.
	signedURI := "/v1/archive?id=A"
	signer := NewSigner(secret, http.DefaultTransport, now)

	// Capture the signature headers via an httptest server.
	var capturedSig, capturedTs string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSig = r.Header.Get(HeaderSignature)
		capturedTs = r.Header.Get(HeaderTimestamp)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	c := &http.Client{Transport: signer}
	resp, err := c.Get(srv.URL + signedURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.NotEmpty(t, capturedSig, "signer must populate the signature header")

	tsNum, err := strconv.ParseInt(capturedTs, 10, 64)
	require.NoError(t, err)

	// The signature MUST verify for the original ?id=A canonical.
	assert.True(t, Verify(secret, http.MethodGet, signedURI, nil, tsNum, capturedSig),
		"signed request must verify against the same RequestURI")

	// Replay the captured signature against ?id=B — the query difference
	// must change the canonical hash and reject.
	tampered := "/v1/archive?id=B"
	assert.False(t, Verify(secret, http.MethodGet, tampered, nil, tsNum, capturedSig),
		"signature for ?id=A must NOT verify against ?id=B (query parameters bound by RequestURI)")

	// Path-only verification (the pre-RequestURI behaviour) would have
	// passed both canonicals because /v1/archive matches both. Pin that
	// the path-only canonical does NOT match the RequestURI signature so
	// nobody is tempted to revert to signing r.URL.Path alone.
	pathOnly := "/v1/archive"
	assert.False(t, Verify(secret, http.MethodGet, pathOnly, nil, tsNum, capturedSig),
		"path-only canonical must NOT verify against a RequestURI signature")
}

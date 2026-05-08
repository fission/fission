/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package hmac

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestVerifierRejectsUnsigned(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	h := Verifier(VerifierOpts{Secret: secret, SkewSec: 60, Now: time.Now})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/archive", nil)
	h.ServeHTTP(rr, req)
	assert.Equal(t, 401, rr.Code)
}

func TestVerifierAcceptsSigned(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	now := func() time.Time { return time.Unix(1715000000, 0) }
	h := Verifier(VerifierOpts{Secret: secret, SkewSec: 60, Now: now})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assert.Equal(t, []byte("hi"), body, "body must be re-readable downstream")
		w.WriteHeader(200)
	}))
	body := []byte("hi")
	sig := Sign(secret, "POST", "/v1/archive", body, 1715000000)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/archive", bytes.NewReader(body))
	req.Header.Set(HeaderTimestamp, "1715000000")
	req.Header.Set(HeaderSignature, sig)
	h.ServeHTTP(rr, req)
	assert.Equal(t, 200, rr.Code)
}

func TestVerifierBypassesHealthz(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	h := Verifier(VerifierOpts{Secret: secret, SkewSec: 60, Bypass: []string{"/healthz"}, Now: time.Now})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	h.ServeHTTP(rr, req)
	assert.Equal(t, 200, rr.Code)
}

func TestVerifierEmptySecretDisablesEnforcement(t *testing.T) {
	// Empty Secret is the explicit "disabled" state; unsigned requests must pass.
	h := Verifier(VerifierOpts{Secret: nil, SkewSec: 60, Now: time.Now})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/archive", nil)
	h.ServeHTTP(rr, req)
	assert.Equal(t, 200, rr.Code)
}

func TestVerifierAcceptsOldSecretDuringRotation(t *testing.T) {
	current := []byte("current-secret-32-bytes-or-more!")
	old := []byte("old-secret-32-bytes-or-more-!!!!")
	now := func() time.Time { return time.Unix(1715000000, 0) }
	h := Verifier(VerifierOpts{Secret: current, OldSecret: old, SkewSec: 60, Now: now})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	body := []byte("rotation")
	// Signed with the old secret — should still be accepted.
	sig := Sign(old, "POST", "/v1/archive", body, 1715000000)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/archive", bytes.NewReader(body))
	req.Header.Set(HeaderTimestamp, "1715000000")
	req.Header.Set(HeaderSignature, sig)
	h.ServeHTTP(rr, req)
	assert.Equal(t, 200, rr.Code)
}

func TestVerifierRejectsStaleTimestamp(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	now := func() time.Time { return time.Unix(1715000200, 0) } // 200s after the signature
	h := Verifier(VerifierOpts{Secret: secret, SkewSec: 60, Now: now})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	body := []byte("late")
	sig := Sign(secret, "POST", "/v1/archive", body, 1715000000)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/archive", bytes.NewReader(body))
	req.Header.Set(HeaderTimestamp, "1715000000")
	req.Header.Set(HeaderSignature, sig)
	h.ServeHTTP(rr, req)
	assert.Equal(t, 401, rr.Code)
}

// TestVerifierRejectsOversizeBody ensures a body that exceeds MaxBodyBytes is
// rejected with 413 before the downstream handler runs.
func TestVerifierRejectsOversizeBody(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	now := func() time.Time { return time.Unix(1715000000, 0) }
	called := false
	h := Verifier(VerifierOpts{
		Secret:       secret,
		SkewSec:      60,
		Now:          now,
		MaxBodyBytes: 16,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	body := bytes.Repeat([]byte("A"), 32) // 2x the cap
	// Sign honestly so we exercise the size cap, not the signature path.
	sig := Sign(secret, "POST", "/v1/archive", body, 1715000000)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/archive", bytes.NewReader(body))
	req.Header.Set(HeaderTimestamp, "1715000000")
	req.Header.Set(HeaderSignature, sig)
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
	assert.False(t, called, "downstream handler must not be invoked when body exceeds MaxBodyBytes")
}

// TestVerifierAcceptsBodyAtBoundary documents that http.MaxBytesReader treats
// the cap as inclusive — a body of exactly N bytes is allowed, only N+1 bytes
// triggers the *http.MaxBytesError.
func TestVerifierAcceptsBodyAtBoundary(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	now := func() time.Time { return time.Unix(1715000000, 0) }
	h := Verifier(VerifierOpts{
		Secret:       secret,
		SkewSec:      60,
		Now:          now,
		MaxBodyBytes: 16,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		assert.Len(t, got, 16, "downstream must see the full boundary body")
		w.WriteHeader(200)
	}))
	body := bytes.Repeat([]byte("A"), 16) // exactly the cap
	sig := Sign(secret, "POST", "/v1/archive", body, 1715000000)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/archive", bytes.NewReader(body))
	req.Header.Set(HeaderTimestamp, "1715000000")
	req.Header.Set(HeaderSignature, sig)
	h.ServeHTTP(rr, req)
	assert.Equal(t, 200, rr.Code, "body of exactly MaxBodyBytes must be accepted")
}

func TestVerifierRejectsBadSignature(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	now := func() time.Time { return time.Unix(1715000000, 0) }
	h := Verifier(VerifierOpts{Secret: secret, SkewSec: 60, Now: now})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/archive", bytes.NewReader([]byte("hi")))
	req.Header.Set(HeaderTimestamp, "1715000000")
	req.Header.Set(HeaderSignature, "deadbeef")
	h.ServeHTTP(rr, req)
	assert.Equal(t, 401, rr.Code)
}

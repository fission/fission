// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spoolTempDir points os.CreateTemp at a fresh per-test directory so the
// tests can assert exactly which temp files the spool created and that
// cleanup removed them.
func spoolTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func requireEmptyDir(t *testing.T, dir, msg string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, msg)
}

func TestSpoolBodyBoundary(t *testing.T) {
	dir := spoolTempDir(t)

	// Exactly at the threshold: stays in memory, no temp file.
	atLimit, err := spoolBody(strings.NewReader("12345678"), 8)
	require.NoError(t, err)
	assert.Nil(t, atLimit.file, "a body of exactly threshold bytes must stay in memory")
	assert.Equal(t, BodyHashHex([]byte("12345678")), atLimit.hashHex)
	requireEmptyDir(t, dir, "the in-memory path must not create temp files")

	// One byte over: spills to a temp file, same hash as hashing in memory.
	over, err := spoolBody(strings.NewReader("123456789"), 8)
	require.NoError(t, err)
	require.NotNil(t, over.file, "a body of threshold+1 bytes must spill to disk")
	assert.Equal(t, BodyHashHex([]byte("123456789")), over.hashHex,
		"the streamed hash must equal the in-memory hash")
	rd, err := over.reader()
	require.NoError(t, err)
	got, err := io.ReadAll(rd)
	require.NoError(t, err)
	assert.Equal(t, "123456789", string(got), "the spooled reader must replay the full body")
	over.cleanup()
	over.cleanup() // idempotent
	requireEmptyDir(t, dir, "cleanup must remove the temp file")
}

// TestVerifierSpoolsLargeBody pins the bounded-memory contract: with
// SpoolThresholdBytes set, an over-threshold body still verifies, reaches
// the handler intact from the temp file, and the temp file is gone once
// the request completes.
func TestVerifierSpoolsLargeBody(t *testing.T) {
	dir := spoolTempDir(t)
	secret := []byte("test-secret-must-be-32-bytes-min")
	now := func() time.Time { return time.Unix(1715000000, 0) }
	body := bytes.Repeat([]byte("A"), 64)

	var gotBody []byte
	h := Verifier(VerifierOpts{Secret: secret, SkewSec: 60, Now: now, SpoolThresholdBytes: 8})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var err error
			gotBody, err = io.ReadAll(r.Body)
			require.NoError(t, err)
			_ = r.Body.Close() // a handler's defer Close must not break cleanup
			w.WriteHeader(200)
		}))

	sig := Sign(secret, "POST", "/v1/archive", body, 1715000000)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/archive", bytes.NewReader(body))
	req.Header.Set(HeaderTimestamp, "1715000000")
	req.Header.Set(HeaderSignature, sig)
	h.ServeHTTP(rr, req)

	assert.Equal(t, 200, rr.Code)
	assert.Equal(t, body, gotBody, "the handler must read the full body back from the spool")
	requireEmptyDir(t, dir, "the spool temp file must be removed after the handler returns")
}

// TestVerifierSpoolSmallBodyStaysInMemory: under the threshold the spool
// path must behave exactly like the historical in-memory path — no files.
func TestVerifierSpoolSmallBodyStaysInMemory(t *testing.T) {
	dir := spoolTempDir(t)
	secret := []byte("test-secret-must-be-32-bytes-min")
	now := func() time.Time { return time.Unix(1715000000, 0) }
	body := []byte("small")

	h := Verifier(VerifierOpts{Secret: secret, SkewSec: 60, Now: now, SpoolThresholdBytes: 1024})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			assert.Equal(t, body, got)
			w.WriteHeader(200)
		}))

	sig := Sign(secret, "POST", "/v1/archive", body, 1715000000)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/archive", bytes.NewReader(body))
	req.Header.Set(HeaderTimestamp, "1715000000")
	req.Header.Set(HeaderSignature, sig)
	h.ServeHTTP(rr, req)

	assert.Equal(t, 200, rr.Code)
	requireEmptyDir(t, dir, "an under-threshold body must never touch disk")
}

// TestVerifierSpoolCleansUpOnBadSignature: a spilled body whose signature
// does not verify must be rejected AND leave no temp file behind.
func TestVerifierSpoolCleansUpOnBadSignature(t *testing.T) {
	dir := spoolTempDir(t)
	secret := []byte("test-secret-must-be-32-bytes-min")
	now := func() time.Time { return time.Unix(1715000000, 0) }
	body := bytes.Repeat([]byte("B"), 64)

	called := false
	h := Verifier(VerifierOpts{Secret: secret, SkewSec: 60, Now: now, SpoolThresholdBytes: 8})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(200)
		}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/archive", bytes.NewReader(body))
	req.Header.Set(HeaderTimestamp, "1715000000")
	req.Header.Set(HeaderSignature, "deadbeef")
	h.ServeHTTP(rr, req)

	assert.Equal(t, 401, rr.Code)
	assert.False(t, called, "the handler must not run on a signature mismatch")
	requireEmptyDir(t, dir, "a rejected request's spool file must be removed")
}

// TestVerifierSpoolIOFailureIs500 pins the fault classification: when the
// spool cannot write its temp file (read-only temp dir — the shape of a
// full disk or readOnlyRootFilesystem), the request is answered 500, not
// 401, so operators chase the disk fault rather than HMAC configuration.
// The classification happens during the pre-verification drain, so it
// applies regardless of the signature's validity; this test signs honestly
// only so a fixed 401 could not pass for the wrong reason.
func TestVerifierSpoolIOFailureIs500(t *testing.T) {
	roDir := t.TempDir()
	require.NoError(t, os.Chmod(roDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })
	t.Setenv("TMPDIR", roDir)

	secret := []byte("test-secret-must-be-32-bytes-min")
	now := func() time.Time { return time.Unix(1715000000, 0) }
	body := bytes.Repeat([]byte("D"), 64)

	called := false
	h := Verifier(VerifierOpts{Secret: secret, SkewSec: 60, Now: now, SpoolThresholdBytes: 8})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(200)
		}))

	sig := Sign(secret, "POST", "/v1/archive", body, 1715000000)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/archive", bytes.NewReader(body))
	req.Header.Set(HeaderTimestamp, "1715000000")
	req.Header.Set(HeaderSignature, sig)
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code,
		"a spool-storage fault is the server's problem, not an auth failure")
	assert.False(t, called, "the handler must not run when the body could not be spooled")
}

// TestVerifierSpoolRejectsOversize: MaxBodyBytes still caps the request when
// spooling — the drain hits the MaxBytesReader limit mid-spool, rejects with
// 413, and leaves no temp file behind.
func TestVerifierSpoolRejectsOversize(t *testing.T) {
	dir := spoolTempDir(t)
	secret := []byte("test-secret-must-be-32-bytes-min")
	now := func() time.Time { return time.Unix(1715000000, 0) }
	body := bytes.Repeat([]byte("C"), 64)

	called := false
	h := Verifier(VerifierOpts{
		Secret:              secret,
		SkewSec:             60,
		Now:                 now,
		MaxBodyBytes:        32,
		SpoolThresholdBytes: 8,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))

	sig := Sign(secret, "POST", "/v1/archive", body, 1715000000)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/archive", bytes.NewReader(body))
	req.Header.Set(HeaderTimestamp, "1715000000")
	req.Header.Set(HeaderSignature, sig)
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
	assert.False(t, called, "the handler must not run when the body exceeds MaxBodyBytes")
	requireEmptyDir(t, dir, "an oversize request must not leave a spool file behind")
}

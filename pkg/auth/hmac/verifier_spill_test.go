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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const spillTestSecret = "test-secret-must-be-32-bytes-min"

func spillNow() time.Time { return time.Unix(1715000000, 0) }

// assertNoSpillLeak fails if any verifier spill temp file is left in os.TempDir()
// (which the caller has pointed at a per-test dir via TMPDIR).
func assertNoSpillLeak(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "fission-hmac-body-", "spill temp file leaked")
	}
}

func signedReq(method, uri string, body []byte, ts int64, sigBody []byte) *http.Request {
	req := httptest.NewRequest(method, uri, bytes.NewReader(body))
	sig := Sign([]byte(spillTestSecret), method, req.URL.RequestURI(), sigBody, ts)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, sig)
	return req
}

func TestVerifierSpillAcceptsLargeSignedBody(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	body := bytes.Repeat([]byte("z"), 4096) // > SpillThreshold below
	var gotBody []byte
	h := Verifier(VerifierOpts{
		Secret: []byte(spillTestSecret), SkewSec: 60, Now: spillNow, SpillThreshold: 1024,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately do NOT close r.Body — the real uploadHandler doesn't, and
		// net/http closes the ORIGINAL request body, not the re-injected
		// spillReader. The verifier must clean up its own temp file regardless.
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, signedReq("POST", "/v1/archive", body, 1715000000, body))
	require.Equal(t, 200, rr.Code)
	assert.Equal(t, body, gotBody, "downstream must re-read the spilled body byte-for-byte")
	assertNoSpillLeak(t)
}

func TestVerifierSpillRejectsTamperedBody(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	signed := bytes.Repeat([]byte("z"), 4096)
	tampered := bytes.Repeat([]byte("y"), 4096)
	h := Verifier(VerifierOpts{
		Secret: []byte(spillTestSecret), SkewSec: 60, Now: spillNow, SpillThreshold: 1024,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	rr := httptest.NewRecorder()
	// sign over `signed` but send `tampered`
	h.ServeHTTP(rr, signedReq("POST", "/v1/archive", tampered, 1715000000, signed))
	require.Equal(t, 401, rr.Code)
	assertNoSpillLeak(t)
}

func TestVerifierSpillRejectsOversizeBody(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	body := bytes.Repeat([]byte("z"), 4096)
	h := Verifier(VerifierOpts{
		Secret: []byte(spillTestSecret), SkewSec: 60, Now: spillNow,
		SpillThreshold: 1024, MaxBodyBytes: 2048,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, signedReq("POST", "/v1/archive", body, 1715000000, body))
	require.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
	assertNoSpillLeak(t)
}

// TestVerifierSpillSmallBodyStaysInMemory exercises the spill path with a body
// under the threshold: it verifies (no temp file involved) and re-reads.
func TestVerifierSpillSmallBodyStaysInMemory(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	body := []byte("small")
	var gotBody []byte
	h := Verifier(VerifierOpts{
		Secret: []byte(spillTestSecret), SkewSec: 60, Now: spillNow, SpillThreshold: 1 << 20,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, signedReq("POST", "/v1/archive", body, 1715000000, body))
	require.Equal(t, 200, rr.Code)
	assert.Equal(t, body, gotBody)
	assert.Equal(t, "small", strings.TrimSpace(string(gotBody)))
	assertNoSpillLeak(t)
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"errors"
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

// TestSignerRepopulatesGetBody pins the retry contract: the signer buffers
// and replaces the request body, so it must also repopulate GetBody — the
// hook net/http and pkg/utils/httpretry use to rewind a request per
// attempt (both refuse to retry when it is nil, and a naive re-submission
// of the same *http.Request would re-sign the consumed — empty — reader,
// which the verifier accepts).
func TestSignerRepopulatesGetBody(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")

	var gotBodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ts, err := strconv.ParseInt(r.Header.Get(HeaderTimestamp), 10, 64)
		require.NoError(t, err)
		require.True(t, Verify(secret, r.Method, r.URL.Path, body, ts, r.Header.Get(HeaderSignature)),
			"every attempt must carry a signature valid for the body it sent")
		gotBodies = append(gotBodies, string(body))
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	signer := NewSigner(secret, http.DefaultTransport, time.Now)

	// Build a request whose GetBody is nil, like a caller handing the
	// transport a bare reader net/http cannot rewind on its own.
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/archive", nil)
	require.NoError(t, err)
	req.Body = io.NopCloser(strings.NewReader("payload"))
	require.Nil(t, req.GetBody, "precondition: the raw request must not be rewindable")

	resp, err := signer.RoundTrip(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	// The signer consumed the original reader, so it must have left a
	// rewind hook behind for any retry layer above it.
	require.NotNil(t, req.GetBody, "signer must repopulate GetBody after buffering the body")

	// Simulate a retry the way httpretry does: rewind Body via GetBody,
	// then run the same request through the signer again. The second
	// attempt must carry the full payload, freshly signed — not an empty
	// body from the consumed reader.
	req.Body, err = req.GetBody()
	require.NoError(t, err)
	resp, err = signer.RoundTrip(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.Equal(t, []string{"payload", "payload"}, gotBodies,
		"the retried attempt must re-send (and re-sign) the original body, never an empty one")
}

// TestSignerStreamsHashViaGetBody pins the no-buffer contract: when a
// request is rewindable (GetBody set — net/http populates it for requests
// built from bytes/strings readers, which covers every JSON client and the
// storagesvc archive upload), the signer must stream the body hash from a
// fresh GetBody reader and leave Body/GetBody untouched, rather than
// buffering a second full copy of the body just to hash it.
func TestSignerStreamsHashViaGetBody(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")

	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ts, err := strconv.ParseInt(r.Header.Get(HeaderTimestamp), 10, 64)
		require.NoError(t, err)
		require.True(t, Verify(secret, r.Method, r.URL.Path, body, ts, r.Header.Get(HeaderSignature)),
			"the streamed hash must sign the exact bytes on the wire")
		gotBody = string(body)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/archive", strings.NewReader("payload"))
	require.NoError(t, err)
	require.NotNil(t, req.GetBody, "precondition: net/http must have made the request rewindable")

	// Count GetBody calls and remember the original hooks so we can assert
	// the signer read a fresh reader once and replaced nothing.
	origBody := req.Body
	origGetBody := req.GetBody
	getBodyCalls := 0
	req.GetBody = func() (io.ReadCloser, error) {
		getBodyCalls++
		return origGetBody()
	}

	signer := NewSigner(secret, http.DefaultTransport, time.Now)
	resp, err := signer.RoundTrip(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "payload", gotBody, "the transport must still stream the original body")
	assert.Equal(t, 1, getBodyCalls, "the hash must be streamed from exactly one fresh GetBody reader")
	assert.True(t, req.Body == origBody, "the signer must not replace a rewindable request's Body")
}

// closeTrackingBody counts Close calls, for pinning the close-exactly-once
// contract on the signer's error paths (io.Closer declares behavior after
// the first Close undefined, so a double-close is as much a bug as a leak).
type closeTrackingBody struct {
	io.Reader
	closes *int
}

func (b *closeTrackingBody) Close() error {
	*b.closes++
	return nil
}

// errReader fails every Read, to force the buffering fallback's error path.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// roundTripperFunc adapts a func to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestSignerClosesBodyOnHashError pins the RoundTripper contract on the
// streaming branch's error path: when GetBody fails, the inner transport
// never runs, so the signer itself must close the request body — otherwise
// a retry loop against a failing GetBody leaks one fd per attempt. Exactly
// one Close: a second would be undefined for non-idempotent closers.
func TestSignerClosesBodyOnHashError(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")

	closes := 0
	req, err := http.NewRequest(http.MethodPost, "http://router.invalid/fission-function/f", nil)
	require.NoError(t, err)
	req.Body = &closeTrackingBody{Reader: strings.NewReader("payload"), closes: &closes}
	req.GetBody = func() (io.ReadCloser, error) {
		return nil, errors.New("getbody failed")
	}

	signer := NewSigner(secret, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("the inner transport must not run when hashing fails")
		return nil, nil
	}), time.Now)

	_, err = signer.RoundTrip(req)
	require.Error(t, err)
	assert.Equal(t, 1, closes, "the signer must close the body exactly once when it errors before the transport runs")
}

// TestSignerClosesBodyExactlyOnceOnReadError pins the same contract on the
// buffering fallback: draining a failing body already closes the original,
// and no layer above may close it a second time.
func TestSignerClosesBodyExactlyOnceOnReadError(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")

	closes := 0
	req, err := http.NewRequest(http.MethodPost, "http://router.invalid/fission-function/f", nil)
	require.NoError(t, err)
	req.Body = &closeTrackingBody{Reader: errReader{err: errors.New("read failed")}, closes: &closes}
	require.Nil(t, req.GetBody, "precondition: the buffering fallback must be exercised")

	signer := NewSigner(secret, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("the inner transport must not run when draining fails")
		return nil, nil
	}), time.Now)

	_, err = signer.RoundTrip(req)
	require.Error(t, err)
	assert.Equal(t, 1, closes, "a failed drain must close the original body exactly once, never twice")
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

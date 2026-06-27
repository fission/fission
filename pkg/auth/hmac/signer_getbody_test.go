// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingRT captures the request the signer forwards.
type recordingRT struct {
	body io.ReadCloser
	sig  string
	ts   string
}

func (r *recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.body = req.Body
	r.sig = req.Header.Get(HeaderSignature)
	r.ts = req.Header.Get(HeaderTimestamp)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(nil))}, nil
}

type trackReadCloser struct{ r io.Reader }

func (t *trackReadCloser) Read(p []byte) (int, error) { return t.r.Read(p) }
func (t *trackReadCloser) Close() error               { return nil }

// TestSignerHashesViaGetBodyAndLeavesBodyUntouched: when GetBody is set, the
// signer hashes a fresh copy from GetBody and forwards the ORIGINAL req.Body
// untouched (no io.ReadAll, no re-injection), so a streaming body stays
// streaming. Signature is identical to the slice form.
func TestSignerHashesViaGetBodyAndLeavesBodyUntouched(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	payload := []byte("streamed-payload-bytes")
	now := func() time.Time { return time.Unix(1715000000, 0) }

	orig := &trackReadCloser{r: bytes.NewReader(payload)}
	req, err := http.NewRequest("POST", "http://example/v1/archive?id=x", orig)
	require.NoError(t, err)
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }

	rt := &recordingRT{}
	resp, err := NewSigner(secret, rt, now).RoundTrip(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.Same(t, orig, rt.body, "signer must forward the original body untouched, not a buffered copy")
	assert.Equal(t, Sign(secret, "POST", "/v1/archive?id=x", payload, 1715000000), rt.sig)
	assert.Equal(t, "1715000000", rt.ts)
}

// TestSignerBuffersWhenNoGetBody: with no GetBody (a bare ReadCloser), the
// signer keeps the buffered path — transport still receives the full body and
// the signature is correct.
func TestSignerBuffersWhenNoGetBody(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	payload := []byte("buffered-payload")
	now := func() time.Time { return time.Unix(1715000000, 0) }

	req, err := http.NewRequest("POST", "http://example/v1/archive", io.NopCloser(bytes.NewReader(payload)))
	require.NoError(t, err)
	req.GetBody = nil // force the no-GetBody path

	rt := &recordingRT{}
	resp, err := NewSigner(secret, rt, now).RoundTrip(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	got, _ := io.ReadAll(rt.body)
	assert.Equal(t, payload, got, "transport must still receive the full body")
	assert.Equal(t, Sign(secret, "POST", "/v1/archive", payload, 1715000000), rt.sig)
}

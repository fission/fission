// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSignerVerifierInterop is the backward-compatibility proof for a rolling
// upgrade: every combination of a {buffered, GetBody-streaming} signer talking
// to a {in-memory, spill} verifier interoperates and delivers the body intact.
// The wire signature is identical across all four, so old/new clients and
// servers mix freely.
func TestSignerVerifierInterop(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")
	payload := bytes.Repeat([]byte("interop-"), 8192) // 64 KiB, above the spill threshold below

	cases := []struct {
		name        string
		spill       int64
		withGetBody bool
	}{
		{"buffered signer -> in-memory verifier (old client, old server)", 0, false},
		{"buffered signer -> spill verifier (old client, new server)", 4096, false},
		{"getbody signer -> in-memory verifier (new client, old server)", 0, true},
		{"getbody signer -> spill verifier (new client, new server)", 4096, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			srv := httptest.NewServer(Verifier(VerifierOpts{
				Secret: secret, SkewSec: 60, Now: time.Now, SpillThreshold: tc.spill,
			})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(200)
			})))
			defer srv.Close()

			client := &http.Client{Transport: NewSigner(secret, http.DefaultTransport, time.Now)}

			var req *http.Request
			var err error
			if tc.withGetBody {
				req, err = http.NewRequest(http.MethodPost, srv.URL+"/v1/archive", bytes.NewReader(payload))
			} else {
				req, err = http.NewRequest(http.MethodPost, srv.URL+"/v1/archive", io.NopCloser(bytes.NewReader(payload)))
				if req != nil {
					req.GetBody = nil // force the buffered signer path
					req.ContentLength = int64(len(payload))
				}
			}
			require.NoError(t, err)
			require.Equal(t, tc.withGetBody, req.GetBody != nil, "test precondition: GetBody presence")

			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, 200, resp.StatusCode)
			assert.Equal(t, payload, gotBody, "body must arrive intact regardless of signer/verifier combination")
		})
	}
}

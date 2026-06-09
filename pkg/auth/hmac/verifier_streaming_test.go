// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestVerifierPreservesHijackerAndFlusher locks in the streaming invariant
// (RFC-0008 P4): the verifier must pass the original http.ResponseWriter through
// so it still satisfies http.Hijacker (WebSocket upgrade) and http.Flusher
// (SSE/chunked). Driven through a real server so w is a genuine Hijacker/Flusher.
func TestVerifierPreservesHijackerAndFlusher(t *testing.T) {
	t.Parallel()

	const ts int64 = 1715000000
	now := func() time.Time { return time.Unix(ts, 0) }

	// inner reports whether the ResponseWriter it received supports the two
	// interfaces streaming relies on.
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, okHijack := w.(http.Hijacker)
		_, okFlush := w.(http.Flusher)
		fmt.Fprintf(w, "hijack=%v flush=%v", okHijack, okFlush)
	})

	cases := []struct {
		name string
		opts VerifierOpts
		sign bool
	}{
		{"pass-through (empty secret)", VerifierOpts{Secret: nil, Now: now}, false},
		{"enforced + signed", VerifierOpts{Secret: []byte("master"), SkewSec: 60, Now: now}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(Verifier(tc.opts)(inner))
			defer srv.Close()

			req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
			require.NoError(t, err)
			if tc.sign {
				sig := Sign(tc.opts.Secret, http.MethodGet, "/", nil, ts)
				req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
				req.Header.Set(HeaderSignature, sig)
			}

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)

			body := make([]byte, 64)
			n, _ := resp.Body.Read(body)
			require.Equal(t, "hijack=true flush=true", string(body[:n]),
				"verifier must not wrap the ResponseWriter (breaks WebSocket/SSE)")
		})
	}
}

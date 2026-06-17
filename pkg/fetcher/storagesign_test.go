// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

// captureRT records the request it is asked to send and short-circuits it.
type captureRT struct{ req *http.Request }

func (c *captureRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.req = r
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
}

// TestStorageSigningTransport pins the fetcher's storagesvc signing wire path —
// the complement of storagesvc's ServiceVerifierNamespaceFromHeader, which until
// now was only ever proven separately. Under dynamic tenancy a tenant fetcher
// holds only its per-namespace storage key (never the master) and must both sign
// with it AND set the namespace header so storagesvc derives the matching key.
func TestStorageSigningTransport(t *testing.T) {
	master := []byte("master-bytes-for-storagesign-test")

	newReq := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "http://storagesvc/v1/archive", strings.NewReader("archive-body"))
	}

	t.Run("namespace key signs with the ns key and sets the header → verifies", func(t *testing.T) {
		// Hex-encoded exactly as the tenant controller provisions it into the keys
		// Secret (env-var transport must be UTF-8); storageSigningTransport hex-decodes.
		t.Setenv("FISSION_STORAGE_KEY", hmacauth.EncodeKeyForEnv(hmacauth.DeriveServiceKeyNS(master, hmacauth.ServiceStoragesvc, "team-a")))
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "") // never the master in a tenant pod

		cap := &captureRT{}
		_, err := storageSigningTransport(cap, "team-a").RoundTrip(newReq())
		require.NoError(t, err)
		require.NotNil(t, cap.req)
		assert.Equal(t, "team-a", cap.req.Header.Get(hmacauth.HeaderNamespace), "must set the namespace header")

		// The real storagesvc verifier must accept it — locks both halves together.
		verifier := hmacauth.ServiceVerifierNamespaceFromHeader(master, nil, hmacauth.ServiceStoragesvc, hmacauth.VerifierOpts{SkewSec: 60})
		rr := httptest.NewRecorder()
		verifier(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(rr, cap.req)
		assert.Equal(t, http.StatusOK, rr.Code, "ns-signed request with header must verify")
	})

	t.Run("master fallback signs master-derived with no namespace header", func(t *testing.T) {
		t.Setenv("FISSION_STORAGE_KEY", "")
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET", string(master))

		cap := &captureRT{}
		_, err := storageSigningTransport(cap, "team-a").RoundTrip(newReq())
		require.NoError(t, err)
		require.NotNil(t, cap.req)
		assert.Empty(t, cap.req.Header.Get(hmacauth.HeaderNamespace), "master-derived path must not claim a namespace")
		// storagesvc dual-accepts a master-derived signature (no header).
		verifier := hmacauth.ServiceVerifierNamespaceFromHeader(master, nil, hmacauth.ServiceStoragesvc, hmacauth.VerifierOpts{SkewSec: 60})
		rr := httptest.NewRecorder()
		verifier(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(rr, cap.req)
		assert.Equal(t, http.StatusOK, rr.Code, "master-derived request must verify (dual-accept)")
	})

	t.Run("no key and no master → unsigned pass-through", func(t *testing.T) {
		t.Setenv("FISSION_STORAGE_KEY", "")
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "")

		cap := &captureRT{}
		_, err := storageSigningTransport(cap, "team-a").RoundTrip(newReq())
		require.NoError(t, err)
		require.NotNil(t, cap.req)
		assert.Empty(t, cap.req.Header.Get(hmacauth.HeaderNamespace))
		assert.Empty(t, cap.req.Header.Get("Authorization"), "internalAuth-disabled must send no auth header")
	})
}

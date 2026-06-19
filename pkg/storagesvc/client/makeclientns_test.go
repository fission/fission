// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/utils/httpmux"
)

// TestMakeClientNS proves the namespace-scoped client signs with the per-namespace
// derived storagesvc key and sets the namespace header, so the real verifier both
// accepts the request and attributes it to that namespace — the property the
// storagesvc archive authorization relies on. An empty namespace falls back to a
// master-derived, unscoped client (principal "").
func TestMakeClientNS(t *testing.T) {
	master := []byte("storagesvc-test-master-key-long-enough")

	var principal string
	var sawRequest bool
	m := httpmux.New(httpmux.WithMiddleware(hmacauth.ServiceVerifierNamespaceFromHeader(master, nil, hmacauth.ServiceStoragesvc,
		hmacauth.VerifierOpts{SkewSec: 60})))
	m.HandleFunc("/v1/archive", func(w http.ResponseWriter, req *http.Request) {
		principal, _ = hmacauth.AuthenticatedNamespace(req.Context())
		sawRequest = true
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodHead)

	srv := httptest.NewServer(m.Handler())
	defer srv.Close()

	t.Run("namespace-scoped client is attributed to its namespace", func(t *testing.T) {
		principal, sawRequest = "", false
		c := MakeClientNS(srv.URL, master, "team-a")
		resp, err := c.Info(t.Context(), "some-archive-id")
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		require.True(t, sawRequest)
		assert.Equal(t, "team-a", principal, "verifier must attribute the request to team-a")
	})

	t.Run("empty namespace falls back to unrestricted (master) principal", func(t *testing.T) {
		principal, sawRequest = "x", false
		c := MakeClientNS(srv.URL, master, "")
		resp, err := c.Info(t.Context(), "some-archive-id")
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		require.True(t, sawRequest)
		assert.Empty(t, principal, "master principal is unrestricted")
	})
}

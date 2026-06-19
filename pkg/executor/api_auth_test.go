// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/utils/httpmux"
)

// TestExecutorVerifierMiddlewareWiring guards the wiring contract in
// (*Executor).GetHandler: the HMAC verifier wraps the mux as the outermost
// middleware, /healthz bypasses signing, and unsigned requests to a non-bypass
// path are rejected with 401.
//
// This mirrors the precedent in pkg/storagesvc/storagesvc_auth_test.go and runs
// without requiring a full Executor instance.
func TestExecutorVerifierMiddlewareWiring(t *testing.T) {
	master := []byte("test-master-must-be-32-bytes-min!!")

	m := httpmux.New(httpmux.WithMiddleware(hmacauth.ServiceVerifier(master, nil, hmacauth.ServiceExecutor, hmacauth.VerifierOpts{
		SkewSec:      60,
		Bypass:       []string{"/healthz"},
		MaxBodyBytes: hmacauth.DefaultMaxBodyBytes,
	})))
	m.HandleFunc("/v2/getServiceForFunction", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodPost)
	m.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodGet)
	r := m.Handler()

	t.Run("rejects unsigned /v2/getServiceForFunction", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v2/getServiceForFunction", nil)
		r.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusUnauthorized, rr.Code,
			"executor must 401 unsigned requests when a master is configured")
	})

	t.Run("accepts unsigned /healthz", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		r.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code,
			"executor /healthz must remain unsigned for kubelet probes")
	})

	t.Run("empty master short-circuits to pass-through", func(t *testing.T) {
		m2 := httpmux.New(httpmux.WithMiddleware(hmacauth.ServiceVerifier(nil, nil, hmacauth.ServiceExecutor, hmacauth.VerifierOpts{
			SkewSec:      60,
			Bypass:       []string{"/healthz"},
			MaxBodyBytes: hmacauth.DefaultMaxBodyBytes,
		})))
		m2.HandleFunc("/v2/getServiceForFunction", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).Methods(http.MethodPost)

		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v2/getServiceForFunction", nil)
		m2.Handler().ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code,
			"empty master must short-circuit the verifier and pass requests through")
	})
}

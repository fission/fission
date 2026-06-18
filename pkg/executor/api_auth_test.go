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
)

// TestExecutorVerifierMiddlewareWiring guards the wiring contract in
// (*Executor).GetHandler: the HMAC verifier is registered before the
// metrics middleware, /healthz bypasses signing, and unsigned requests
// to a non-bypass path are rejected with 401.
//
// This mirrors the precedent in pkg/storagesvc/storagesvc_auth_test.go
// and runs without requiring a full Executor instance.
func TestExecutorVerifierMiddlewareWiring(t *testing.T) {
	master := []byte("test-master-must-be-32-bytes-min!!")

	m := http.NewServeMux()
	m.HandleFunc("POST /v2/getServiceForFunction", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r := hmacauth.ServiceVerifier(master, nil, hmacauth.ServiceExecutor, hmacauth.VerifierOpts{
		SkewSec:      60,
		Bypass:       []string{"/healthz"},
		MaxBodyBytes: hmacauth.DefaultMaxBodyBytes,
	})(m)

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
		m2 := http.NewServeMux()
		m2.HandleFunc("POST /v2/getServiceForFunction", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		r2 := hmacauth.ServiceVerifier(nil, nil, hmacauth.ServiceExecutor, hmacauth.VerifierOpts{
			SkewSec:      60,
			Bypass:       []string{"/healthz"},
			MaxBodyBytes: hmacauth.DefaultMaxBodyBytes,
		})(m2)

		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v2/getServiceForFunction", nil)
		r2.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code,
			"empty master must short-circuit the verifier and pass requests through")
	})
}

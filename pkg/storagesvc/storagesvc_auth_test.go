// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

// TestVerifierMiddlewareWiring exercises the middleware chain that
// (ss *StorageService).Start would build, without standing up the full
// service. It uses ServiceVerifier with ServiceStoragesvc to mirror
// the production wiring exactly — a regression in the per-service key
// derivation would surface here, not just in the lower-level
// hmac/verifier_test.go suite.
func TestVerifierMiddlewareWiring(t *testing.T) {
	master := []byte("test-master-must-be-32-bytes-min")

	r := mux.NewRouter()
	r.Use(hmacauth.ServiceVerifier(master, nil, hmacauth.ServiceStoragesvc, hmacauth.VerifierOpts{
		SkewSec: 60,
		Bypass:  []string{"/healthz"},
	}))
	r.HandleFunc("/v1/archive", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodGet)
	r.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodGet)

	t.Run("rejects unsigned /v1/archive", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/archive", nil)
		r.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("accepts unsigned /healthz", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		r.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
	})
}

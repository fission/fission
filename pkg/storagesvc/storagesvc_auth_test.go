/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package storagesvc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

// TestVerifierMiddlewareWiring exercises just the middleware chain that
// (ss *StorageService).Start would build, without standing up the full
// service. It guards against regressions in the auth wiring even when no
// kind cluster is available for the integration test in
// test/integration/suites/common/storagesvc_auth_test.go.
func TestVerifierMiddlewareWiring(t *testing.T) {
	secret := []byte("test-secret-must-be-32-bytes-min")

	r := mux.NewRouter()
	r.Use(hmacauth.Verifier(hmacauth.VerifierOpts{
		Secret:  secret,
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

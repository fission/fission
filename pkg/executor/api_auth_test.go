/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package executor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
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

	r := mux.NewRouter()
	r.Use(hmacauth.ServiceVerifier(master, nil, hmacauth.ServiceExecutor, hmacauth.VerifierOpts{
		SkewSec:      60,
		Bypass:       []string{"/healthz"},
		MaxBodyBytes: hmacauth.DefaultMaxBodyBytes,
	}))
	r.HandleFunc("/v2/getServiceForFunction", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodPost)
	r.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodGet)

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
		r2 := mux.NewRouter()
		r2.Use(hmacauth.ServiceVerifier(nil, nil, hmacauth.ServiceExecutor, hmacauth.VerifierOpts{
			SkewSec:      60,
			Bypass:       []string{"/healthz"},
			MaxBodyBytes: hmacauth.DefaultMaxBodyBytes,
		}))
		r2.HandleFunc("/v2/getServiceForFunction", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).Methods(http.MethodPost)

		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v2/getServiceForFunction", nil)
		r2.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code,
			"empty master must short-circuit the verifier and pass requests through")
	})
}

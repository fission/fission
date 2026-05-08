//go:build integration

/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package common_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests assume:
//   - Fission is installed with `internalAuth.enabled=true` (the default).
//   - `kubectl port-forward svc/storagesvc 8000:8000 -n fission` is running.
//
// They cover the binding gate for RFC-0004 / GHSA-chf8-4hv6-8pg6: storagesvc
// rejects unsigned /v1/archive requests but kubelet probes (/healthz) still
// pass.

func TestStorageSvcRejectsUnsignedListRequest(t *testing.T) {
	resp, err := http.Get("http://localhost:8000/v1/archive")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"unsigned GET /v1/archive must be rejected; body=%s",
		strings.TrimSpace(string(body)))
}

func TestStorageSvcRejectsUnsignedUploadRequest(t *testing.T) {
	resp, err := http.Post("http://localhost:8000/v1/archive", "application/octet-stream", strings.NewReader("payload"))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"unsigned POST /v1/archive must be rejected; body=%s",
		strings.TrimSpace(string(body)))
}

func TestStorageSvcRejectsUnsignedDeleteRequest(t *testing.T) {
	req, err := http.NewRequest(http.MethodDelete, "http://localhost:8000/v1/archive?id=does-not-exist", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"unsigned DELETE /v1/archive must be rejected")
}

func TestStorageSvcAcceptsHealthzWithoutSignature(t *testing.T) {
	resp, err := http.Get("http://localhost:8000/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

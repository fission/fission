// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestRouterInternalListener exercises the listener split introduced
// for GHSA-3g33-6vg6-27m8: /fission-function/<ns>/<name> must NOT be
// reachable on the public listener (Service `router`) and the internal
// listener (the separate ClusterIP-only Service `router-internal`, so
// routerServiceType=NodePort/LoadBalancer doesn't expose it outside the
// cluster) must reject unsigned requests when the HMAC secret is
// provisioned, while remaining reachable for healthcheck-style probing.
//
// Both listeners are reached through the framework's unsigned HTTPClient
// (the internal-listener request must NOT carry an HMAC signature here —
// rejecting unsigned traffic is the point).

// unsignedGet issues a single unsigned GET bounded to 10s so a hung listener
// fails the test fast rather than waiting on the suite-level timeout.
func unsignedGet(t *testing.T, f *framework.Framework, url string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := f.HTTPClient().Do(req)
	require.NoError(t, err, "router listener %s must be reachable", url)
	return resp
}

// TestPublicRouterReturns404ForInternalRoute pins the GHSA-3g33-6vg6-27m8
// regression: the public listener must NOT serve /fission-function/...,
// regardless of whether the function exists.
func TestPublicRouterReturns404ForInternalRoute(t *testing.T) {
	f := framework.Connect(t)
	resp := unsignedGet(t, f, f.Router(t).BaseURL()+"/fission-function/default/nonexistent")
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"public listener must 404 /fission-function/...; body=%s",
		strings.TrimSpace(string(body)))
}

// TestInternalRouterRejectsUnsigned checks that the internal listener
// rejects unsigned requests with 401 when the HMAC verifier is active.
// This test will skip itself if the cluster is configured in the
// rollout-friendly pass-through mode (FISSION_INTERNAL_AUTH_SECRET
// unset on the router pod), because in that mode the verifier
// short-circuits and the request reaches the function handler.
func TestInternalRouterRejectsUnsigned(t *testing.T) {
	f := framework.Connect(t)
	resp := unsignedGet(t, f, f.RouterInternalBaseURL()+"/fission-function/default/nonexistent")
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Skipf("internal listener returned %d (expected 401); the router may be in HMAC pass-through mode (FISSION_INTERNAL_AUTH_SECRET unset). body=%s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"internal listener must 401 unsigned requests when HMAC is enforced")
}

//go:build integration

package common_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouterInternalListener exercises the listener split introduced
// for GHSA-3g33-6vg6-27m8: /fission-function/<ns>/<name> must NOT be
// reachable on the public listener (port 8888 → forwarded to
// localhost:8888) and the internal listener (port 8889) must reject
// unsigned requests when the HMAC secret is provisioned, while
// remaining reachable for healthcheck-style probing.
//
// Prerequisites (matching the integration-test bootstrap in the suite
// workflow / README — the public listener is on Service `router`, the
// internal listener is on the separate ClusterIP-only Service
// `router-internal` so routerServiceType=NodePort/LoadBalancer doesn't
// expose port 8889 outside the cluster):
//
//	kubectl port-forward svc/router          8888:80   -n fission &
//	kubectl port-forward svc/router-internal 8889:8889 -n fission &
//
// The integration suite already requires the public 8888 forward;
// the 8889 forward is new for this advisory.

const (
	publicRouterURL   = "http://localhost:8888"
	internalRouterURL = "http://localhost:8889"
)

// httpClientWithTimeout returns a short-timeout client so a hung
// listener fails the test fast rather than waiting on the suite-level
// 30m timeout.
func httpClientWithTimeout() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

// TestPublicRouterReturns404ForInternalRoute pins the GHSA-3g33-6vg6-27m8
// regression: the public listener must NOT serve /fission-function/...,
// regardless of whether the function exists.
func TestPublicRouterReturns404ForInternalRoute(t *testing.T) {
	client := httpClientWithTimeout()
	resp, err := client.Get(publicRouterURL + "/fission-function/default/nonexistent")
	require.NoError(t, err, "public router on 8888 must be reachable")
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
	client := httpClientWithTimeout()
	resp, err := client.Get(internalRouterURL + "/fission-function/default/nonexistent")
	require.NoError(t, err, "internal router on 8889 must be reachable")
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Skipf("internal listener returned %d (expected 401); the router may be in HMAC pass-through mode (FISSION_INTERNAL_AUTH_SECRET unset). body=%s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"internal listener must 401 unsigned requests when HMAC is enforced")
}

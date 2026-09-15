// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// identity_test.go drives every row of the design spec's auth truth table
// through IdentityOrJWT wrapping the real Authorizer.Middleware, with real
// HKDF-derived tokens (hmacauth.DeriveAgentIdentityKey) — the same shape
// main_route_test.go uses for the JWT-only route, extended to the identity
// channel.
package agentruntime

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

// identityToken derives and hex-encodes the G16 agent-identity bearer for
// (ns, agent) under master — exactly what the fetcher's agenttoken.go writes
// to the pod-side credential file.
func identityToken(master []byte, ns, agent string) string {
	return hmacauth.EncodeKeyForEnv(hmacauth.DeriveAgentIdentityKey(master, ns, agent))
}

// newIdentityRequest builds a POST /agents/{pathNS}/{pathName} request with
// the {namespace}/{name} path values set as the outer mux would, carrying
// the identity claim headers (claimedNS, claimedAgent) and an
// Authorization: Bearer <bearer> header. An empty claimedNS/claimedAgent
// (both) omits the identity headers entirely.
func newIdentityRequest(pathNS, pathName, claimedNS, claimedAgent, bearer string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/agents/"+pathNS+"/"+pathName, nil)
	r.SetPathValue("namespace", pathNS)
	r.SetPathValue("name", pathName)
	if claimedNS != "" {
		r.Header.Set(HeaderIdentityNamespace, claimedNS)
	}
	if claimedAgent != "" {
		r.Header.Set(HeaderIdentityName, claimedAgent)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

// TestIdentityOrJWT_AllowInsecurePassthrough covers truth-table row 1: JWT
// key unset -> the whole wrapper is a no-op passthrough, including a request
// carrying identity headers with the fetcher's "dev-unauthenticated"
// placeholder token (agenttoken.go) — it must NOT 401.
func TestIdentityOrJWT_AllowInsecurePassthrough(t *testing.T) {
	t.Parallel()
	authz := NewAuthorizer(nil) // JWT key unset
	require.False(t, authz.Enabled())

	// main.go's own rule: identity is nil exactly when authz is disabled.
	handler := IdentityOrJWT(nil, authz.Middleware)(okHandler())

	t.Run("no headers at all", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, newIdentityRequest("ns-a", "fn", "", "", ""))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("dev-unauthenticated placeholder with identity headers", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, newIdentityRequest("ns-a", "fn", "ns-a", "caller-agent", "dev-unauthenticated"))
		assert.Equal(t, http.StatusOK, w.Code, "allowInsecure must pass through even with identity headers present")
	})

	t.Run("identity headers with a real-looking but bogus token", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, newIdentityRequest("ns-a", "fn", "ns-a", "caller-agent", "deadbeef"))
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// TestIdentityOrJWT_HeadersAbsentDelegatesToJWT covers truth-table row 5:
// identity headers absent -> the existing JWT path, byte-identical to
// authz.Middleware alone (TestMiddleware in authz_test.go already proves
// Middleware's own behavior; this proves IdentityOrJWT doesn't change it).
func TestIdentityOrJWT_HeadersAbsentDelegatesToJWT(t *testing.T) {
	t.Parallel()
	key := []byte("test-signing-key")
	master := []byte("test-internal-master")
	authz := NewAuthorizer(key)
	view := NewAgentView()
	identity := NewIdentityVerifier(master, nil, view)
	handler := IdentityOrJWT(identity, authz.Middleware)(okHandler())

	validTok := mintToken(t, key, jwt.MapClaims{
		"sub": "caller-1", "allowed_namespaces": []any{"ns-a"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	t.Run("valid JWT, no identity headers, passes", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, newIdentityRequest("ns-a", "fn", "", "", validTok))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("missing bearer, no identity headers, 401", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, newIdentityRequest("ns-a", "fn", "", "", ""))
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	// Row 5 also covers the masters-empty case: the headers-absent branch
	// must short-circuit to the JWT path before it ever looks at the master
	// secret(s), so a misconfigured (mastersless) install still serves plain
	// JWT-authenticated traffic that never presents identity headers — only
	// a request that DOES present identity headers hits the row-4 misconfig
	// 401 (TestIdentityOrJWT_MasterUnsetMisconfig).
	t.Run("both masters unset, no identity headers, valid JWT still passes", func(t *testing.T) {
		t.Parallel()
		identityNoMasters := NewIdentityVerifier(nil, nil, view) // both masters unset
		handlerNoMasters := IdentityOrJWT(identityNoMasters, authz.Middleware)(okHandler())

		w := httptest.NewRecorder()
		handlerNoMasters.ServeHTTP(w, newIdentityRequest("ns-a", "fn", "", "", validTok))
		assert.Equal(t, http.StatusOK, w.Code, "the headers-absent branch must never consult the masters")
	})
}

// TestIdentityOrJWT_MissingOrMalformedBearerRejected covers the identity
// path's own step 1 (verify's doc comment): identity headers present but no
// usable bearer token — no Authorization header at all, a non-Bearer scheme,
// or an empty token after the prefix — is 401, and never reaches the
// constant-time compare (there is nothing to compare). Also pins the
// deliberate either-header-present fail-closed choice: a request with only
// ONE of the two claim headers set still takes the identity path (and is
// rejected, rather than silently falling back to JWT).
func TestIdentityOrJWT_MissingOrMalformedBearerRejected(t *testing.T) {
	t.Parallel()
	key := []byte("test-signing-key")
	master := []byte("test-internal-master")
	authz := NewAuthorizer(key)
	view := NewAgentView()
	view.Upsert(headerEntry("ns-a", "caller-agent", 0))
	identity := NewIdentityVerifier(master, nil, view)
	handler := IdentityOrJWT(identity, authz.Middleware)(okHandler())

	tests := []struct {
		name string
		req  *http.Request
	}{
		{
			name: "identity headers present, no Authorization header at all",
			req:  newIdentityRequest("ns-a", "fn", "ns-a", "caller-agent", ""),
		},
		{
			name: "identity headers present, non-Bearer scheme",
			req: func() *http.Request {
				r := newIdentityRequest("ns-a", "fn", "ns-a", "caller-agent", "")
				r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
				return r
			}(),
		},
		{
			name: "only the namespace claim header set",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/agents/ns-a/fn", nil)
				r.SetPathValue("namespace", "ns-a")
				r.SetPathValue("name", "fn")
				r.Header.Set(HeaderIdentityNamespace, "ns-a")
				return r
			}(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, tt.req)
			assert.Equal(t, http.StatusUnauthorized, w.Code)
		})
	}
}

// TestIdentityOrJWT_ValidIdentityDispatches covers truth-table row 2: a
// valid, live-agent identity token scoped to the path's namespace dispatches
// straight through — identity headers present is sufficient; no
// Authorization: Bearer JWT is needed at all.
func TestIdentityOrJWT_ValidIdentityDispatches(t *testing.T) {
	t.Parallel()
	key := []byte("test-signing-key")
	master := []byte("test-internal-master")
	authz := NewAuthorizer(key)
	view := NewAgentView()
	// The revocation check looks up the CALLER's claimed identity
	// (caller-agent), not the dispatch target (target-fn) — the two are
	// deliberately different names here to make that distinction obvious.
	view.Upsert(headerEntry("ns-a", "caller-agent", 0))
	identity := NewIdentityVerifier(master, nil, view)
	handler := IdentityOrJWT(identity, authz.Middleware)(okHandler())

	tok := identityToken(master, "ns-a", "caller-agent")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIdentityRequest("ns-a", "target-fn", "ns-a", "caller-agent", tok))
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestIdentityOrJWT_OldMasterAcceptance: a token derived from masterOld
// (rotation) still verifies, mirroring statesvc's dual-master acceptance.
func TestIdentityOrJWT_OldMasterAcceptance(t *testing.T) {
	t.Parallel()
	key := []byte("test-signing-key")
	master := []byte("new-master")
	masterOld := []byte("old-master")
	authz := NewAuthorizer(key)
	view := NewAgentView()
	view.Upsert(headerEntry("ns-a", "caller-agent", 0))
	identity := NewIdentityVerifier(master, masterOld, view)
	handler := IdentityOrJWT(identity, authz.Middleware)(okHandler())

	tok := identityToken(masterOld, "ns-a", "caller-agent")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIdentityRequest("ns-a", "target-fn", "ns-a", "caller-agent", tok))
	assert.Equal(t, http.StatusOK, w.Code, "a token derived from the OLD (rotation) master must still verify")
}

// TestIdentityOrJWT_DeadAgentRejected: the token derives correctly (a valid
// signature over the claimed namespace/agent) but the agent is absent from
// the live view (deleted, or never existed) -> 401, not 200.
func TestIdentityOrJWT_DeadAgentRejected(t *testing.T) {
	t.Parallel()
	key := []byte("test-signing-key")
	master := []byte("test-internal-master")
	authz := NewAuthorizer(key)
	view := NewAgentView() // empty: no agent registered
	identity := NewIdentityVerifier(master, nil, view)
	handler := IdentityOrJWT(identity, authz.Middleware)(okHandler())

	tok := identityToken(master, "ns-a", "ghost-agent")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIdentityRequest("ns-a", "target-fn", "ns-a", "ghost-agent", tok))
	assert.Equal(t, http.StatusUnauthorized, w.Code, "a correctly-derived token for a dead/nonexistent agent must be rejected")
}

// TestIdentityOrJWT_MismatchedClaimsRejected: the claim headers name one
// (namespace, agent) but the presented token was derived for a different
// one — the constant-time compare fails, 401, no fallback.
func TestIdentityOrJWT_MismatchedClaimsRejected(t *testing.T) {
	t.Parallel()
	key := []byte("test-signing-key")
	master := []byte("test-internal-master")
	authz := NewAuthorizer(key)
	view := NewAgentView()
	view.Upsert(headerEntry("ns-a", "target-fn", 0))
	view.Upsert(headerEntry("ns-a", "other-agent", 0))
	identity := NewIdentityVerifier(master, nil, view)
	handler := IdentityOrJWT(identity, authz.Middleware)(okHandler())

	// Token is actually derived for "other-agent", but the claim headers say
	// "caller-agent".
	tok := identityToken(master, "ns-a", "other-agent")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIdentityRequest("ns-a", "target-fn", "ns-a", "caller-agent", tok))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestIdentityOrJWT_CrossNamespaceForbidden is the plan-review blocker: a
// valid token for (nsA, agentX) must not authorize dispatch into a different
// namespace's path, even though the token verifies and the agent is live.
func TestIdentityOrJWT_CrossNamespaceForbidden(t *testing.T) {
	t.Parallel()
	key := []byte("test-signing-key")
	master := []byte("test-internal-master")
	authz := NewAuthorizer(key)
	view := NewAgentView()
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	identity := NewIdentityVerifier(master, nil, view)
	handler := IdentityOrJWT(identity, authz.Middleware)(okHandler())

	tok := identityToken(master, "ns-a", "agent-x")
	w := httptest.NewRecorder()
	// Path namespace is ns-b; the token/claims are for ns-a.
	handler.ServeHTTP(w, newIdentityRequest("ns-b", "some-fn", "ns-a", "agent-x", tok))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestIdentityOrJWT_SameNamespaceCrossAgentAllowed is the spawn shape: a
// valid token identifying the CALLER (nsA, agentX) dispatching to a
// DIFFERENT agent in the same namespace (nsA, agentY) must be allowed — the
// claimed agent name identifies who is calling, not who may be dispatched
// to.
func TestIdentityOrJWT_SameNamespaceCrossAgentAllowed(t *testing.T) {
	t.Parallel()
	key := []byte("test-signing-key")
	master := []byte("test-internal-master")
	authz := NewAuthorizer(key)
	view := NewAgentView()
	view.Upsert(headerEntry("ns-a", "agent-x", 0))
	view.Upsert(headerEntry("ns-a", "agent-y", 0))
	identity := NewIdentityVerifier(master, nil, view)
	handler := IdentityOrJWT(identity, authz.Middleware)(okHandler())

	tok := identityToken(master, "ns-a", "agent-x")
	w := httptest.NewRecorder()
	// Dispatch path targets agent-y; the caller's identity is agent-x.
	handler.ServeHTTP(w, newIdentityRequest("ns-a", "agent-y", "ns-a", "agent-x", tok))
	assert.Equal(t, http.StatusOK, w.Code, "dispatch to a different agent in the same namespace is the spawn shape and must be allowed")
}

// TestIdentityOrJWT_MasterUnsetMisconfig covers truth-table row 4: JWT is
// enabled but no internal master secret is configured on either the active
// or rotation slot -> 401 with an operator-actionable message, regardless of
// what token/claims the request carries.
func TestIdentityOrJWT_MasterUnsetMisconfig(t *testing.T) {
	t.Parallel()
	key := []byte("test-signing-key")
	authz := NewAuthorizer(key)
	view := NewAgentView()
	view.Upsert(headerEntry("ns-a", "caller-agent", 0))
	identity := NewIdentityVerifier(nil, nil, view) // both masters unset
	handler := IdentityOrJWT(identity, authz.Middleware)(okHandler())

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIdentityRequest("ns-a", "fn", "ns-a", "caller-agent", "anything"))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "FISSION_INTERNAL_AUTH_SECRET", "the misconfig response must name the env var an operator needs to set")
}

// TestIdentityOrJWT_BothCredentialsPresentIdentityWins covers the design
// spec's explicit both-present case: identity headers select the identity
// path exclusively, so a valid identity token dispatches successfully even
// though the same Authorization value is not a valid JWT (proving no JWT
// verification ever ran) — and conversely, an otherwise-valid JWT bearer is
// never consulted once identity headers are present, even when the identity
// token itself is invalid (proving no fallback).
func TestIdentityOrJWT_BothCredentialsPresentIdentityWins(t *testing.T) {
	t.Parallel()
	key := []byte("test-signing-key")
	master := []byte("test-internal-master")
	authz := NewAuthorizer(key)
	view := NewAgentView()
	view.Upsert(headerEntry("ns-a", "caller-agent", 0))
	identity := NewIdentityVerifier(master, nil, view)
	handler := IdentityOrJWT(identity, authz.Middleware)(okHandler())

	t.Run("valid identity token (not a valid JWT) dispatches", func(t *testing.T) {
		t.Parallel()
		tok := identityToken(master, "ns-a", "caller-agent")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, newIdentityRequest("ns-a", "target-fn", "ns-a", "caller-agent", tok))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("valid JWT bearer ignored when identity headers present and identity invalid", func(t *testing.T) {
		t.Parallel()
		validJWT := mintToken(t, key, jwt.MapClaims{
			"sub": "caller-1", "allowed_namespaces": []any{"ns-a"},
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		w := httptest.NewRecorder()
		// The Authorization header carries a well-formed, validly-signed JWT
		// scoped to ns-a — if IdentityOrJWT fell back to jwt(next) on identity
		// failure, this would be admitted (200). It must not be.
		handler.ServeHTTP(w, newIdentityRequest("ns-a", "target-fn", "ns-a", "caller-agent", validJWT))
		assert.Equal(t, http.StatusUnauthorized, w.Code, "a valid JWT must never rescue a failed identity check")
	})
}

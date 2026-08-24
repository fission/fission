// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mintToken signs a JWT with the given claims and key (HS256).
func mintToken(t *testing.T, key []byte, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString(key)
	require.NoError(t, err)
	return s
}

// newScopedRequest builds a request whose {namespace} path value is ns, as if
// it had been matched by the "POST /agents/{namespace}/{name}" pattern — the
// shape Middleware always runs behind (see main.go).
func newScopedRequest(ns, bearer string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/agents/"+ns+"/fn", nil)
	r.SetPathValue("namespace", ns)
	r.SetPathValue("name", "fn")
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

// TestMiddleware covers the four states the brief calls out: no key = every
// caller passes through with a wildcard scope; a valid token scoped to the
// request's namespace is admitted; a valid token scoped to a DIFFERENT
// namespace is 403; and a token that fails verification entirely is 401.
func TestMiddleware(t *testing.T) {
	t.Parallel()

	t.Run("no key disabled", func(t *testing.T) {
		t.Parallel()
		a := NewAuthorizer(nil)
		require.False(t, a.Enabled())

		w := httptest.NewRecorder()
		a.Middleware(okHandler()).ServeHTTP(w, newScopedRequest("any-ns", ""))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("valid token scoped to namespace allowed", func(t *testing.T) {
		t.Parallel()
		key := []byte("test-signing-key")
		a := NewAuthorizer(key)
		tok := mintToken(t, key, jwt.MapClaims{
			"sub":                "agent-1",
			"allowed_namespaces": []any{"ns-a"},
			"exp":                time.Now().Add(time.Hour).Unix(),
		})

		w := httptest.NewRecorder()
		a.Middleware(okHandler()).ServeHTTP(w, newScopedRequest("ns-a", tok))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("wrong-namespace token forbidden", func(t *testing.T) {
		t.Parallel()
		key := []byte("test-signing-key")
		a := NewAuthorizer(key)
		tok := mintToken(t, key, jwt.MapClaims{
			"sub":                "agent-1",
			"allowed_namespaces": []any{"ns-a"},
			"exp":                time.Now().Add(time.Hour).Unix(),
		})

		w := httptest.NewRecorder()
		a.Middleware(okHandler()).ServeHTTP(w, newScopedRequest("ns-b", tok))
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("garbage token unauthorized", func(t *testing.T) {
		t.Parallel()
		a := NewAuthorizer([]byte("test-signing-key"))

		w := httptest.NewRecorder()
		a.Middleware(okHandler()).ServeHTTP(w, newScopedRequest("ns-a", "not-a-jwt"))
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("missing bearer token unauthorized", func(t *testing.T) {
		t.Parallel()
		a := NewAuthorizer([]byte("test-signing-key"))

		w := httptest.NewRecorder()
		a.Middleware(okHandler()).ServeHTTP(w, newScopedRequest("ns-a", ""))
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("wildcard token allows any namespace", func(t *testing.T) {
		t.Parallel()
		key := []byte("test-signing-key")
		a := NewAuthorizer(key)
		tok := mintToken(t, key, jwt.MapClaims{
			"sub":                "agent-1",
			"allowed_namespaces": "*",
			"exp":                time.Now().Add(time.Hour).Unix(),
		})

		w := httptest.NewRecorder()
		a.Middleware(okHandler()).ServeHTTP(w, newScopedRequest("any-ns", tok))
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestScopeAllows(t *testing.T) {
	t.Parallel()
	assert.True(t, AuthScope{Wildcard: true}.Allows("anything"))
	assert.True(t, AuthScope{Namespaces: []string{"a", "b"}}.Allows("b"))
	assert.False(t, AuthScope{Namespaces: []string{"a"}}.Allows("b"))
	assert.False(t, AuthScope{}.Allows("a"))
}

func TestAuthorizerDevPassThrough(t *testing.T) {
	t.Parallel()
	a := NewAuthorizer(nil)
	assert.False(t, a.Enabled())

	scope, ok := a.ScopeFromTokenInfo(nil)
	require.True(t, ok)
	assert.True(t, scope.Wildcard)
}

func TestAuthorizerNilTokenDenyWhenEnabled(t *testing.T) {
	t.Parallel()
	a := NewAuthorizer([]byte("k"))
	// Defense in depth: a nil token when verification is enabled is denied.
	_, ok := a.ScopeFromTokenInfo(nil)
	assert.False(t, ok)
}

// TestScopeFromBearer covers the three cases ScopeFromBearer's doc comment
// calls out: a valid token yields its scope, a garbage token is refused, and
// a disabled Authorizer returns a wildcard regardless of what token (if any)
// was supplied — this is what makes GET /registry/events's auth-disabled
// stance (see events.go) provable at the Authorizer layer rather than only
// through the handler.
func TestScopeFromBearer(t *testing.T) {
	t.Parallel()

	t.Run("valid token scope", func(t *testing.T) {
		t.Parallel()
		key := []byte("test-signing-key")
		a := NewAuthorizer(key)
		tok := mintToken(t, key, jwt.MapClaims{
			"sub":                "agent-1",
			"allowed_namespaces": []any{"ns-a", "ns-b"},
			"exp":                time.Now().Add(time.Hour).Unix(),
		})

		scope, ok := a.ScopeFromBearer(tok)
		require.True(t, ok)
		assert.False(t, scope.Wildcard)
		assert.True(t, scope.Allows("ns-a"))
		assert.True(t, scope.Allows("ns-b"))
		assert.False(t, scope.Allows("ns-c"))
	})

	t.Run("garbage token refused", func(t *testing.T) {
		t.Parallel()
		a := NewAuthorizer([]byte("test-signing-key"))

		_, ok := a.ScopeFromBearer("not-a-jwt")
		assert.False(t, ok)
	})

	t.Run("empty token refused when enabled", func(t *testing.T) {
		t.Parallel()
		a := NewAuthorizer([]byte("test-signing-key"))

		_, ok := a.ScopeFromBearer("")
		assert.False(t, ok)
	})

	t.Run("disabled wildcard", func(t *testing.T) {
		t.Parallel()
		a := NewAuthorizer(nil)
		require.False(t, a.Enabled())

		scope, ok := a.ScopeFromBearer("anything, even garbage")
		require.True(t, ok)
		assert.True(t, scope.Wildcard)

		scope, ok = a.ScopeFromBearer("")
		require.True(t, ok)
		assert.True(t, scope.Wildcard)
	})
}

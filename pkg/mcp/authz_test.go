// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
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

func TestScopeAllows(t *testing.T) {
	t.Parallel()
	assert.True(t, AuthScope{Wildcard: true}.Allows("anything"))
	assert.True(t, AuthScope{Namespaces: []string{"a", "b"}}.Allows("b"))
	assert.False(t, AuthScope{Namespaces: []string{"a"}}.Allows("b"))
	assert.False(t, AuthScope{}.Allows("a"))
}

func TestParseScopeClaim(t *testing.T) {
	t.Parallel()

	mustScope := func(v any) AuthScope {
		s, ok := parseScopeClaim(v)
		require.True(t, ok, "claim %v should be valid", v)
		return s
	}
	assert.True(t, mustScope("*").Wildcard)
	assert.Equal(t, []string{"ns1"}, mustScope("ns1").Namespaces)
	assert.Empty(t, mustScope("").Namespaces)
	assert.Equal(t, []string{"a", "b"}, mustScope([]any{"a", "b"}).Namespaces)
	assert.True(t, mustScope([]any{"a", "*"}).Wildcard)

	// Absent claim is valid (authorized for nothing).
	s, ok := parseScopeClaim(nil)
	assert.True(t, ok)
	assert.Empty(t, s.Namespaces)
	assert.False(t, s.Wildcard)

	// Malformed claims are rejected, not silently emptied.
	_, ok = parseScopeClaim(42)
	assert.False(t, ok, "a numeric claim must be rejected")
	_, ok = parseScopeClaim([]any{"a", 7})
	assert.False(t, ok, "a non-string array element must be rejected")
}

func TestAuthorizerVerifyToken(t *testing.T) {
	t.Parallel()
	key := []byte("test-signing-key")
	a := NewAuthorizer(key)
	require.True(t, a.Enabled())

	t.Run("valid token yields scope", func(t *testing.T) {
		t.Parallel()
		tok := mintToken(t, key, jwt.MapClaims{
			"sub":                "agent-1",
			"allowed_namespaces": []any{"ns-a", "ns-b"},
			"exp":                time.Now().Add(time.Hour).Unix(),
		})
		ti, err := a.verifyToken(context.Background(), tok, nil)
		require.NoError(t, err)
		scope, ok := a.ScopeFromTokenInfo(ti)
		require.True(t, ok)
		assert.False(t, scope.Wildcard)
		assert.Equal(t, []string{"ns-a", "ns-b"}, scope.Namespaces)
		assert.Equal(t, "agent-1", ti.UserID)
	})

	t.Run("wildcard claim", func(t *testing.T) {
		t.Parallel()
		tok := mintToken(t, key, jwt.MapClaims{"sub": "agent", "allowed_namespaces": "*", "exp": time.Now().Add(time.Hour).Unix()})
		ti, err := a.verifyToken(context.Background(), tok, nil)
		require.NoError(t, err)
		scope, _ := a.ScopeFromTokenInfo(ti)
		assert.True(t, scope.Wildcard)
	})

	t.Run("wrong key rejected", func(t *testing.T) {
		t.Parallel()
		tok := mintToken(t, []byte("other-key"), jwt.MapClaims{"exp": time.Now().Add(time.Hour).Unix()})
		_, err := a.verifyToken(context.Background(), tok, nil)
		assert.Error(t, err)
	})

	t.Run("expired rejected", func(t *testing.T) {
		t.Parallel()
		tok := mintToken(t, key, jwt.MapClaims{"exp": time.Now().Add(-time.Hour).Unix()})
		_, err := a.verifyToken(context.Background(), tok, nil)
		assert.Error(t, err)
	})

	t.Run("none signing method rejected", func(t *testing.T) {
		t.Parallel()
		tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"allowed_namespaces": "*"})
		s, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
		require.NoError(t, err)
		_, err = a.verifyToken(context.Background(), s, nil)
		assert.Error(t, err, "alg=none must be rejected")
	})

	t.Run("missing exp rejected", func(t *testing.T) {
		t.Parallel()
		// No exp: the SDK rejects a zero Expiration, so verifyToken must too.
		tok := mintToken(t, key, jwt.MapClaims{"allowed_namespaces": "*"})
		_, err := a.verifyToken(context.Background(), tok, nil)
		assert.Error(t, err, "a token without exp must be rejected")
	})

	t.Run("malformed claim rejected", func(t *testing.T) {
		t.Parallel()
		tok := mintToken(t, key, jwt.MapClaims{"sub": "agent", "allowed_namespaces": 42, "exp": time.Now().Add(time.Hour).Unix()})
		_, err := a.verifyToken(context.Background(), tok, nil)
		assert.Error(t, err, "a malformed allowed_namespaces claim must be rejected")
	})

	t.Run("missing sub rejected", func(t *testing.T) {
		t.Parallel()
		// No sub: the SDK can't bind the session to a user, so reject.
		tok := mintToken(t, key, jwt.MapClaims{"allowed_namespaces": "*", "exp": time.Now().Add(time.Hour).Unix()})
		_, err := a.verifyToken(context.Background(), tok, nil)
		assert.Error(t, err, "a token without sub must be rejected")
	})

	t.Run("valid token carries expiration and user", func(t *testing.T) {
		t.Parallel()
		tok := mintToken(t, key, jwt.MapClaims{"sub": "agent", "allowed_namespaces": "*", "exp": time.Now().Add(time.Hour).Unix()})
		ti, err := a.verifyToken(context.Background(), tok, nil)
		require.NoError(t, err)
		assert.False(t, ti.Expiration.IsZero(), "Expiration must be set so the SDK accepts the token")
		assert.Equal(t, "agent", ti.UserID)
	})
}

func TestAuthorizerDevPassThrough(t *testing.T) {
	t.Parallel()
	a := NewAuthorizer(nil)
	assert.False(t, a.Enabled())

	// Nil token info in dev mode → wildcard.
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

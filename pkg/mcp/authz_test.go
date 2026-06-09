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

func TestScopeFromClaim(t *testing.T) {
	t.Parallel()
	assert.True(t, scopeFromClaim("*").Wildcard)
	assert.Equal(t, []string{"ns1"}, scopeFromClaim("ns1").Namespaces)
	assert.Empty(t, scopeFromClaim("").Namespaces)
	assert.Equal(t, []string{"a", "b"}, scopeFromClaim([]any{"a", "b"}).Namespaces)
	assert.True(t, scopeFromClaim([]any{"a", "*"}).Wildcard)
	assert.Empty(t, scopeFromClaim(42).Namespaces)
	assert.Empty(t, scopeFromClaim(nil).Namespaces)
}

func TestAuthorizerVerifyToken(t *testing.T) {
	t.Parallel()
	key := []byte("test-signing-key")
	a := NewAuthorizer(key)
	require.True(t, a.Enabled())

	t.Run("valid token yields scope", func(t *testing.T) {
		t.Parallel()
		tok := mintToken(t, key, jwt.MapClaims{
			"sub":                 "agent-1",
			"allowed_namespaces":  []any{"ns-a", "ns-b"},
			"exp":                 time.Now().Add(time.Hour).Unix(),
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
		tok := mintToken(t, key, jwt.MapClaims{"allowed_namespaces": "*", "exp": time.Now().Add(time.Hour).Unix()})
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

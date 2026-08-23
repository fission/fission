// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
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

func TestScopeClaimUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    AuthScope
		wantErr bool
	}{
		{name: "wildcard string", raw: `"*"`, want: AuthScope{Wildcard: true}},
		{name: "single namespace", raw: `"ns1"`, want: AuthScope{Namespaces: []string{"ns1"}}},
		{name: "empty string", raw: `""`, want: AuthScope{}},
		{name: "array", raw: `["a","b"]`, want: AuthScope{Namespaces: []string{"a", "b"}}},
		{name: "array with wildcard", raw: `["a","*"]`, want: AuthScope{Wildcard: true}},
		{name: "empty array", raw: `[]`, want: AuthScope{Namespaces: []string{}}},
		// Absent and null are both "authorized for nothing", never a reject.
		{name: "null", raw: `null`, want: AuthScope{}},
		// Malformed claims are rejected, not silently emptied.
		{name: "number", raw: `42`, wantErr: true},
		{name: "object", raw: `{"a":1}`, wantErr: true},
		{name: "non-string element", raw: `["a",7]`, wantErr: true},
		// A JSON null element unmarshals into a string as a no-op, so without
		// an explicit refusal it would become "" and the token would be
		// accepted where the pre-typed code rejected it.
		{name: "null element", raw: `[null]`, wantErr: true},
		{name: "null element after a valid one", raw: `["a",null]`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var c mcpClaims
			err := json.Unmarshal([]byte(`{"allowed_namespaces":`+tt.raw+`}`), &c)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, AuthScope(c.AllowedNamespaces))
		})
	}

	t.Run("absent claim", func(t *testing.T) {
		t.Parallel()
		var c mcpClaims
		require.NoError(t, json.Unmarshal([]byte(`{}`), &c))
		assert.Equal(t, AuthScope{}, AuthScope(c.AllowedNamespaces))
	})
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

	// encoding/json matches struct fields case-insensitively; the pre-typed
	// code looked claims up by exact key. A token that smuggles a second
	// spelling past the exact lookup must be rejected, not silently widened.
	t.Run("case-variant claim spellings are rejected", func(t *testing.T) {
		t.Parallel()
		for _, claims := range []jwt.MapClaims{
			{"sub": "agent", "exp": time.Now().Add(time.Hour).Unix(), "ALLOWED_NAMESPACES": "*"},
			{"sub": "agent", "exp": time.Now().Add(time.Hour).Unix(), "allowed_namespaces": "ns-a", "ALLOWED_NAMESPACES": "*"},
			{"SUB": "agent", "exp": time.Now().Add(time.Hour).Unix(), "allowed_namespaces": "ns-a"},
			{"sub": "agent", "EXP": time.Now().Add(time.Hour).Unix(), "allowed_namespaces": "ns-a"},
		} {
			_, err := a.verifyToken(context.Background(), mintToken(t, key, claims), nil)
			assert.Errorf(t, err, "a case-variant claim must be rejected: %v", claims)
		}
	})

	t.Run("null claim is authorized for nothing", func(t *testing.T) {
		t.Parallel()
		tok := mintToken(t, key, jwt.MapClaims{"sub": "agent", "allowed_namespaces": nil, "exp": time.Now().Add(time.Hour).Unix()})
		ti, err := a.verifyToken(context.Background(), tok, nil)
		require.NoError(t, err, "a null allowed_namespaces claim is valid, not malformed")
		scope, ok := a.ScopeFromTokenInfo(ti)
		require.True(t, ok)
		assert.False(t, scope.Wildcard)
		assert.Empty(t, scope.Namespaces)
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

// TestMcpClaimsStrictDecode pins the deliberate v2 strictness of the
// SECURITY-boundary claims parser: a claim set carrying duplicate member
// names or invalid UTF-8 is rejected outright — the duplicate-key smuggling
// and encoding-ambiguity classes v1 tolerated silently. A future leniency
// option added to authz.go fails this test.
func TestMcpClaimsStrictDecode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
	}{
		{name: "duplicate allowed_namespaces", raw: `{"allowed_namespaces":"a","allowed_namespaces":"b"}`},
		{name: "duplicate nested key", raw: `{"allowed_namespaces":["a"],"sub":"x","sub":"y"}`},
		{name: "invalid UTF-8 in claim", raw: "{\"allowed_namespaces\":\"n\xffs\"}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var c mcpClaims
			require.Error(t, c.UnmarshalJSON([]byte(tc.raw)))
		})
	}
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"bytes"
	"encoding/json/jsontext"
	"io"
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

// TestAgentClaimsUnmarshalJSON tables the bespoke allowed_namespaces claim
// decode (agentClaims.UnmarshalJSON / scopeClaim.UnmarshalJSON) at the level
// verifyToken actually calls it: the whole claims document, so the exact-key
// guard and the scope decode are exercised together exactly as production
// does. Each row is either accepted with a precise (Wildcard, Namespaces)
// outcome, or REJECTED outright — a scopeClaim decode error never falls back
// to an empty/no-op scope (see scopeClaim's doc comment): an accidentally
// silent-empty scope would be indistinguishable from "no agents exist",
// while a rejected token is loudly refused.
func TestAgentClaimsUnmarshalJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		raw            string
		wantErr        bool
		wantWildcard   bool
		wantNamespaces []string
	}{
		{
			name:           "single non-wildcard string form",
			raw:            `{"sub":"s","exp":1700000000,"allowed_namespaces":"ns-a"}`,
			wantNamespaces: []string{"ns-a"},
		},
		{
			name: "empty-string scope authorizes nothing",
			raw:  `{"sub":"s","exp":1700000000,"allowed_namespaces":""}`,
		},
		{
			name: "explicit JSON null authorizes nothing",
			raw:  `{"sub":"s","exp":1700000000,"allowed_namespaces":null}`,
		},
		{
			name:    "null element in array is rejected, not silently dropped",
			raw:     `{"sub":"s","exp":1700000000,"allowed_namespaces":["ns-a",null]}`,
			wantErr: true,
		},
		{
			name:    "non-string array element is rejected",
			raw:     `{"sub":"s","exp":1700000000,"allowed_namespaces":[1,2]}`,
			wantErr: true,
		},
		{
			name:    "wrong JSON type is rejected, not silently emptied",
			raw:     `{"sub":"s","exp":1700000000,"allowed_namespaces":42}`,
			wantErr: true,
		},
		{
			name:    "case-colliding ALLOWED_NAMESPACES key is not accepted as the claim",
			raw:     `{"sub":"s","exp":1700000000,"ALLOWED_NAMESPACES":"ns-a"}`,
			wantErr: true,
		},
		{
			name:    "duplicate member names rejected (v2 strict)",
			raw:     `{"sub":"a","sub":"b","exp":1700000000}`,
			wantErr: true,
		},
		{
			name:    "invalid UTF-8 rejected (v2 strict)",
			raw:     "{\"sub\":\"s\",\"exp\":1700000000,\"allowed_namespaces\":\"\xff\"}",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var c agentClaims
			err := c.UnmarshalJSON([]byte(tt.raw))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			scope := AuthScope(c.AllowedNamespaces)
			assert.Equal(t, tt.wantWildcard, scope.Wildcard)
			assert.Equal(t, tt.wantNamespaces, scope.Namespaces)
			if !tt.wantWildcard && len(tt.wantNamespaces) == 0 {
				assert.False(t, scope.Allows("any-namespace"), "an empty/null scope must authorize nothing")
			}
		})
	}
}

// FuzzScopeClaimUnmarshal fuzzes agentClaims.UnmarshalJSON (the whole claims
// document — what verifyToken hands the bespoke decode after JWT signature
// verification has already passed) against the invariant the exact-key/
// strict decode exists to guarantee: it may reject the input outright, but a
// SUCCESSFUL decode must never produce a scope wider than the string
// literals actually present in the input. A widened scope from a malformed
// or adversarial claim set would be a silent authorization escalation — the
// exact failure mode this decode is hardened against.
func FuzzScopeClaimUnmarshal(f *testing.F) {
	seeds := []string{
		`{"sub":"s","exp":1700000000,"allowed_namespaces":"ns-a"}`,
		`{"sub":"s","exp":1700000000,"allowed_namespaces":""}`,
		`{"sub":"s","exp":1700000000,"allowed_namespaces":null}`,
		`{"sub":"s","exp":1700000000,"allowed_namespaces":["ns-a",null]}`,
		`{"sub":"s","exp":1700000000,"allowed_namespaces":[1,2]}`,
		`{"sub":"s","exp":1700000000,"allowed_namespaces":42}`,
		`{"sub":"s","exp":1700000000,"ALLOWED_NAMESPACES":"ns-a"}`,
		`{"sub":"a","sub":"b","exp":1700000000}`,
		`{"sub":"s","exp":1700000000,"allowed_namespaces":"*"}`,
		`{"sub":"s","exp":1700000000,"allowed_namespaces":["ns-a","ns-b"]}`,
		`{}`,
		`null`,
		`[]`,
		`"just a string"`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Add([]byte("{\"sub\":\"s\",\"exp\":1700000000,\"allowed_namespaces\":\"\xff\"}"))

	f.Fuzz(func(t *testing.T, data []byte) {
		var c agentClaims
		if err := c.UnmarshalJSON(data); err != nil {
			return // rejecting the input outright is always a safe outcome
		}
		scope := AuthScope(c.AllowedNamespaces)

		leaves := jsonStringLiterals(t, data)
		if scope.Wildcard && !leaves["*"] {
			t.Fatalf("decoded a wildcard scope but %q is not a string literal in the input %s", "*", data)
		}
		for _, ns := range scope.Namespaces {
			if !leaves[ns] {
				t.Fatalf("decoded namespace %q is not a string literal in the input %s", ns, data)
			}
		}
	})
}

// jsonStringLiterals re-tokenizes data (a document a v2-strict decode above
// has already accepted, so this cannot itself fail) with jsontext.Decoder and
// collects every string token's decoded value — object keys and values
// alike — so FuzzScopeClaimUnmarshal's invariant can check "never widens
// beyond what's in the token" correctly even across JSON string escapes.
// jsontext.Token is a real, purpose-built type (Kind + typed accessors), not
// an untyped decode target, so this walks the document's string content
// without ever holding it in an any/map[string]any/[]any container.
func jsonStringLiterals(t *testing.T, data []byte) map[string]bool {
	t.Helper()
	dec := jsontext.NewDecoder(bytes.NewReader(data))
	leaves := map[string]bool{}
	for {
		tok, err := dec.ReadToken()
		if err != nil {
			require.ErrorIsf(t, err, io.EOF, "re-tokenizing a document agentClaims.UnmarshalJSON accepted failed: %v (data=%s)", err, data)
			return leaves
		}
		if tok.Kind() == '"' {
			leaves[tok.String()] = true
		}
	}
}

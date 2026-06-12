// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

// claimAllowedNamespaces is the JWT claim carrying the caller's authorized
// namespaces: either the string "*" (all namespaces) or a JSON array of
// namespace names. A token with no such claim is authorized for nothing.
const claimAllowedNamespaces = "allowed_namespaces"

// wildcardNamespace is the sentinel scope entry meaning "all namespaces".
const wildcardNamespace = "*"

// AuthScope is the namespace authorization derived from a caller's token. It is
// the single authority for which tools a caller may list and invoke. Wildcard
// takes precedence: when it is true, Namespaces is ignored (the constructors in
// this file always leave Namespaces nil for a wildcard scope).
type AuthScope struct {
	Namespaces []string
	Wildcard   bool
}

// Allows reports whether the scope authorizes the given namespace.
func (s AuthScope) Allows(namespace string) bool {
	if s.Wildcard {
		return true
	}
	return slices.Contains(s.Namespaces, namespace)
}

// Authorizer validates bearer tokens for the MCP endpoint and derives the
// namespace scope. When no signing key is configured it runs in pass-through dev
// mode (every caller gets a wildcard scope), matching the router internal
// listener's "empty secret = pass-through" stance. Pass-through is fail-closed at
// startup: Start refuses to run without a key unless MCP_ALLOW_INSECURE=true, and
// the Helm chart only deploys unauthenticated when explicitly opted in. A scoped
// deployment requires a signing key (Helm wires it from the router secret when
// authentication.enabled) and per-caller JWTs carrying an allowed_namespaces claim.
type Authorizer struct {
	signingKey []byte
}

// NewAuthorizer returns an authorizer. An empty signingKey enables dev
// pass-through mode.
func NewAuthorizer(signingKey []byte) *Authorizer {
	return &Authorizer{signingKey: signingKey}
}

// Enabled reports whether token verification is active (a signing key is set).
func (a *Authorizer) Enabled() bool { return len(a.signingKey) > 0 }

// HTTPMiddleware wraps the MCP HTTP handler. When enabled it requires a valid
// bearer token (the SDK's RequireBearerToken stores the resulting TokenInfo on
// the request, which the SDK threads into each MCP request's Extra). When
// disabled it is a pass-through: no TokenInfo is attached and ScopeFromRequest
// yields a wildcard.
func (a *Authorizer) HTTPMiddleware(next http.Handler) http.Handler {
	if !a.Enabled() {
		return next
	}
	return auth.RequireBearerToken(a.verifyToken, nil)(next)
}

// verifyToken is the SDK TokenVerifier: it validates the JWT against the signing
// key and projects the allowed_namespaces claim into TokenInfo. Scopes carries
// the namespace list (or the "*" sentinel); UserID is the subject, which the
// transport uses to bind a session to one caller and prevent session hijacking.
// Expiration is required: the SDK's RequireBearerToken rejects a TokenInfo with
// a zero Expiration, so a token without an exp claim is treated as invalid.
func (a *Authorizer) verifyToken(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return a.signingKey, nil
	})
	if err != nil || !parsed.Valid {
		return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}

	scope, ok := parseScopeClaim(claims[claimAllowedNamespaces])
	if !ok {
		return nil, fmt.Errorf("%w: malformed %s claim", auth.ErrInvalidToken, claimAllowedNamespaces)
	}

	exp, ok := claims["exp"].(float64)
	if !ok {
		return nil, fmt.Errorf("%w: missing exp claim", auth.ErrInvalidToken)
	}

	// A non-empty subject is required: the SDK binds a session to TokenInfo.UserID
	// to stop another party from attaching to a leaked session id. An empty UserID
	// disables that check, so reject tokens without a sub.
	sub, ok := claims["sub"].(string)
	if !ok || sub == "" {
		return nil, fmt.Errorf("%w: missing sub claim", auth.ErrInvalidToken)
	}

	ti := &auth.TokenInfo{Scopes: scope.Namespaces, Expiration: time.Unix(int64(exp), 0), UserID: sub}
	if scope.Wildcard {
		ti.Scopes = []string{wildcardNamespace}
	}
	return ti, nil
}

// ScopeFromTokenInfo derives the namespace scope from a verified token. A nil
// token means no verification ran: a wildcard in dev mode (no key), deny
// otherwise (defense in depth — RequireBearerToken should have rejected it).
func (a *Authorizer) ScopeFromTokenInfo(ti *auth.TokenInfo) (AuthScope, bool) {
	if ti == nil {
		if a.Enabled() {
			return AuthScope{}, false
		}
		return AuthScope{Wildcard: true}, true
	}
	return scopeFromScopes(ti.Scopes), true
}

// scopeFromScopes rebuilds an AuthScope from TokenInfo.Scopes (the "*" sentinel
// means wildcard).
func scopeFromScopes(scopes []string) AuthScope {
	if slices.Contains(scopes, wildcardNamespace) {
		return AuthScope{Wildcard: true}
	}
	return AuthScope{Namespaces: scopes}
}

// parseScopeClaim parses the allowed_namespaces claim, which may be absent (a
// valid token authorized for nothing), the string "*", a single namespace
// string, or an array of namespace strings. It returns ok=false when the claim
// is present but malformed (a wrong JSON type, or an array with non-string
// elements) so the token is rejected rather than silently reduced to an empty
// scope that is indistinguishable from "no tools exist".
func parseScopeClaim(v any) (AuthScope, bool) {
	switch c := v.(type) {
	case nil:
		return AuthScope{}, true // absent claim: authorized for nothing
	case string:
		if c == wildcardNamespace {
			return AuthScope{Wildcard: true}, true
		}
		if c == "" {
			return AuthScope{}, true
		}
		return AuthScope{Namespaces: []string{c}}, true
	case []any:
		ns := make([]string, 0, len(c))
		for _, e := range c {
			s, ok := e.(string)
			if !ok {
				return AuthScope{}, false // non-string element: malformed
			}
			if s == wildcardNamespace {
				return AuthScope{Wildcard: true}, true
			}
			ns = append(ns, s)
		}
		return AuthScope{Namespaces: ns}, true
	default:
		return AuthScope{}, false // unexpected type: malformed
	}
}

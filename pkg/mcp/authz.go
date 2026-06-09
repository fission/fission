// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"net/http"

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
// the single authority for which tools a caller may list and invoke.
type AuthScope struct {
	Namespaces []string
	Wildcard   bool
}

// Allows reports whether the scope authorizes the given namespace.
func (s AuthScope) Allows(namespace string) bool {
	if s.Wildcard {
		return true
	}
	for _, ns := range s.Namespaces {
		if ns == namespace {
			return true
		}
	}
	return false
}

// Authorizer validates bearer tokens for the MCP endpoint and derives the
// namespace scope. When no signing key is configured it runs in pass-through dev
// mode (every caller gets a wildcard scope), matching the router internal
// listener's "empty secret = pass-through" stance. The Helm default sets a key
// and scopes callers to the install namespace, so production is scoped-by-default.
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

	scope := scopeFromClaim(claims[claimAllowedNamespaces])
	ti := &auth.TokenInfo{Scopes: scope.Namespaces}
	if scope.Wildcard {
		ti.Scopes = []string{wildcardNamespace}
	}
	if sub, ok := claims["sub"].(string); ok {
		ti.UserID = sub
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
	for _, s := range scopes {
		if s == wildcardNamespace {
			return AuthScope{Wildcard: true}
		}
	}
	return AuthScope{Namespaces: scopes}
}

// scopeFromClaim parses the allowed_namespaces claim, which may be the string
// "*", a single namespace string, or an array of namespace strings.
func scopeFromClaim(v any) AuthScope {
	switch c := v.(type) {
	case string:
		if c == wildcardNamespace {
			return AuthScope{Wildcard: true}
		}
		if c == "" {
			return AuthScope{}
		}
		return AuthScope{Namespaces: []string{c}}
	case []any:
		var ns []string
		for _, e := range c {
			if s, ok := e.(string); ok {
				if s == wildcardNamespace {
					return AuthScope{Wildcard: true}
				}
				ns = append(ns, s)
			}
		}
		return AuthScope{Namespaces: ns}
	default:
		return AuthScope{}
	}
}

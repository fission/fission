// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"slices"
	"strings"

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
	var claims mcpClaims
	parsed, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return a.signingKey, nil
	})
	if err != nil {
		// A malformed allowed_namespaces claim surfaces here too: scopeClaim's
		// UnmarshalJSON rejects it, so the token is refused rather than
		// silently reduced to an empty scope.
		return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}
	if !parsed.Valid {
		return nil, fmt.Errorf("%w: token is invalid", auth.ErrInvalidToken)
	}

	if claims.ExpiresAt == nil {
		return nil, fmt.Errorf("%w: missing exp claim", auth.ErrInvalidToken)
	}

	// A non-empty subject is required: the SDK binds a session to TokenInfo.UserID
	// to stop another party from attaching to a leaked session id. An empty UserID
	// disables that check, so reject tokens without a sub.
	if claims.Subject == "" {
		return nil, fmt.Errorf("%w: missing sub claim", auth.ErrInvalidToken)
	}

	scope := AuthScope(claims.AllowedNamespaces)
	ti := &auth.TokenInfo{Scopes: scope.Namespaces, Expiration: claims.ExpiresAt.Time, UserID: claims.Subject}
	if scope.Wildcard {
		ti.Scopes = []string{wildcardNamespace}
	}
	return ti, nil
}

// mcpClaims is the decoded shape of an MCP bearer token: the registered claims
// (exp, sub, ...) plus the allowed_namespaces scope claim.
type mcpClaims struct {
	jwt.RegisteredClaims
	AllowedNamespaces scopeClaim `json:"allowed_namespaces"`
}

// readClaims are the claim names this verifier's decisions depend on, either
// directly (allowed_namespaces, sub) or through the library's validation
// (exp, nbf, iat). Their spelling is guarded — see UnmarshalJSON.
var readClaims = map[string]struct{}{
	claimAllowedNamespaces: {}, "sub": {}, "exp": {}, "nbf": {}, "iat": {},
}

// UnmarshalJSON decodes the claim set by EXACT key.
//
// encoding/json (v1) matches struct fields case-insensitively, so a plain
// struct decode would let a token carrying "ALLOWED_NAMESPACES" populate the
// scope that only "allowed_namespaces" is meant to own — and, with two
// spellings present, the last one in the document would win. The pre-typed
// code looked claims up in a map with exact keys and had no such hole, so any
// key that differs only in case from a claim this verifier reads is refused
// outright. (v2's default struct matching is itself case-sensitive, which
// would already close this hole, but the map-based guard stays: it is what
// enforces the exact-key contract, not an artifact of v1's matching.)
func (c *mcpClaims) UnmarshalJSON(b []byte) error {
	// SECURITY boundary: v2 defaults (strict) are used deliberately here —
	// duplicate JSON member names and invalid UTF-8 in the token's claim set
	// are rejected rather than silently tolerated, hardening this JWT-claims
	// decode against ambiguous or malformed tokens.
	var raw map[string]jsontext.Value
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for k := range raw {
		if _, exact := readClaims[k]; exact {
			continue
		}
		if _, collides := readClaims[strings.ToLower(k)]; collides {
			return fmt.Errorf("claim %q differs only in case from %q", k, strings.ToLower(k))
		}
	}
	if v, ok := raw[claimAllowedNamespaces]; ok {
		if err := c.AllowedNamespaces.UnmarshalJSON(v); err != nil {
			return err
		}
	}
	// Registered claims keep the library's own decoding and validation; the
	// guard above has already ruled out case variants of the ones that matter.
	return json.Unmarshal(b, &c.RegisteredClaims)
}

// scopeClaim is the decoded allowed_namespaces claim. On the wire it may be
// absent (a valid token authorized for nothing), JSON null, the string "*", a
// single namespace string, or an array of namespace strings. Any other JSON
// type — or an array with a non-string element — is malformed and fails the
// decode, so the token is rejected rather than silently reduced to an empty
// scope that is indistinguishable from "no tools exist".
//
// It is an AuthScope so the projection is a conversion rather than a second
// pair of fields that could disagree with it.
type scopeClaim AuthScope

// UnmarshalJSON accepts a JSON string, an array of strings, or null.
//
// SECURITY boundary: both Unmarshal calls below use v2 strict defaults (no
// Allow options) — this claim is untrusted token input, and the hardening
// goal is to reject a malformed or ambiguous encoding rather than tolerate it
//.
func (c *scopeClaim) UnmarshalJSON(b []byte) error {
	*c = scopeClaim{}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil // explicit null: authorized for nothing
	}
	var single string
	if err := json.Unmarshal(b, &single); err == nil {
		switch single {
		case wildcardNamespace:
			c.Wildcard = true
		case "":
		default:
			c.Namespaces = []string{single}
		}
		return nil
	}
	// []*string rather than []string: a JSON null element unmarshals into a
	// string as a no-op, so it would silently become "" and the token would be
	// accepted where the pre-typed code rejected it. A pointer element is nil
	// for null and can be refused.
	var many []*string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("%s claim must be a string or an array of strings", claimAllowedNamespaces)
	}
	ns := make([]string, 0, len(many))
	for _, s := range many {
		if s == nil {
			return fmt.Errorf("%s claim has a null element; every element must be a string", claimAllowedNamespaces)
		}
		if *s == wildcardNamespace {
			c.Wildcard = true
			return nil
		}
		ns = append(ns, *s)
	}
	c.Namespaces = ns
	return nil
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

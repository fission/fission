// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// authz.go is a mirror of pkg/mcp/authz.go: the same JWT bearer verification
// on JWT_SIGNING_KEY, the same allowed_namespaces claim contract, and the
// same "empty key = dev pass-through" stance. It differs from the mcp
// original only in how the verified scope is applied to a request: mcp
// checks a tool's Function namespace against the scope from inside its MCP
// server logic, while agentruntime is a plain net/http endpoint, so
// Middleware below checks the scope against the request's {namespace} path
// value directly. A shared-package extraction of the common bearer-token
// verification is a known follow-up (see main.go's package doc); this file
// stays a faithful duplicate until that lands, rather than diverging the
// claim contract between the two subsystems.
package agentruntime

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"slices"
	"strings"
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
// the single authority for which agent namespace a caller may dispatch turns
// into. Wildcard takes precedence: when it is true, Namespaces is ignored (the
// constructors in this file always leave Namespaces nil for a wildcard scope).
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

// Authorizer validates bearer tokens for the agent runtime endpoint and
// derives the namespace scope. When no signing key is configured it runs in
// pass-through dev mode (every caller gets a wildcard scope), matching the
// router internal listener's "empty secret = pass-through" stance.
// Pass-through is fail-closed at startup: Start refuses to run without a key
// unless AGENT_ALLOW_INSECURE=true, and the Helm chart only deploys
// unauthenticated when explicitly opted in. A scoped deployment requires a
// signing key (Helm wires it from the router secret when
// authentication.enabled) and per-caller JWTs carrying an allowed_namespaces
// claim.
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

// HTTPMiddleware wraps next: when enabled it requires a valid bearer token
// (the SDK's RequireBearerToken stores the resulting TokenInfo on the
// request's context). When disabled it is a pass-through: no TokenInfo is
// attached and ScopeFromTokenInfo yields a wildcard.
func (a *Authorizer) HTTPMiddleware(next http.Handler) http.Handler {
	if !a.Enabled() {
		return next
	}
	return auth.RequireBearerToken(a.verifyToken, nil)(next)
}

// Middleware wraps next with HTTPMiddleware, then enforces that the verified
// scope allows the request's {namespace} path value (set by the ServeMux
// pattern this handler is registered under — see main.go). A caller with a
// valid token but the wrong namespace scope gets 403, distinct from the 401
// HTTPMiddleware returns for a missing or invalid token.
func (a *Authorizer) Middleware(next http.Handler) http.Handler {
	return a.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope, ok := a.ScopeFromTokenInfo(auth.TokenInfoFromContext(r.Context()))
		if !ok || !scope.Allows(r.PathValue("namespace")) {
			http.Error(w, "forbidden: token not authorized for this namespace", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// verifyToken is the SDK TokenVerifier: it validates the JWT against the signing
// key and projects the allowed_namespaces claim into TokenInfo. Scopes carries
// the namespace list (or the "*" sentinel); UserID is the subject. Expiration
// is required: the SDK's RequireBearerToken rejects a TokenInfo with a zero
// Expiration, so a token without an exp claim is treated as invalid.
func (a *Authorizer) verifyToken(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	var claims agentClaims
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

	// A non-empty subject is required, mirroring mcp's contract even though
	// this transport has no session-hijacking concern of its own: keeping the
	// same rejection here avoids two verifiers silently accepting different
	// tokens for the same claim shape.
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

// agentClaims is the decoded shape of an agent-runtime bearer token: the
// registered claims (exp, sub, ...) plus the allowed_namespaces scope claim.
type agentClaims struct {
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
// spellings present, the last one in the document would win. Looking claims
// up in a map with exact keys closes that hole: any key that differs only in
// case from a claim this verifier reads is refused outright. (v2's default
// struct matching is itself case-sensitive, which would already close this
// hole, but the map-based guard stays: it is what enforces the exact-key
// contract, not an artifact of v1's matching.)
func (c *agentClaims) UnmarshalJSON(b []byte) error {
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
// scope that is indistinguishable from "no agents exist".
//
// It is an AuthScope so the projection is a conversion rather than a second
// pair of fields that could disagree with it.
type scopeClaim AuthScope

// UnmarshalJSON accepts a JSON string, an array of strings, or null.
//
// SECURITY boundary: both Unmarshal calls below use v2 strict defaults (no
// Allow options) — this claim is untrusted token input, and the hardening
// goal is to reject a malformed or ambiguous encoding rather than tolerate it.
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

// ScopeFromBearer verifies token and derives its namespace scope, for a
// handler that cannot use HTTPMiddleware/Middleware because they rely on the
// SDK's auth.RequireBearerToken, which only ever looks at the standard
// Authorization header — GET /registry/events accepts a token via ?token=
// as well (a browser EventSource cannot set request headers), so its
// handler extracts the token itself and calls this instead.
//
// verifyToken is reused unchanged: no separate, potentially-diverging
// verification path exists for this entry point. When verification is
// disabled (no signing key), this returns a wildcard scope regardless of
// token — mirroring HTTPMiddleware's own pass-through stance — rather than
// requiring the caller to special-case Enabled() before calling in.
func (a *Authorizer) ScopeFromBearer(token string) (AuthScope, bool) {
	scope, _, ok := a.ScopeAndExpiryFromBearer(token)
	return scope, ok
}

// ScopeAndExpiryFromBearer is ScopeFromBearer plus the token's expiration
// instant, so a long-lived connection (GET /registry/events, an open SSE
// stream) can bound itself by the credential that authorized it rather than
// outliving the token indefinitely. The expiry is the JWT's exp claim
// (verifyToken already requires and surfaces it as TokenInfo.Expiration). In
// pass-through dev mode (no signing key) there is no token and so no expiry:
// the returned time is the zero Time, which the caller treats as "no
// token-driven deadline" (a hard max-lifetime backstop still applies).
func (a *Authorizer) ScopeAndExpiryFromBearer(token string) (AuthScope, time.Time, bool) {
	if !a.Enabled() {
		return AuthScope{Wildcard: true}, time.Time{}, true
	}
	ti, err := a.verifyToken(context.Background(), token, nil)
	if err != nil {
		return AuthScope{}, time.Time{}, false
	}
	return scopeFromScopes(ti.Scopes), ti.Expiration, true
}

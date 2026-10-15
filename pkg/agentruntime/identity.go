// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// identity.go implements the G16 agent-identity bearer verification,
// originally on the dispatch route ONLY (POST /agents/{namespace}/{name}) —
// a second, exclusive auth path alongside authz.go's JWT verification, for a
// pod's own spawn/dispatch calls carrying the HKDF-derived token Task 1
// (DeriveAgentIdentityKey, pkg/auth/hmac/keys.go) and Task 2 (the fetcher's
// .fission-agent-token file) produced.
//
// AMENDED (G12 session workspace, a later slice): the identity bearer now
// also authenticates the session workspace routes
// (PUT/GET/DELETE /workspace/{namespace}/{name}/{session}/{path...} and the
// list route, workspace.go) — a SECOND surface, with a STRICTER
// authorization rule than dispatch's. The shared verification steps (1-4
// below) are factored into verifyClaims; each surface then applies its own
// step-5 authorization closure: dispatch's verify keeps ns-only equality
// (cross-agent dispatch in the same namespace is the spawn shape), while
// workspace's verifyOwnWorkspace additionally requires agent equality
// (own-workspace only in v1 — see verifyOwnWorkspace's doc for the future
// cross-agent-sharing relaxation this parallels). It still never reaches the
// registry/SSE routes: IdentityOrJWT/IdentityOrJWTOwnWorkspace are wired onto
// the dispatch and workspace mounts only (main.go).
package agentruntime

import (
	"crypto/subtle"
	"net/http"
	"strings"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

const (
	// HeaderIdentityNamespace and HeaderIdentityName carry the CLAIMED
	// (namespace, agent) identity of a pod-side bearer token: the
	// Authorization header carries the derived token itself, and verification
	// recomputes DeriveAgentIdentityKey from exactly these claims and
	// compares — the same claims-ride-beside-the-token shape as statesvc's
	// bearer channel (pkg/statesvc/auth.go, stateapi.HeaderNamespace/
	// HeaderKeyspace). The presence of either header (not the Authorization
	// scheme) selects the identity verification path EXCLUSIVELY on either of
	// the two mounts that consult it — the dispatch route (IdentityOrJWT) and
	// the session workspace routes (IdentityOrJWTOwnWorkspace, workspace.go).
	// Named alongside the dispatcher's own X-Fission-Agent-* header family
	// (dispatcher.go:37-74) but distinct in
	// kind: those are turn-protocol headers forwarded to/from the function;
	// these two are AUTHENTICATION claims consumed here and never proxied
	// upstream.
	HeaderIdentityNamespace = "X-Fission-Agent-Auth-Namespace"
	HeaderIdentityName      = "X-Fission-Agent-Auth-Name"
)

// IdentityVerifier verifies the G16 agent-identity bearer against BOTH the
// active and rotation-old FISSION_INTERNAL_AUTH_SECRET masters. master and
// masterOld are RAW — read as []byte(os.Getenv(...)) exactly like every
// other master-secret consumer (hmacauth.ServiceSigner/ServiceVerifier,
// fetcher/statetoken.go, fetcher/agenttoken.go) — never
// hmacauth.DecodeKeyFromEnv, which reverses EncodeKeyForEnv for an
// already-DERIVED key read back from an env var; applying it to the raw
// master here would silently re-hex-decode a secret that was never
// hex-encoded in the first place and change what every derivation produces.
//
// view is the live AgentView (Task-independent, view.go): Lookup is the
// revocation check — a token that derives correctly for a deleted or
// never-existing agent is still rejected, because the view holds only
// Agent-enabled Functions the reconciler currently knows about.
type IdentityVerifier struct {
	master, masterOld []byte
	view              *AgentView
}

// NewIdentityVerifier returns a verifier over the given masters and view.
func NewIdentityVerifier(master, masterOld []byte, view *AgentView) *IdentityVerifier {
	return &IdentityVerifier{master: master, masterOld: masterOld, view: view}
}

// verifyClaims implements the identity path's shared checks 1-4, in order,
// writing the HTTP error response itself and returning ok=false on the first
// one that fails. It is the extracted "verify core" both surfaces
// (dispatch's verify and workspace's verifyOwnWorkspace) build on — each
// composes its own step-5 authorization closure on top of the returned,
// now-authenticated claims.
//
//  1. Parse the bearer token. Empty (missing header, non-Bearer scheme, or
//     an empty token) -> 401.
//  2. Both masters unset -> 401 with an operator-actionable message: this is
//     the misconfig row of the truth table (JWT auth is on but the internal
//     secret the identity channel depends on never got wired in), distinct
//     from "identity is not in use here" (that case never reaches verify —
//     see IdentityOrJWT).
//  3. Constant-time compare against BOTH masters' derivations, mirroring
//     statesvc's verifyKeyspaceToken (pkg/statesvc/auth.go:65-77): both
//     comparisons always run (ok |=, never short-circuited) so which master
//     matched is not observable via timing. Failure -> 401.
//  4. Revocation check AFTER the compare, not before: view.Lookup so an
//     invalid-token holder never learns whether the claimed agent exists.
//     Missing -> 401.
func (v *IdentityVerifier) verifyClaims(w http.ResponseWriter, r *http.Request) (claimedNS, claimedAgent string, ok bool) {
	claimedNS = r.Header.Get(HeaderIdentityNamespace)
	claimedAgent = r.Header.Get(HeaderIdentityName)

	token, isBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !isBearer || token == "" {
		http.Error(w, "missing agent identity bearer token", http.StatusUnauthorized)
		return "", "", false
	}

	if len(v.master) == 0 && len(v.masterOld) == 0 {
		http.Error(w, "agent identity requires FISSION_INTERNAL_AUTH_SECRET on the agent runtime", http.StatusUnauthorized)
		return "", "", false
	}

	match := 0
	for _, master := range [][]byte{v.master, v.masterOld} {
		if len(master) == 0 {
			continue
		}
		want := hmacauth.EncodeKeyForEnv(hmacauth.DeriveAgentIdentityKey(master, claimedNS, claimedAgent))
		match |= subtle.ConstantTimeCompare([]byte(token), []byte(want))
	}
	if match != 1 {
		http.Error(w, "invalid agent identity token", http.StatusUnauthorized)
		return "", "", false
	}

	if _, live := v.view.Lookup(claimedNS, claimedAgent); !live {
		http.Error(w, "invalid agent identity token", http.StatusUnauthorized)
		return "", "", false
	}

	return claimedNS, claimedAgent, true
}

// verify is the dispatch route's step-5 authorization on top of
// verifyClaims: the claimed namespace must equal the dispatch path's
// {namespace} (set by the outer mux before this wrapper runs). The claimed
// AGENT identifies the CALLER, not the target: dispatch to a different agent
// in the SAME namespace is the spawn shape (architect -> support-desk) and
// is allowed. Mismatch -> 403.
func (v *IdentityVerifier) verify(w http.ResponseWriter, r *http.Request) bool {
	claimedNS, _, ok := v.verifyClaims(w, r)
	if !ok {
		return false
	}
	if claimedNS != r.PathValue("namespace") {
		http.Error(w, "forbidden: agent identity token not authorized for this namespace", http.StatusForbidden)
		return false
	}
	return true
}

// verifyOwnWorkspace is the session workspace routes' step-5 authorization
// on top of verifyClaims — the G16 surface amendment (see this file's
// package doc): STRICTER than dispatch's verify, requiring BOTH the claimed
// namespace AND the claimed agent to equal the workspace path's
// {namespace}/{name}. Own-AGENT-workspace only in v1: a pod may read/write/
// list/delete only its own AGENT's artifacts, never another agent's — even one
// in the same namespace, which verify's dispatch-side check would allow.
//
// The enforced boundary is the (namespace, agent) pair, NOT the session: the
// identity token is DeriveAgentIdentityKey(master, ns, agent), so every session
// of one agent shares it, and requireSession only checks the {session} exists
// under (ns, agent). Session-level isolation is therefore NOT cryptographically
// enforceable with the current agent-scoped token — one session of an agent can
// reach another session's artifacts of the SAME agent. That is acceptable in v1
// (all sessions of an agent run the same Function code and identity); binding
// the token to the session is a later hardening, tracked with the cross-agent
// workspace-sharing relaxation (parallel to dispatch's cross-agent spawn
// allowance), deliberately not built here. Mismatch on either claim -> 403.
func (v *IdentityVerifier) verifyOwnWorkspace(w http.ResponseWriter, r *http.Request) bool {
	claimedNS, claimedAgent, ok := v.verifyClaims(w, r)
	if !ok {
		return false
	}
	if claimedNS != r.PathValue("namespace") || claimedAgent != r.PathValue("name") {
		http.Error(w, "forbidden: agent identity token not authorized for this workspace", http.StatusForbidden)
		return false
	}
	return true
}

// identityOrJWT is the shared combinator behind IdentityOrJWT and
// IdentityOrJWTOwnWorkspace: identity path when identity is non-nil and the
// request carries either identity claim header (verified by check, the
// surface-specific step-5 closure — (*IdentityVerifier).verify or
// verifyOwnWorkspace), otherwise the existing jwt middleware unchanged.
//
// identity is nil exactly when the JWT signing key is unset (allowInsecure
// dev mode) — main.go only constructs an IdentityVerifier when
// authz.Enabled(). That is what makes the allowInsecure row of the truth
// table hold without this needing any other signal of its own: with
// identity == nil the returned handler is jwt(next) and nothing else, so it
// is a bytewise no-op passthrough exactly as today (authz.HTTPMiddleware
// returns next unwrapped when the key is empty, authz.go:85-90) — a request
// carrying identity headers with the fetcher's "dev-unauthenticated"
// placeholder token (agenttoken.go) never reaches identity verification at
// all, and so cannot be 401'd by the "no master secret configured" check.
//
// When identity is non-nil, the two credential channels are mutually
// exclusive by construction: identity headers present selects check
// EXCLUSIVELY — success calls next.ServeHTTP directly (bypassing jwt
// entirely, even when the request also carries an Authorization: Bearer JWT
// — identity wins), and failure returns the identity path's own error
// response with NO fallback to jwt (falling back would let an attacker
// downgrade a rejected identity token to a JWT check by also supplying a
// stolen or forged bearer). Identity headers absent delegates unchanged to
// jwt(next), built once here rather than per-request.
func identityOrJWT(identity *IdentityVerifier, jwt func(http.Handler) http.Handler, check func(*IdentityVerifier, http.ResponseWriter, *http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		jwtHandler := jwt(next)
		if identity == nil {
			return jwtHandler
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(HeaderIdentityNamespace) == "" && r.Header.Get(HeaderIdentityName) == "" {
				jwtHandler.ServeHTTP(w, r)
				return
			}
			if check(identity, w, r) {
				next.ServeHTTP(w, r)
			}
		})
	}
}

// IdentityOrJWT wraps next with the dispatch route's dual auth: see
// identityOrJWT. Uses (*IdentityVerifier).verify (ns-only equality).
func IdentityOrJWT(identity *IdentityVerifier, jwt func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return identityOrJWT(identity, jwt, (*IdentityVerifier).verify)
}

// IdentityOrJWTOwnWorkspace wraps next with the session workspace routes'
// dual auth: see identityOrJWT. Uses (*IdentityVerifier).verifyOwnWorkspace
// (ns AND agent equality — own-workspace only, the G16 surface amendment).
func IdentityOrJWTOwnWorkspace(identity *IdentityVerifier, jwt func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return identityOrJWT(identity, jwt, (*IdentityVerifier).verifyOwnWorkspace)
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statesvc

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/statestore"
)

// Scope/identity request headers. The namespace and keyspace a request
// operates on are CLAIMS the caller presents; they become the statestore
// Scope only after the bearer token (re-derived from exactly those claims) or
// the admin HMAC signature verifies. A function cannot name another
// function's keyspace: its token only ever matches its own (ns, keyspace).
const (
	HeaderStateNamespace = "X-Fission-State-Namespace"
	HeaderStateKeyspace  = "X-Fission-State-Keyspace"
)

// StateOwner is the fixed Scope.Owner for every function-state keyspace.
// Deliberately NOT the function name: the keyspace (explicit in StateConfig,
// defaulting to the function name) is the durable identity, so renaming a
// function while keeping its keyspace keeps its data.
const StateOwner = "function-state"

type scopeCtxKey struct{}

type authedScope struct {
	scope statestore.Scope
	admin bool
}

// scopeFrom returns the verified scope stashed by the auth middleware.
func scopeFrom(ctx context.Context) (authedScope, bool) {
	s, ok := ctx.Value(scopeCtxKey{}).(authedScope)
	return s, ok
}

// authenticator verifies the two statesvc channels: per-keyspace bearer
// tokens (function path) and ServiceStateAPI HMAC signatures (admin path).
// Verification is stateless — tokens are re-derived from the master secret
// and the request's scope claims, never stored.
type authenticator struct {
	master    []byte
	masterOld []byte
	adminMW   func(http.Handler) http.Handler
}

func newAuthenticator(master, masterOld []byte, opts hmacauth.VerifierOpts) *authenticator {
	a := &authenticator{master: master, masterOld: masterOld}
	if len(master) > 0 {
		a.adminMW = hmacauth.ServiceVerifier(master, masterOld, hmacauth.ServiceStateAPI, opts)
	}
	return a
}

// adminScopeFromQuery reads the signature-covered admin scope claims.
func adminScopeFromQuery(r *http.Request) (ns, keyspace string) {
	return r.URL.Query().Get(adminQueryNamespace), r.URL.Query().Get(adminQueryKeyspace)
}

// passThrough reports dev mode: no master secret configured, bearer requests
// are accepted on their claims alone (parity with the router-internal and
// statestoresvc empty-secret convention; the Helm chart always sets a secret).
func (a *authenticator) passThrough() bool { return len(a.master) == 0 }

// verifyKeyspaceToken constant-time-compares token against the derivations
// from the active and rotation masters. Both comparisons always run.
func (a *authenticator) verifyKeyspaceToken(token, ns, keyspace string) bool {
	ok := 0
	for _, master := range [][]byte{a.master, a.masterOld} {
		if len(master) == 0 {
			continue
		}
		want := hmacauth.EncodeKeyForEnv(hmacauth.DeriveStateKeyspaceKey(master, ns, keyspace))
		ok |= subtle.ConstantTimeCompare([]byte(token), []byte(want))
	}
	return ok == 1
}

// middleware authenticates the request and stashes the verified scope. The
// bearer path (function pods) and the HMAC path (CLI/operator admin) are
// disjoint: a request with an Authorization header is never admin.
func (a *authenticator) middleware(next http.Handler) http.Handler {
	adminNext := http.Handler(nil)
	if a.adminMW != nil {
		adminNext = a.adminMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ns, keyspace := adminScopeFromQuery(r)
			a.serveWithScope(w, r, next, ns, keyspace, true)
		}))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			// Bearer path: scope claims ride headers, and the token — which is
			// derived from exactly those claims — is what binds them.
			ns := r.Header.Get(HeaderStateNamespace)
			keyspace := r.Header.Get(HeaderStateKeyspace)
			if ns == "" || keyspace == "" {
				writeError(w, http.StatusBadRequest, "bad_request", "the "+HeaderStateNamespace+" and "+HeaderStateKeyspace+" headers are required")
				return
			}
			token, isBearer := strings.CutPrefix(auth, "Bearer ")
			if !isBearer {
				writeError(w, http.StatusUnauthorized, "unauthorized", "unsupported Authorization scheme")
				return
			}
			if !a.passThrough() && !a.verifyKeyspaceToken(token, ns, keyspace) {
				writeError(w, http.StatusForbidden, "forbidden", "token does not match the requested namespace/keyspace scope")
				return
			}
			a.serveWithScope(w, r, next, ns, keyspace, false)
			return
		}

		// Admin path fails closed: without a configured master secret there is
		// nothing to verify a signature against, so refuse rather than trust.
		if adminNext == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "admin access requires the internal auth secret (FISSION_INTERNAL_AUTH_SECRET) to be configured")
			return
		}
		// Admin scope claims must ride the QUERY STRING, not headers: the HMAC
		// canonical covers method+URI+body+timestamp but not headers, so
		// header-borne claims could be retargeted on a replayed signature
		// within the skew window. Query-borne claims are signature-covered.
		if r.URL.Query().Get(adminQueryNamespace) == "" || r.URL.Query().Get(adminQueryKeyspace) == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "admin requests must carry the "+adminQueryNamespace+" and "+adminQueryKeyspace+" query parameters (signature-covered), not scope headers")
			return
		}
		adminNext.ServeHTTP(w, r)
	})
}

// Admin scope query parameters — covered by the HMAC-signed request URI.
const (
	adminQueryNamespace = "scope-namespace"
	adminQueryKeyspace  = "scope-keyspace"
)

func (a *authenticator) serveWithScope(w http.ResponseWriter, r *http.Request, next http.Handler, ns, keyspace string, admin bool) {
	sc := authedScope{
		scope: statestore.Scope{
			Namespace: ns,
			Owner:     StateOwner,
			Keyspace:  keyspace,
		},
		admin: admin,
	}
	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), scopeCtxKey{}, sc)))
}

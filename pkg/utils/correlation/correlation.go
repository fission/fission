// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package correlation carries the per-invocation request id used to tie a
// single function call together across the router error response, the
// distributed trace, and the logs (RFC-0015). It deliberately has no router /
// executor dependency so every component can reference the header names and
// the context helpers.
package correlation

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// Header names used to correlate and attribute a single function invocation.
const (
	// HeaderRequestID carries the stable per-invocation id. The router honors
	// an inbound value (client-supplied correlation / idempotency) and mints
	// one otherwise; it is echoed on the response and propagated to the
	// function pod and the executor RPC.
	HeaderRequestID = "X-Fission-Request-ID"
	// HeaderComponent is echoed on structured error responses to name the
	// component that failed (router / executor / fetcher / function / timeout).
	HeaderComponent = "X-Fission-Component"
	// HeaderDebug, when set to "true" by the caller, opts a request into a
	// verbose error body (raw error detail). It is honored only when the
	// router itself runs in debug mode, so detail never leaks by default.
	HeaderDebug = "X-Fission-Debug"
)

// maxInboundIDLen bounds an accepted client-supplied request id. A value that
// is longer, empty, or carries anything outside printable, non-space ASCII is
// rejected and a fresh id is minted instead — keeping the id header-safe and
// log-safe regardless of what a caller sends.
const maxInboundIDLen = 192

type contextKey struct{}

var requestIDKey = contextKey{}

// validInbound reports whether a client-supplied id is safe to honor verbatim:
// 1..maxInboundIDLen printable ASCII characters with no spaces or control
// bytes. Anything else is replaced by a minted UUID.
func validInbound(s string) bool {
	if len(s) == 0 || len(s) > maxInboundIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x20 || c >= 0x7f {
			return false
		}
	}
	return true
}

// ID returns the request id for an invocation: the inbound value when it is
// safe to honor, otherwise a freshly minted UUID.
func ID(inbound string) string {
	if validInbound(inbound) {
		return inbound
	}
	return uuid.NewString()
}

// NewContext returns a copy of ctx carrying the request id.
func NewContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// FromContext returns the request id stored in ctx, or "" if none.
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// Middleware resolves the request id once per request — honoring an inbound
// X-Fission-Request-ID or minting one — then makes it available three ways:
// on the inbound request header (so the reverse proxy forwards it to the
// function pod and downstream handlers can read it), in the request context
// (for logging and the structured error body), and on the response header (so
// the caller can correlate). It sets the response header before calling the
// next handler, so it is in place even on the proxy error path.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ID(r.Header.Get(HeaderRequestID))
		r.Header.Set(HeaderRequestID, id)
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), id)))
	})
}

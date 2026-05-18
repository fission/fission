// Package httpsecurity provides composable middlewares that harden
// Fission's HTTP listeners against browser-driven cross-origin attacks.
//
// Three middlewares are exported:
//
//   - SecurityHeaders sets X-Content-Type-Options: nosniff and appends
//     Origin to the Vary header on every response. Safe to apply on any
//     listener; never rejects a request.
//   - DenyAllCORS rejects browser-driven cross-origin preflights with 403
//     and strips any Access-Control-* response header the inner handler
//     may have set. Intended for cluster-internal listeners.
//   - CORSAllowlist enforces an explicit per-origin allowlist for routes
//     that legitimately need cross-origin browser callers (router public
//     listener, per-HTTPTrigger config).
//
// The zero value of AllowlistConfig behaves like DenyAllCORS so triggers
// that don't set a CORS spec fall through to deny.
package httpsecurity

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SecurityHeaders adds X-Content-Type-Options: nosniff and appends
// "Origin" to the Vary header on every response. It composes safely with
// any other middleware: existing Vary entries set upstream or by the
// inner handler (e.g., Accept-Encoding from a gzip layer) are preserved.
//
// Headers are injected just before the response status line is written
// so that an inner handler calling Header().Set("Vary", "...") cannot
// clobber the Origin entry.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&securityHeadersWriter{ResponseWriter: w}, r)
	})
}

// securityHeadersWriter injects X-Content-Type-Options and appends Origin
// to Vary at the moment the response is committed. It forwards Hijack,
// Flush, and Push to the underlying ResponseWriter so WebSocket upgrades
// (router proxy path), SSE streaming, and HTTP/2 push continue to work.
type securityHeadersWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (s *securityHeadersWriter) WriteHeader(code int) {
	s.injectHeadersOnce()
	s.ResponseWriter.WriteHeader(code)
}

func (s *securityHeadersWriter) Write(b []byte) (int, error) {
	s.injectHeadersOnce()
	return s.ResponseWriter.Write(b)
}

// Hijack implements http.Hijacker so HTTP/1.1 connection upgrade
// (WebSocket, CONNECT) still works through this wrapper. After Hijack
// the wrapper no longer mediates the connection; security headers are
// already flushed (or never were, for a successful 101 upgrade where
// the client receives the response without going through Write).
func (s *securityHeadersWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return hijackOrErr(s.ResponseWriter)
}

func (s *securityHeadersWriter) Flush() {
	s.injectHeadersOnce()
	if fl, ok := s.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

func (s *securityHeadersWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := s.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

func (s *securityHeadersWriter) injectHeadersOnce() {
	if s.wroteHeader {
		return
	}
	s.wroteHeader = true
	h := s.Header()
	if h.Get("X-Content-Type-Options") == "" {
		h.Set("X-Content-Type-Options", "nosniff")
	}
	addVary(h, "Origin")
}

// DenyAllCORS rejects browser-driven cross-origin requests:
//   - A preflight (OPTIONS with both Origin and Access-Control-Request-Method)
//     from any origin returns 403 without invoking the inner handler.
//   - A non-preflight request with an Origin header is forwarded to the
//     inner handler, but any Access-Control-* response header the handler
//     emits is stripped before the response is sent. The browser's
//     Same-Origin Policy then blocks the cross-origin read because no
//     Access-Control-Allow-Origin header is echoed.
//
// Same-origin OPTIONS (no Access-Control-Request-Method, or Origin matches
// the request Host) are passed through unchanged so legitimate non-CORS
// preflights are not broken.
func DenyAllCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isCORSPreflight(r) {
			http.Error(w, "cross-origin requests not permitted", http.StatusForbidden)
			return
		}
		// Wrap so we can strip any Access-Control-* header the inner
		// handler set before it is sent on the wire.
		next.ServeHTTP(&corsStripper{ResponseWriter: w}, r)
	})
}

// AllowlistConfig is the per-route or per-listener CORS allowlist.
//
// An empty AllowOrigins (the zero value) behaves identically to
// DenyAllCORS — no Access-Control-Allow-Origin is echoed and cross-origin
// preflights are rejected. This means an HTTPTrigger without a CorsConfig
// falls through to deny by default.
type AllowlistConfig struct {
	// AllowOrigins are exact-match origins (scheme + host + port).
	// The single-element value ["*"] permits any origin. Mixing "*"
	// with AllowCredentials=true is a configuration error and panics
	// in CORSAllowlist.
	AllowOrigins []string

	// AllowMethods is the list of HTTP methods echoed in the
	// Access-Control-Allow-Methods preflight response.
	AllowMethods []string

	// AllowHeaders is the list of request headers the browser is
	// allowed to send, echoed in Access-Control-Allow-Headers.
	AllowHeaders []string

	// ExposeHeaders is the list of response headers exposed to the
	// browser, set in Access-Control-Expose-Headers.
	ExposeHeaders []string

	// AllowCredentials, when true, sets Access-Control-Allow-Credentials.
	// The CORS spec forbids combining this with AllowOrigins=["*"].
	AllowCredentials bool

	// MaxAge is the preflight cache lifetime sent in
	// Access-Control-Max-Age. Zero means the header is omitted.
	MaxAge time.Duration
}

// CORSAllowlist returns a middleware that enforces cfg. It panics if cfg
// is structurally invalid (AllowOrigins=["*"] with AllowCredentials=true);
// that combination would cause every browser to refuse the response, so
// it is treated as a caller bug rather than a runtime error.
func CORSAllowlist(cfg AllowlistConfig) func(http.Handler) http.Handler {
	if cfg.AllowCredentials && hasWildcard(cfg.AllowOrigins) {
		panic("httpsecurity: AllowOrigins=[\"*\"] cannot be combined with AllowCredentials=true")
	}

	// Pre-compute the static header values so the per-request hot
	// path does only one origin compare + a few header sets.
	allowMethods := strings.Join(cfg.AllowMethods, ", ")
	allowHeaders := strings.Join(cfg.AllowHeaders, ", ")
	exposeHeaders := strings.Join(cfg.ExposeHeaders, ", ")
	maxAge := ""
	if cfg.MaxAge > 0 {
		maxAge = strconv.Itoa(int(cfg.MaxAge.Seconds()))
	}

	return func(next http.Handler) http.Handler {
		// Empty AllowOrigins -> behave exactly like DenyAllCORS so
		// unconfigured triggers fall through to deny.
		if len(cfg.AllowOrigins) == 0 {
			return DenyAllCORS(next)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			allowed := originAllowed(origin, cfg.AllowOrigins)
			if isCORSPreflight(r) {
				if !allowed {
					http.Error(w, "cross-origin requests not permitted", http.StatusForbidden)
					return
				}
				writeAllowOrigin(w.Header(), origin, cfg.AllowOrigins)
				if allowMethods != "" {
					w.Header().Set("Access-Control-Allow-Methods", allowMethods)
				}
				if allowHeaders != "" {
					w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
				}
				if cfg.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				if maxAge != "" {
					w.Header().Set("Access-Control-Max-Age", maxAge)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if origin != "" && allowed {
				writeAllowOrigin(w.Header(), origin, cfg.AllowOrigins)
				if cfg.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				if exposeHeaders != "" {
					w.Header().Set("Access-Control-Expose-Headers", exposeHeaders)
				}
			}
			// Disallowed cross-origin actual request: forward to the
			// inner handler but do not echo Allow-Origin. The browser
			// SOP then blocks the read.
			next.ServeHTTP(w, r)
		})
	}
}

// isCORSPreflight reports whether r is a CORS preflight per the Fetch
// spec: OPTIONS plus both Origin and Access-Control-Request-Method
// headers. Plain OPTIONS without those headers (e.g., HTTP OPTIONS
// discovery) is not a CORS preflight and passes through.
func isCORSPreflight(r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	if r.Header.Get("Origin") == "" {
		return false
	}
	return r.Header.Get("Access-Control-Request-Method") != ""
}

// originAllowed returns true if origin is exactly listed in allowed or
// allowed contains the wildcard "*".
func originAllowed(origin string, allowed []string) bool {
	if origin == "" {
		return false
	}
	return slices.Contains(allowed, "*") || slices.Contains(allowed, origin)
}

func hasWildcard(origins []string) bool {
	return slices.Contains(origins, "*")
}

// writeAllowOrigin echoes the caller's Origin when an exact match was
// configured; if the allowlist is wildcard-only, the response uses "*".
// Using the literal origin (not "*") when possible improves cache
// behaviour under Vary: Origin.
func writeAllowOrigin(h http.Header, origin string, allowed []string) {
	if slices.Contains(allowed, origin) {
		h.Set("Access-Control-Allow-Origin", origin)
		return
	}
	// allowed contains "*" and the origin is not an exact match
	h.Set("Access-Control-Allow-Origin", "*")
}

// addVary appends value to the Vary header without clobbering existing
// entries. Duplicates are tolerated by browsers but we skip them to keep
// the header tidy.
func addVary(h http.Header, value string) {
	for _, entry := range h.Values("Vary") {
		for part := range strings.SplitSeq(entry, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return
			}
		}
	}
	h.Add("Vary", value)
}

// corsStripper wraps an http.ResponseWriter and removes any
// Access-Control-* header the inner handler may have set just before the
// status line is written. We intercept WriteHeader; for handlers that
// call Write without WriteHeader, the Go http package invokes
// WriteHeader(200) implicitly, which we also catch. Hijack/Flush/Push
// are forwarded so connection upgrades and streaming responses work.
type corsStripper struct {
	http.ResponseWriter
	wroteHeader bool
}

func (s *corsStripper) WriteHeader(code int) {
	if !s.wroteHeader {
		stripAccessControl(s.Header())
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *corsStripper) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		stripAccessControl(s.Header())
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *corsStripper) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return hijackOrErr(s.ResponseWriter)
}

func (s *corsStripper) Flush() {
	if fl, ok := s.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

func (s *corsStripper) Push(target string, opts *http.PushOptions) error {
	if p, ok := s.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

// hijackOrErr delegates Hijack to the wrapped ResponseWriter if it
// implements http.Hijacker, otherwise returns http.ErrNotSupported wrapped
// for the caller to surface. Centralised so both wrappers behave
// identically.
func hijackOrErr(w http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("httpsecurity: underlying ResponseWriter does not implement http.Hijacker")
}

func stripAccessControl(h http.Header) {
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			h.Del(k)
		}
	}
}

// String returns a human-readable summary of cfg, useful for logging
// when a trigger reconciles with a new CORS spec.
func (c AllowlistConfig) String() string {
	return fmt.Sprintf("origins=%v methods=%v headers=%v expose=%v creds=%t maxAge=%s",
		c.AllowOrigins, c.AllowMethods, c.AllowHeaders, c.ExposeHeaders, c.AllowCredentials, c.MaxAge)
}

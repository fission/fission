// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package httpmux is Fission's internal HTTP router: a small, SOLID layer over
// net/http that replaces gorilla/mux. It owns route matching and dispatch
// (method/host/exact/prefix matching, path-variable extraction, the matched
// pattern), wires a middleware chain, and drives optional per-route metrics —
// keeping those concerns out of the metrics/auth packages.
//
// Routes are matched in REGISTRATION ORDER, first match wins; callers control
// precedence by the order they register (the router feeds the precedence-
// ordered routetable.Materialization). Patterns may be static paths, prefixes,
// or {var}/{var:regexp} templates (see template.go).
//
// Paths are matched LITERALLY against r.URL.Path (or r.URL.EscapedPath under
// WithEncodedPath): httpmux does NOT clean or redirect non-canonical paths
// (".", "..", "//") the way gorilla/mux does by default. This is safe for exact,
// method-gated routes behind an outermost auth verifier (a non-canonical path
// simply 404s). A future consumer that routes security-sensitive paths — the
// router's HMAC-signed internal listener, which signs the raw request-URI — must
// decide its normalization policy explicitly and test "..", "//", and
// encoded-slash inputs at that boundary.
package httpmux

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// MatchKind selects how a route's pattern is matched against the request path.
type MatchKind uint8

const (
	// Exact matches the request path equal to the pattern.
	Exact MatchKind = iota
	// Prefix matches when the request path starts with the pattern.
	Prefix
)

// Route is one registration. Handle/HandlePrefix return it so callers can
// fluently restrict the method set and host (gorilla-style, keeping method and
// path separate for readability).
type Route struct {
	pattern string
	kind    MatchKind
	methods []string // nil/empty = any method
	host    string   // "" = any host
	handler http.Handler
}

// Methods restricts the route to the given HTTP methods (case-insensitive).
func (r *Route) Methods(methods ...string) *Route {
	r.methods = methods
	return r
}

// Host restricts the route to requests for the given (exact) host.
func (r *Route) Host(host string) *Route {
	r.host = host
	return r
}

func (r *Route) matchesMethod(method string) bool {
	if len(r.methods) == 0 {
		return true
	}
	for _, m := range r.methods {
		if strings.EqualFold(m, method) {
			return true
		}
	}
	return false
}

// Mux accumulates routes, middleware, and options, then compiles them into an
// http.Handler via Handler(). It is configured up front and not mutated after
// Handler() is called; the router builds a fresh Mux per route-table change and
// swaps it atomically.
type Mux struct {
	routes      []*Route
	middleware  []func(http.Handler) http.Handler
	recorder    Recorder
	encodedPath bool
}

// Option configures a Mux at construction.
type Option func(*Mux)

// WithMiddleware adds middleware wrapped OUTERMOST in registration order (the
// first added runs first), around the matched handler — e.g. an HMAC verifier.
func WithMiddleware(mw ...func(http.Handler) http.Handler) Option {
	return func(m *Mux) { m.middleware = append(m.middleware, mw...) }
}

// WithMetrics enables per-route metrics: each matched route is instrumented
// with rec, labelled by the route's pattern. A nil Recorder disables it.
func WithMetrics(rec Recorder) Option {
	return func(m *Mux) { m.recorder = rec }
}

// WithEncodedPath matches against the raw (percent-encoded) request path
// (r.URL.EscapedPath) instead of the decoded one — replaces gorilla's
// UseEncodedPath.
func WithEncodedPath() Option {
	return func(m *Mux) { m.encodedPath = true }
}

// New returns a Mux configured by opts.
func New(opts ...Option) *Mux {
	m := &Mux{}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Handle registers an exact-path route and returns it for fluent configuration.
func (m *Mux) Handle(pattern string, h http.Handler) *Route {
	return m.add(pattern, Exact, h)
}

// HandleFunc is Handle for an http.HandlerFunc.
func (m *Mux) HandleFunc(pattern string, h http.HandlerFunc) *Route {
	return m.add(pattern, Exact, h)
}

// HandlePrefix registers a prefix (subtree) route and returns it for fluent
// configuration.
func (m *Mux) HandlePrefix(pattern string, h http.Handler) *Route {
	return m.add(pattern, Prefix, h)
}

func (m *Mux) add(pattern string, kind MatchKind, h http.Handler) *Route {
	r := &Route{pattern: pattern, kind: kind, handler: h}
	m.routes = append(m.routes, r)
	return r
}

// Constant patterns under which unmatched requests are recorded, so 404/405
// stay visible in metrics WITHOUT the unbounded label cardinality that
// recording the raw request path would cause (e.g. path-scanning probes).
const (
	patternNotFound         = "<not found>"
	patternMethodNotAllowed = "<method not allowed>"
)

// Handler compiles the registered routes into an http.Handler: each route's
// handler is instrumented (if metrics are enabled), then the dispatcher is
// wrapped with the middleware chain. Safe to call once configuration is done.
func (m *Mux) Handler() http.Handler {
	compiled := make([]*compiledRoute, len(m.routes))
	for i, r := range m.routes {
		// Templates compile to a regexp. A compile error here is a registration
		// bug: callers handling user-supplied patterns (the router) must reject
		// them up front via CompilePattern. Failing loud at build time surfaces
		// it, rather than silently leaving a dead, never-matching route.
		re, err := compilePattern(r.pattern, r.kind)
		if err != nil {
			panic(fmt.Errorf("httpmux: route %q has an invalid template (validate with CompilePattern before registering): %w", r.pattern, err))
		}
		compiled[i] = &compiledRoute{route: r, handler: instrument(m.recorder, r.pattern, r.handler), re: re}
	}
	var h http.Handler = &dispatcher{
		routes:           compiled,
		encodedPath:      m.encodedPath,
		notFound:         instrument(m.recorder, patternNotFound, http.HandlerFunc(http.NotFound)),
		methodNotAllowed: instrument(m.recorder, patternMethodNotAllowed, http.HandlerFunc(methodNotAllowedHandler)),
	}
	// Apply middleware so the first-added wraps outermost (runs first).
	for i := len(m.middleware) - 1; i >= 0; i-- {
		h = m.middleware[i](h)
	}
	return h
}

func methodNotAllowedHandler(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)
}

type compiledRoute struct {
	route   *Route
	handler http.Handler   // instrumented
	re      *regexp.Regexp // non-nil for {var}/{var:regexp} templates
}

// matchPath reports whether path matches the route and returns any extracted
// path variables (nil for static routes).
func (cr *compiledRoute) matchPath(path string) (bool, map[string]string) {
	if cr.re != nil {
		sub := cr.re.FindStringSubmatch(path)
		if sub == nil {
			return false, nil
		}
		var vars map[string]string
		for i, name := range cr.re.SubexpNames() {
			if name != "" {
				if vars == nil {
					vars = make(map[string]string, len(sub))
				}
				vars[name] = sub[i]
			}
		}
		return true, vars
	}
	if cr.route.kind == Prefix {
		return strings.HasPrefix(path, cr.route.pattern), nil
	}
	return path == cr.route.pattern, nil
}

// dispatcher is the built matcher: it scans routes in registration order and
// dispatches the first full match, falling back to instrumented 405/404
// handlers.
type dispatcher struct {
	routes           []*compiledRoute
	encodedPath      bool
	notFound         http.Handler
	methodNotAllowed http.Handler
}

func (d *dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if d.encodedPath {
		path = r.URL.EscapedPath()
	}
	methodNotAllowed := false
	for _, cr := range d.routes {
		rt := cr.route
		if rt.host != "" && rt.host != r.Host {
			continue
		}
		ok, vars := cr.matchPath(path)
		if !ok {
			continue
		}
		if !rt.matchesMethod(r.Method) {
			// Path matched but method didn't: remember for 405, but keep
			// scanning — a later route may match this request fully.
			methodNotAllowed = true
			continue
		}
		cr.handler.ServeHTTP(w, withMatch(r, rt.pattern, vars))
		return
	}
	if methodNotAllowed {
		d.methodNotAllowed.ServeHTTP(w, r)
		return
	}
	d.notFound.ServeHTTP(w, r)
}

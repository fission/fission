// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/bep/debounce"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/utils/httpmux"
	"github.com/fission/fission/pkg/utils/httpsecurity"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// muxMatches reports whether the mux would dispatch `path` (with `method`) to a
// registered route. It is used in unit tests to assert route registration
// without actually running the downstream handler — handlers in this package
// require additional tsRoundTripperParams plumbing that has no place in a
// routing test (httpmux.Match answers "which route", not "what response").
func muxMatches(m *httpmux.Mux, method, path string) bool {
	_, ok := m.Match(httptest.NewRequest(method, path, nil))
	return ok
}

// newTestTriggerSet builds an HTTPTriggerSet wired with a minimal
// resolver and no Kubernetes informers. It is intended only for tests
// that exercise buildMuxes.
func newTestTriggerSet(t *testing.T, functions []fv1.Function, triggers []fv1.HTTPTrigger) *HTTPTriggerSet {
	t.Helper()
	logger := loggerfactory.GetLogger()
	ts := &HTTPTriggerSet{
		logger:                     logger.WithName("test_trigger_set"),
		triggers:                   triggers,
		functions:                  functions,
		updateRouterRequestChannel: make(chan struct{}, 1),
		syncDebouncer:              debounce.New(time.Millisecond),
	}
	// resolver only matters when triggers are present; supply a stub
	// so resolve() does not panic on an empty trigger list.
	ts.resolver = makeFunctionReferenceResolver(ts.logger, nil)
	return ts
}

// TestPublicMuxDoesNotRegisterInternalFunctionRoute is the GHSA-3g33-6vg6-27m8
// regression guard: the public listener must NOT have a route
// registered for /fission-function/<ns>/<name>, while the internal
// listener MUST. We check route registration via mux.Match rather
// than driving the handler — the handler relies on tsRoundTripperParams
// and a live functionService cache, neither of which belong in a unit
// test for route shape.
func TestPublicMuxDoesNotRegisterInternalFunctionRoute(t *testing.T) {
	// Use a non-default namespace so the registered route follows the
	// /fission-function/<ns>/<name> form (utils.UrlForFunction folds
	// the default namespace into /fission-function/<name>).
	fn := fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "myns"},
	}
	ts := newTestTriggerSet(t, []fv1.Function{fn}, nil)

	publicMux, internalMux, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	// Public mux must NOT have the internal-only route.
	assert.False(t, muxMatches(publicMux, http.MethodGet, "/fission-function/myns/example"),
		"public listener must not route /fission-function/myns/example")
	assert.False(t, muxMatches(publicMux, http.MethodPost, "/fission-function/myns/example/sub"),
		"public listener must not route /fission-function/myns/example/sub")

	// And calling ServeHTTP on the public mux must 404 the internal
	// path (httpmux's dispatcher 404s when no route matches, so this is
	// the user-visible behaviour).
	rr := httptest.NewRecorder()
	publicMux.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/fission-function/myns/example", nil))
	assert.Equal(t, http.StatusNotFound, rr.Code,
		"public listener must respond 404 for /fission-function/...")

	// Internal mux must route the same path.
	assert.True(t, muxMatches(internalMux, http.MethodGet, "/fission-function/myns/example"),
		"internal listener must route /fission-function/myns/example")
	assert.True(t, muxMatches(internalMux, http.MethodPost, "/fission-function/myns/example/sub"),
		"internal listener must route /fission-function/myns/example/sub via the prefix handler")
}

// TestInternalListenerLiteralPathMatching pins the Phase-3 path-normalization
// decision for the HMAC-signed internal listener: httpmux matches paths
// LITERALLY and does NOT clean "." / ".." / "//" the way gorilla did by
// default. This is the correct policy here — the listener verifies an HMAC over
// the RAW request-URI, so gorilla's clean-and-301-redirect would have produced
// a path the signature no longer covers; and legit publishers always build
// clean paths via utils.UrlForFunction. Non-canonical forms therefore simply
// fail to match (404) rather than being silently rewritten onto a function.
func TestInternalListenerLiteralPathMatching(t *testing.T) {
	fn := fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "myns"}}
	ts := newTestTriggerSet(t, []fv1.Function{fn}, nil)
	_, internalMux, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	// Canonical paths match: the exact route and its slash-prefix subtree.
	assert.True(t, muxMatches(internalMux, http.MethodPost, "/fission-function/myns/example"),
		"the canonical function path must match")
	assert.True(t, muxMatches(internalMux, http.MethodPost, "/fission-function/myns/example/sub"),
		"a clean subpath must match the prefix route")

	// Non-canonical forms are matched literally — i.e. not at all — so they
	// can neither sneak onto a function route nor trigger a surprise redirect.
	for _, p := range []string{
		"/fission-function/myns//example",        // doubled slash
		"/fission-function/myns/./example",       // dot segment
		"/fission-function/myns/../myns/example", // parent traversal
	} {
		assert.False(t, muxMatches(internalMux, http.MethodPost, p),
			"non-canonical path %q must not match (httpmux matches literally, no cleaning)", p)
	}
}

// TestPublicMuxStillServesHealthAndVersion ensures the listener split
// did not accidentally move /router-healthz or /_version onto the
// internal listener; readiness probes and external monitors must still
// hit them on the public port.
func TestPublicMuxStillServesHealthAndVersion(t *testing.T) {
	ts := newTestTriggerSet(t, nil, nil)

	publicMux, internalMux, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	publicMux.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/router-healthz", nil))
	assert.Equal(t, http.StatusOK, rr.Code, "/router-healthz must stay on the public listener")

	rr = httptest.NewRecorder()
	publicMux.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/_version", nil))
	assert.Equal(t, http.StatusOK, rr.Code, "/_version must stay on the public listener")

	// Symmetric guard: the internal listener must NOT serve
	// /router-healthz or /_version (so cluster monitors can't probe it
	// without HMAC creds — which would otherwise mask a misconfigured
	// listener).
	rr = httptest.NewRecorder()
	internalMux.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/router-healthz", nil))
	assert.Equal(t, http.StatusNotFound, rr.Code, "internal listener must not serve /router-healthz")

	rr = httptest.NewRecorder()
	internalMux.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/_version", nil))
	assert.Equal(t, http.StatusNotFound, rr.Code, "internal listener must not serve /_version")
}

// TestInternalListenerRejectsUnsignedRequests demonstrates that the
// HMAC verifier wrapper used by the bundle returns 401 for a
// /fission-function/... request that arrives without the required
// X-Fission-Auth-* headers. This guards the integration-test
// expectation that port 8889 returns 401 for unsigned requests.
func TestInternalListenerRejectsUnsignedRequests(t *testing.T) {
	// Use a non-default namespace so the registered route follows the
	// /fission-function/<ns>/<name> form (utils.UrlForFunction folds
	// the default namespace into /fission-function/<name>).
	fn := fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "myns"},
	}
	ts := newTestTriggerSet(t, []fv1.Function{fn}, nil)

	_, internalMux, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	// Mirror the production wiring: ServiceVerifier with ServiceRouterInternal
	// rather than the raw Verifier. A regression in service-key derivation
	// (e.g. accidentally using a different service id on the signing side)
	// would surface here in addition to the lower-level keys_test.go suite.
	verifier := hmacauth.ServiceVerifier([]byte("test-master"), nil, hmacauth.ServiceRouterInternal, hmacauth.VerifierOpts{
		SkewSec:      60,
		MaxBodyBytes: internalListenerMaxBodyBytes,
	})
	wrapped := verifier(internalMux.Handler())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/fission-function/myns/example", nil)
	wrapped.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code,
		"internal listener must 401 unsigned requests when a secret is configured")
}

// TestInternalListenerPassThroughWithEmptySecret pins the documented
// rollout behaviour: when FISSION_INTERNAL_AUTH_SECRET is unset the
// verifier short-circuits and the request is forwarded to the
// downstream handler. We use a sentinel handler in place of the
// production functionHandler so the test focuses on the pass-through
// short-circuit; the functionHandler itself needs additional plumbing
// that is out of scope for a routing test.
func TestInternalListenerPassThroughWithEmptySecret(t *testing.T) {
	// Sanity-pin the env var so the test does not pick up a stale
	// value from the developer's shell.
	t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "")
	require.Empty(t, os.Getenv("FISSION_INTERNAL_AUTH_SECRET"))

	called := false
	sentinel := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})

	// Use ServiceVerifier to match the production wiring; an empty
	// master propagates to an empty derived key, which the underlying
	// Verifier short-circuits to pass-through.
	verifier := hmacauth.ServiceVerifier(nil, nil, hmacauth.ServiceRouterInternal, hmacauth.VerifierOpts{
		SkewSec:      60,
		MaxBodyBytes: internalListenerMaxBodyBytes,
	})
	wrapped := verifier(sentinel)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/fission-function/myns/example", nil)
	wrapped.ServeHTTP(rr, req)
	assert.True(t, called, "empty secret must short-circuit the verifier and call downstream")
	assert.Equal(t, http.StatusTeapot, rr.Code, "downstream sentinel must respond")
}

// TestPublicListener_SecurityHeadersPresentOnRouterOwnedRoutes pins the
// round-3 wrap: every response on the public listener carries
// X-Content-Type-Options: nosniff and Vary: Origin. We mirror the
// production handler chain (SecurityHeaders → mux) so a regression in
// router.go's wrap surfaces here.
func TestPublicListener_SecurityHeadersPresentOnRouterOwnedRoutes(t *testing.T) {
	ts := newTestTriggerSet(t, nil, nil)
	publicMux, _, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)
	wrapped := httpsecurity.SecurityHeaders(publicMux.Handler())

	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/router-healthz", nil))
	assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"),
		"/router-healthz response must carry X-Content-Type-Options: nosniff")
	assert.Contains(t, rr.Header().Get("Vary"), "Origin",
		"/router-healthz response must carry Vary: Origin")
}

// TestPublicListener_RouterOwnedRoutesRejectCrossOriginPreflight pins the
// round-3 per-route DenyAllCORS wrap on router-owned routes. Each route
// registers OPTIONS alongside its real verb so the mux routes the
// preflight to the wrapped DenyAllCORS handler, which returns 403.
func TestPublicListener_RouterOwnedRoutesRejectCrossOriginPreflight(t *testing.T) {
	ts := newTestTriggerSet(t, nil, nil)
	publicMux, _, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	for _, path := range []string{"/router-healthz", "/_version", "/"} {
		t.Run(path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodOptions, path, nil)
			req.Header.Set("Origin", "https://attacker.example")
			req.Header.Set("Access-Control-Request-Method", "GET")
			publicMux.Handler().ServeHTTP(rr, req)
			assert.Equal(t, http.StatusForbidden, rr.Code,
				"cross-origin preflight to %s must be 403 from DenyAllCORS", path)
		})
	}
}

// TestToAllowlistConfig pins the per-trigger CORS adapter: it converts
// the user-facing CRD spec into the httpsecurity AllowlistConfig,
// parses MaxAge as a time.Duration, and falls back to the trigger's
// HTTP methods when AllowMethods is unset.
func TestToAllowlistConfig(t *testing.T) {
	t.Run("explicit AllowMethods preferred over trigger methods", func(t *testing.T) {
		cfg := toAllowlistConfig(&fv1.HTTPTriggerCorsConfig{
			AllowOrigins: []string{"https://app.example.com"},
			AllowMethods: []string{"GET", "POST"},
			MaxAge:       "10m",
		}, []string{"GET"})
		assert.Equal(t, []string{"https://app.example.com"}, cfg.AllowOrigins)
		assert.Equal(t, []string{"GET", "POST"}, cfg.AllowMethods)
		assert.Equal(t, 10*time.Minute, cfg.MaxAge)
	})
	t.Run("empty AllowMethods falls back to trigger methods", func(t *testing.T) {
		cfg := toAllowlistConfig(&fv1.HTTPTriggerCorsConfig{
			AllowOrigins: []string{"https://app.example.com"},
		}, []string{"GET", "POST"})
		assert.Equal(t, []string{"GET", "POST"}, cfg.AllowMethods,
			"AllowMethods unset must fall back to the trigger's allowed methods")
	})
	t.Run("malformed MaxAge defaults to zero", func(t *testing.T) {
		// Validation rejects this at admission, but defense-in-depth at
		// the adapter level guarantees the middleware never panics on a
		// bad duration.
		cfg := toAllowlistConfig(&fv1.HTTPTriggerCorsConfig{
			AllowOrigins: []string{"https://app.example.com"},
			MaxAge:       "garbage",
		}, nil)
		assert.Equal(t, time.Duration(0), cfg.MaxAge,
			"unparseable MaxAge must fall through to zero, not panic")
	})
	t.Run("AllowCredentials and ExposeHeaders carried through", func(t *testing.T) {
		cfg := toAllowlistConfig(&fv1.HTTPTriggerCorsConfig{
			AllowOrigins:     []string{"https://app.example.com"},
			ExposeHeaders:    []string{"X-Request-Id"},
			AllowHeaders:     []string{"Authorization"},
			AllowCredentials: true,
		}, []string{"GET"})
		assert.True(t, cfg.AllowCredentials)
		assert.Equal(t, []string{"X-Request-Id"}, cfg.ExposeHeaders)
		assert.Equal(t, []string{"Authorization"}, cfg.AllowHeaders)
	})
}

// TestInternalListener_RejectsCrossOriginPreflight pins the round-3
// DenyAllCORS wrap on the internal listener. A browser-driven preflight
// must 403 before the HMAC verifier even reads the body.
func TestInternalListener_RejectsCrossOriginPreflight(t *testing.T) {
	fn := fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "myns"},
	}
	ts := newTestTriggerSet(t, []fv1.Function{fn}, nil)
	_, internalMux, err := ts.buildMuxes(t.Context(), nil)
	require.NoError(t, err)

	// Mirror the production wrap chain from router.go:Start: HMAC
	// verifier inside, DenyAllCORS outside it, SecurityHeaders outermost.
	verifier := hmacauth.ServiceVerifier([]byte("test-master"), nil, hmacauth.ServiceRouterInternal, hmacauth.VerifierOpts{
		SkewSec:      60,
		MaxBodyBytes: internalListenerMaxBodyBytes,
	})
	wrapped := httpsecurity.SecurityHeaders(httpsecurity.DenyAllCORS(verifier(internalMux.Handler())))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/fission-function/myns/example", nil)
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	wrapped.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code,
		"internal listener must 403 cross-origin preflight before HMAC")
	assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"),
		"even the 403 must carry X-Content-Type-Options: nosniff")
}

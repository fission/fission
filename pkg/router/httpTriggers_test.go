/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/bep/debounce"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// muxMatches reports whether the gorilla/mux router would dispatch
// `path` (with `method`) to a registered route. It is used in unit
// tests to assert route registration without actually running the
// downstream handler — handlers in this package require additional
// tsRoundTripperParams plumbing that has no place in a routing test.
func muxMatches(r *mux.Router, method, path string) bool {
	req := httptest.NewRequest(method, path, nil)
	var match mux.RouteMatch
	return r.Match(req, &match) && match.Handler != nil
}

// newTestTriggerSet builds an HTTPTriggerSet wired with a minimal
// resolver and no Kubernetes informers. It is intended only for tests
// that exercise buildMuxes.
func newTestTriggerSet(t *testing.T, functions []fv1.Function, triggers []fv1.HTTPTrigger) *HTTPTriggerSet {
	t.Helper()
	logger := loggerfactory.GetLogger()
	ts := &HTTPTriggerSet{
		logger:                     logger.WithName("test_trigger_set"),
		functionServiceMap:         makeFunctionServiceMap(logger, time.Minute),
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

	publicMux, internalMux, err := ts.buildMuxes(nil)
	require.NoError(t, err)

	// Public mux must NOT have the internal-only route.
	assert.False(t, muxMatches(publicMux, http.MethodGet, "/fission-function/myns/example"),
		"public listener must not route /fission-function/myns/example")
	assert.False(t, muxMatches(publicMux, http.MethodPost, "/fission-function/myns/example/sub"),
		"public listener must not route /fission-function/myns/example/sub")

	// And calling ServeHTTP on the public mux must 404 the internal
	// path (the gorilla/mux NotFoundHandler defaults to 404 when no
	// route matches, so this is the user-visible behaviour).
	rr := httptest.NewRecorder()
	publicMux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/fission-function/myns/example", nil))
	assert.Equal(t, http.StatusNotFound, rr.Code,
		"public listener must respond 404 for /fission-function/...")

	// Internal mux must route the same path.
	assert.True(t, muxMatches(internalMux, http.MethodGet, "/fission-function/myns/example"),
		"internal listener must route /fission-function/myns/example")
	assert.True(t, muxMatches(internalMux, http.MethodPost, "/fission-function/myns/example/sub"),
		"internal listener must route /fission-function/myns/example/sub via the prefix handler")
}

// TestPublicMuxStillServesHealthAndVersion ensures the listener split
// did not accidentally move /router-healthz or /_version onto the
// internal listener; readiness probes and external monitors must still
// hit them on the public port.
func TestPublicMuxStillServesHealthAndVersion(t *testing.T) {
	ts := newTestTriggerSet(t, nil, nil)

	publicMux, internalMux, err := ts.buildMuxes(nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	publicMux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/router-healthz", nil))
	assert.Equal(t, http.StatusOK, rr.Code, "/router-healthz must stay on the public listener")

	rr = httptest.NewRecorder()
	publicMux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/_version", nil))
	assert.Equal(t, http.StatusOK, rr.Code, "/_version must stay on the public listener")

	// Symmetric guard: the internal listener must NOT serve /healthz or
	// /_version (so cluster monitors can't probe it without HMAC creds
	// — which would otherwise mask a misconfigured listener).
	rr = httptest.NewRecorder()
	internalMux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/router-healthz", nil))
	assert.Equal(t, http.StatusNotFound, rr.Code, "internal listener must not serve /router-healthz")
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

	_, internalMux, err := ts.buildMuxes(nil)
	require.NoError(t, err)

	verifier := hmacauth.Verifier(hmacauth.VerifierOpts{
		Secret:       []byte("test-secret"),
		SkewSec:      60,
		MaxBodyBytes: internalListenerMaxBodyBytes,
	})
	wrapped := verifier(internalMux)

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

	verifier := hmacauth.Verifier(hmacauth.VerifierOpts{
		Secret:       nil,
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

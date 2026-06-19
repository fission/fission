// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpmux

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMethodMismatchFallsThroughToLaterMatch pins the most refactor-fragile line
// in the dispatcher: a method-mismatching route earlier in the scan must NOT
// short-circuit to 405 — a later route that fully matches still wins. An early
// `return 405` would pass every other test but break real multi-method paths.
func TestMethodMismatchFallsThroughToLaterMatch(t *testing.T) {
	t.Parallel()
	m := New()
	m.Handle("/p", ok("get")).Methods("GET")
	m.Handle("/p", ok("post")).Methods("POST")
	h := m.Handler()
	assert.Equal(t, "post", do(t, h, http.MethodPost, "/p").Body.String(),
		"POST must reach the second route, not 405 on the first")
	assert.Equal(t, "get", do(t, h, http.MethodGet, "/p").Body.String())
}

// TestMethodNotAllowedAcrossRoutes: the path matches several routes but no
// method matches → 405 (not 404), proving the flag survives the whole scan.
func TestMethodNotAllowedAcrossRoutes(t *testing.T) {
	t.Parallel()
	m := New()
	m.Handle("/p", ok("")).Methods("GET")
	m.Handle("/p", ok("")).Methods("HEAD")
	assert.Equal(t, http.StatusMethodNotAllowed, do(t, m.Handler(), http.MethodPost, "/p").Code)
}

// TestHostMismatchIs404Not405: a host-restricted route whose host doesn't match
// must be skipped BEFORE the method check, so it can't poison the 405 flag — a
// request for a different host that matches nothing is 404, not 405.
func TestHostMismatchIs404Not405(t *testing.T) {
	t.Parallel()
	m := New()
	m.Handle("/x", ok("")).Methods("GET").Host("a.com")
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Host = "b.com"
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// TestMetricsDefaultStatus200: a handler that writes a body but never calls
// WriteHeader is recorded as 200 (the responseRecorder's default status).
func TestMetricsDefaultStatus200(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	m := New(WithMetrics(rec))
	m.HandleFunc("/x", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hi")) // implicit 200, no WriteHeader call
	}).Methods("GET")
	do(t, m.Handler(), http.MethodGet, "/x")
	assert.Equal(t, []string{"/x GET 200"}, rec.observed)
}

// TestHandlerPanicsOnInvalidTemplate: a malformed template registered without
// prior CompilePattern validation is a programming error — Handler() must fail
// loud, not silently create a dead (never-matching) route.
func TestHandlerPanicsOnInvalidTemplate(t *testing.T) {
	t.Parallel()
	m := New()
	m.Handle("/a/{id", ok("")).Methods("GET") // unbalanced brace
	assert.Panics(t, func() { _ = m.Handler() })
}

// TestTemplateMultipleVars: a template with several variables extracts them all
// (guards the SubexpNames index mapping that a single-var test can't catch).
func TestTemplateMultipleVars(t *testing.T) {
	t.Parallel()
	var gotVars map[string]string
	m := New()
	m.HandleFunc("/{a}/{b}", func(w http.ResponseWriter, r *http.Request) {
		gotVars = Vars(r)
		w.WriteHeader(http.StatusOK)
	}).Methods("GET")
	do(t, m.Handler(), http.MethodGet, "/x/y")
	assert.Equal(t, map[string]string{"a": "x", "b": "y"}, gotVars)
}

// TestTemplateWithEncodedPath: under WithEncodedPath a template matches the raw
// (escaped) path and the captured var keeps the raw encoding — the shape the
// router's /fission-function/{name} hot path runs under USE_ENCODED_PATH.
func TestTemplateWithEncodedPath(t *testing.T) {
	t.Parallel()
	var gotVars map[string]string
	m := New(WithEncodedPath())
	m.HandleFunc("/fn/{name}", func(w http.ResponseWriter, r *http.Request) {
		gotVars = Vars(r)
		w.WriteHeader(http.StatusOK)
	}).Methods("GET")
	req := httptest.NewRequest(http.MethodGet, "/fn/a%20b", nil)
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "a%20b", gotVars["name"], "matches EscapedPath; var keeps the raw encoding")
}

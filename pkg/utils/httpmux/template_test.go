// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpmux

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTemplateMatchingAndVars(t *testing.T) {
	t.Parallel()
	var gotVars map[string]string
	m := New()
	m.HandleFunc("/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		gotVars = Vars(r)
		w.WriteHeader(http.StatusOK)
	}).Methods("GET")
	h := m.Handler()

	rr := do(t, h, http.MethodGet, "/accounts/abc123")
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, map[string]string{"id": "abc123"}, gotVars, "extracted path var feeds X-Fission-Params")

	// A bare {id} is a single segment, so an embedded slash does not match.
	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/accounts/a/b").Code)
}

func TestTemplateRegexSpanningSlash(t *testing.T) {
	t.Parallel()
	// Real Fission trigger (routeshape_test.go): a regex var whose class
	// includes '/' matches multiple path segments.
	var gotVars map[string]string
	m := New()
	m.HandleFunc(`/bank/{html:[a-zA-Z0-9\./]+}`, func(w http.ResponseWriter, r *http.Request) {
		gotVars = Vars(r)
		w.WriteHeader(http.StatusOK)
	}).Methods("GET")

	rr := do(t, m.Handler(), http.MethodGet, "/bank/foo/bar/baz.html")
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "foo/bar/baz.html", gotVars["html"])
}

func TestTemplateCapturingGroupAccepted(t *testing.T) {
	t.Parallel()
	// gorilla PANICS on a capturing group like (asc|desc); httpmux uses named
	// groups, so this previously-rejected pattern compiles and works.
	require.NoError(t, CompilePattern("/sort/{by:(asc|desc)}", Exact))
	m := New()
	m.HandleFunc("/sort/{by:(asc|desc)}", ok("")).Methods("GET")
	h := m.Handler()
	assert.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "/sort/asc").Code)
	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/sort/sideways").Code)
}

func TestTemplatePrefix(t *testing.T) {
	t.Parallel()
	m := New()
	m.HandlePrefix("/files/{dir}/", ok("prefix")).Methods("GET")
	// Prefix template is anchored at the start only.
	assert.Equal(t, "prefix", do(t, m.Handler(), http.MethodGet, "/files/docs/readme").Body.String())
}

func TestCompilePatternValidation(t *testing.T) {
	t.Parallel()
	ok := []string{"/static/path", "/a/{id}", `/bank/{html:[a-zA-Z0-9\./]+}`, "/a/{id:[0-9]{3}}", "/sort/{by:(asc|desc)}"}
	for _, p := range ok {
		assert.NoErrorf(t, CompilePattern(p, Exact), "valid pattern %q", p)
	}
	bad := []string{"/a/{id", "/a/{}", "/a/{id:[}"} // unbalanced, empty name, uncompilable regexp
	for _, p := range bad {
		assert.Errorf(t, CompilePattern(p, Exact), "malformed pattern %q must error, not panic", p)
	}
}

func TestTemplateFirstMatchOrderWithStatic(t *testing.T) {
	t.Parallel()
	// A static route registered before a template that would also match wins
	// (registration order is precedence — the router relies on this).
	m := New()
	m.Handle("/accounts/me", ok("static")).Methods("GET")
	m.Handle("/accounts/{id}", ok("template")).Methods("GET")
	h := m.Handler()
	assert.Equal(t, "static", do(t, h, http.MethodGet, "/accounts/me").Body.String())
	assert.Equal(t, "template", do(t, h, http.MethodGet, "/accounts/42").Body.String())
}

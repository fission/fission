// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpmux

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- White-box eligibility: which literal exacts make it into the index ---

// TestExactIndexEligibility pins buildExactIndex's shadowing rule directly: a
// literal exact is indexed only when no template or prefix route matches its
// path.
func TestExactIndexEligibility(t *testing.T) {
	t.Parallel()
	m := New()
	m.Handle("/plain", ok(""))                // literal exact, nothing shadows → indexed
	m.Handle("/dup", ok("")).Methods("GET")   // two literal exacts at one path...
	m.Handle("/dup", ok("")).Methods("POST")  // ...both share the bucket → indexed
	m.Handle("/api/v1", ok(""))               // literal exact, but shadowed by the prefix below
	m.HandlePrefix("/api/", ok(""))           // prefix matcher → shadows /api/v1
	m.Handle("/accounts/me", ok(""))          // literal exact, shadowed by the template below
	m.Handle("/accounts/{id}", ok(""))        // template matcher → shadows /accounts/me
	m.Handle("/sort/{by:(asc|desc)}", ok("")) // template exact (re != nil) → never indexed itself

	idx := buildExactIndex(m.compile())

	assert.Contains(t, idx, "/plain", "an unshadowed literal exact is indexed")
	assert.Len(t, idx["/dup"], 2, "both literal exacts at /dup share the bucket")
	assert.NotContains(t, idx, "/api/v1", "a literal exact under a prefix is NOT indexed (prefix may win)")
	assert.NotContains(t, idx, "/accounts/me", "a literal exact a template matches is NOT indexed")
	assert.NotContains(t, idx, "/accounts/{id}", "a template (re != nil) is never a literal-exact key")
	assert.NotContains(t, idx, "/sort/{by:(asc|desc)}", "a template exact is never indexed")
}

// --- Black-box precedence: the fast path must never change the winner ---

// TestExactFastPathTemplateRegisteredFirstWins: a template registered BEFORE a
// literal exact at a path it matches must still win — the literal is shadowed
// out of the index, so the scan (registration order) decides.
func TestExactFastPathTemplateRegisteredFirstWins(t *testing.T) {
	t.Parallel()
	m := New()
	m.Handle("/{x}", ok("template")).Methods("GET") // registered first
	m.Handle("/specific", ok("exact")).Methods("GET")
	assert.Equal(t, "template", do(t, m.Handler(), http.MethodGet, "/specific").Body.String(),
		"the earlier template must win even though a literal exact for the path exists")
}

// TestExactFastPathExactRegisteredFirstWins: the mirror — a literal exact
// registered BEFORE a template that also matches it still wins. It is shadowed
// out of the index (a template matches its path), so the scan decides, and the
// scan hits the exact first.
func TestExactFastPathExactRegisteredFirstWins(t *testing.T) {
	t.Parallel()
	m := New()
	m.Handle("/specific", ok("exact")).Methods("GET") // registered first
	m.Handle("/{x}", ok("template")).Methods("GET")
	assert.Equal(t, "exact", do(t, m.Handler(), http.MethodGet, "/specific").Body.String())
	assert.Equal(t, "template", do(t, m.Handler(), http.MethodGet, "/other").Body.String(),
		"the template still serves paths with no literal exact")
}

// TestExactFastPathPrefixRegisteredFirstWins: a prefix registered before a
// literal exact under it still wins.
func TestExactFastPathPrefixRegisteredFirstWins(t *testing.T) {
	t.Parallel()
	m := New()
	m.HandlePrefix("/api/", ok("prefix")).Methods("GET") // registered first
	m.Handle("/api/v1", ok("exact")).Methods("GET")
	assert.Equal(t, "prefix", do(t, m.Handler(), http.MethodGet, "/api/v1").Body.String(),
		"the earlier prefix must win over a shadowed literal exact")
}

// TestExactFastPath404And405: within an indexed path, method/host resolution and
// the 405-vs-404 distinction match the scan.
func TestExactFastPath404And405(t *testing.T) {
	t.Parallel()
	m := New()
	m.Handle("/x", ok("")).Methods("GET")
	h := m.Handler()
	assert.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "/x").Code)
	assert.Equal(t, http.StatusMethodNotAllowed, do(t, h, http.MethodPost, "/x").Code,
		"known indexed path + wrong method → 405")
	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/y").Code,
		"unindexed path → 404")
}

// TestExactFastPathHostWithinBucket: host-qualified literal exacts at one path
// resolve within the bucket exactly as the scan would.
func TestExactFastPathHostWithinBucket(t *testing.T) {
	t.Parallel()
	m := New()
	m.Handle("/h", ok("a")).Methods("GET").Host("a.com")
	m.Handle("/h", ok("none")).Methods("GET") // host-less fallback, registered second
	h := m.Handler()

	reqA := httptest.NewRequest(http.MethodGet, "/h", nil)
	reqA.Host = "a.com"
	rrA := httptest.NewRecorder()
	h.ServeHTTP(rrA, reqA)
	assert.Equal(t, "a", rrA.Body.String(), "hosted route wins for its host (registered first)")

	reqB := httptest.NewRequest(http.MethodGet, "/h", nil)
	reqB.Host = "b.com"
	rrB := httptest.NewRecorder()
	h.ServeHTTP(rrB, reqB)
	assert.Equal(t, "none", rrB.Body.String(), "host-less route serves other hosts")
}

// TestExactFastPathEncodedPath: under WithEncodedPath the index is keyed and
// looked up on the escaped path, consistent with the scan.
func TestExactFastPathEncodedPath(t *testing.T) {
	t.Parallel()
	m := New(WithEncodedPath())
	m.Handle("/fn/a%20b", ok("hit")).Methods("GET")
	req := httptest.NewRequest(http.MethodGet, "/fn/a%20b", nil)
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, req)
	assert.Equal(t, "hit", rr.Body.String(), "encoded literal path matches via the index")
}

// --- Benchmarks: the O(routes)→O(1) win and the build cost ---

// exactMux registers n literal exact routes (the internal-listener shape:
// /fission-function/ns/fn-i plus its prefix subtree), the case the index
// targets.
func exactMux(n int) *Mux {
	m := New()
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("/fission-function/ns/fn-%d", i)
		m.Handle(p, ok("")).Methods("POST")
		m.HandlePrefix(p+"/", ok("")).Methods("POST")
	}
	return m
}

// BenchmarkExactIndexMatchAtScale: matching the LAST exact route at n routes —
// O(n) under the pure scan, O(1) with the index.
func BenchmarkExactIndexMatchAtScale(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("routes=%d", n), func(b *testing.B) {
			h := exactMux(n).Handler()
			benchServe(b, h, http.MethodPost, fmt.Sprintf("/fission-function/ns/fn-%d", n-1))
		})
	}
}

// BenchmarkExactIndexBuild: the per-rebuild cost of building the index at scale
// (paid on shape changes only, off the serving path).
func BenchmarkExactIndexBuild(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("routes=%d", n), func(b *testing.B) {
			routes := exactMux(n).compile()
			b.ReportAllocs()
			for b.Loop() {
				_ = buildExactIndex(routes)
			}
		})
	}
}

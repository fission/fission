// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpmux

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

// scanOnly builds a dispatcher over m's routes with the fast-path index
// DISABLED, so it always linear-scans — the oracle the indexed dispatcher must
// match for every request.
func scanOnly(m *Mux) http.Handler {
	return &dispatcher{
		routes:           m.compile(),
		encodedPath:      m.encodedPath,
		notFound:         http.HandlerFunc(http.NotFound),
		methodNotAllowed: http.HandlerFunc(methodNotAllowedHandler),
	}
}

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

// TestExactFastPathPrecedence: first-match precedence holds with the index — a
// template or prefix registered BEFORE a literal exact at a path it matches
// still wins, because the literal is shadowed out of the index and the
// registration-order scan decides.
func TestExactFastPathPrecedence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		register func(*Mux)
		reqPath  string
		want     string
	}{
		{"template before exact", func(m *Mux) {
			m.Handle("/{x}", ok("template")).Methods("GET")
			m.Handle("/specific", ok("exact")).Methods("GET")
		}, "/specific", "template"},
		{"exact before template", func(m *Mux) {
			m.Handle("/specific", ok("exact")).Methods("GET")
			m.Handle("/{x}", ok("template")).Methods("GET")
		}, "/specific", "exact"},
		{"template still serves non-literal paths", func(m *Mux) {
			m.Handle("/specific", ok("exact")).Methods("GET")
			m.Handle("/{x}", ok("template")).Methods("GET")
		}, "/other", "template"},
		{"prefix before exact", func(m *Mux) {
			m.HandlePrefix("/api/", ok("prefix")).Methods("GET")
			m.Handle("/api/v1", ok("exact")).Methods("GET")
		}, "/api/v1", "prefix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := New()
			tc.register(m)
			assert.Equal(t, tc.want, do(t, m.Handler(), http.MethodGet, tc.reqPath).Body.String())
		})
	}
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

// TestExactFastPathMatchesScanDifferential is the strongest fast-path guard: for
// a corpus of route sets crossed with requests, the indexed dispatcher and a
// forced linear scan (scanOnly) must return identical status and body. Any
// future shadowing regression — a {var:regexp} no longer treated as a shadow, a
// prefix-length probe off by one, a host leak — diverges here even if nobody
// wrote a targeted case for it.
func TestExactFastPathMatchesScanDifferential(t *testing.T) {
	t.Parallel()
	routeSets := []struct {
		name     string
		register func(*Mux)
		encoded  bool
	}{
		{"regexp template + shadowed and unshadowed exacts", func(m *Mux) {
			m.Handle(`/bank/{html:[a-zA-Z0-9./]+}`, ok("tmpl")).Methods("GET")
			m.Handle("/bank/index.html", ok("exact")).Methods("GET") // shadowed by the template
			m.Handle("/elsewhere", ok("plain")).Methods("GET")       // unshadowed
		}, false},
		{"bare {var} before an exact it matches", func(m *Mux) {
			m.Handle("/a/{id}", ok("var")).Methods("GET")
			m.Handle("/a/me", ok("exact")).Methods("GET")
		}, false},
		{"prefix not ending in slash", func(m *Mux) {
			m.HandlePrefix("/ap", ok("pfx")).Methods("GET") // shadows /api and /apex
			m.Handle("/api", ok("api")).Methods("GET")
			m.Handle("/apex", ok("apex")).Methods("GET")
			m.Handle("/other", ok("other")).Methods("GET") // unshadowed
		}, false},
		{"prefix longer than a sibling exact does not shadow it", func(m *Mux) {
			m.HandlePrefix("/apix/", ok("pfx")).Methods("GET")
			m.Handle("/api", ok("api")).Methods("GET")
		}, false},
		{"host-qualified bucket, no host-less fallback", func(m *Mux) {
			m.Handle("/h", ok("a")).Methods("GET").Host("a.com")
			m.Handle("/h", ok("b")).Methods("POST").Host("b.com")
		}, false},
		{"dual registration boundary", func(m *Mux) {
			m.Handle("/api", ok("exact")).Methods("GET")
			m.HandlePrefix("/api/", ok("pfx")).Methods("GET")
		}, false},
		{"encoded path", func(m *Mux) {
			m.Handle("/fn/a%20b", ok("enc")).Methods("GET")
			m.HandlePrefix("/fn/a%20b/", ok("sub")).Methods("GET")
		}, true},
	}
	methods := []string{http.MethodGet, http.MethodPost, http.MethodOptions}
	paths := []string{
		"/bank/index.html", "/bank/x/y.css", "/bank/oops!", "/elsewhere",
		"/a/me", "/a/123", "/a/1/2",
		"/ap", "/api", "/apex", "/apix", "/apix/sub", "/other",
		"/h", "/api/", "/api/v1",
		"/fn/a%20b", "/fn/a%20b/sub", "/nope",
	}
	hosts := []string{"", "a.com", "b.com"}
	for _, rs := range routeSets {
		t.Run(rs.name, func(t *testing.T) {
			t.Parallel()
			var opts []Option
			if rs.encoded {
				opts = append(opts, WithEncodedPath())
			}
			indexed := New(opts...)
			rs.register(indexed)
			scan := New(opts...)
			rs.register(scan)
			indexedH, scanH := indexed.Handler(), scanOnly(scan)
			for _, method := range methods {
				for _, path := range paths {
					for _, host := range hosts {
						req := httptest.NewRequest(method, path, nil)
						if host != "" {
							req.Host = host
						}
						ir, sr := httptest.NewRecorder(), httptest.NewRecorder()
						indexedH.ServeHTTP(ir, req)
						scanH.ServeHTTP(sr, req)
						assert.Equalf(t, sr.Code, ir.Code, "status: %s %s host=%q", method, path, host)
						assert.Equalf(t, sr.Body.String(), ir.Body.String(), "body: %s %s host=%q", method, path, host)
					}
				}
			}
		})
	}
}

// TestExactIndexWellFormed ties the index back to the authoritative route slice:
// every bucketed route is a literal exact present in routes whose pattern equals
// its key, and a bucket preserves registration order. Guards the index from
// drifting from the slice it projects.
func TestExactIndexWellFormed(t *testing.T) {
	t.Parallel()
	m := New()
	m.Handle("/a", ok("")).Methods("GET")
	m.Handle("/a", ok("")).Methods("POST") // multi-exact bucket
	m.Handle("/b/{id}", ok(""))            // template — never a key
	m.HandlePrefix("/c/", ok(""))          // prefix — never a key
	m.Handle("/c/x", ok(""))               // shadowed by /c/ — excluded
	routes := m.compile()
	idx := buildExactIndex(routes)

	for path, bucket := range idx {
		lastPos := -1
		for _, cr := range bucket {
			assert.Nil(t, cr.re, "bucket route must be a literal (re==nil)")
			assert.Equal(t, Exact, cr.route.kind, "bucket route must be Exact kind")
			assert.Equal(t, path, cr.route.pattern, "bucket route pattern must equal its key")
			pos := slices.Index(routes, cr)
			assert.Greater(t, pos, lastPos, "bucket preserves registration order")
			lastPos = pos
		}
	}
	assert.Len(t, idx["/a"], 2, "both exacts at /a share the bucket")
	assert.NotContains(t, idx, "/c/x", "shadowed exact is excluded")
	assert.NotContains(t, idx, "/b/{id}", "template is never a key")
}

// TestExactIndexEngagedAtScale guards that the optimization actually engages: a
// refactor that disabled indexing would pass every correctness test while
// silently reverting to an O(routes) scan, but would fail here.
func TestExactIndexEngagedAtScale(t *testing.T) {
	t.Parallel()
	idx := buildExactIndex(exactMux(1000).compile())
	assert.Len(t, idx, 1000, "every unshadowed exact must be indexed")
}

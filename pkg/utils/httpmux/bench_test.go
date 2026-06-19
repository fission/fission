// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpmux

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Benchmarks for the matcher hot path. The executor/storagesvc muxes are small
// (a handful of exact routes); the router (Phase 3) scans templates in
// precedence order. These guard that neither path regresses and give a baseline
// to compare the Phase 3 router cutover against gorilla/mux.

func benchServe(b *testing.B, h http.Handler, method, path string) {
	b.Helper()
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(rr, req)
	}
}

// BenchmarkExactMatch: the executor/storagesvc shape — a few exact, method-gated
// routes, hitting the last one so the scan runs the full length.
func BenchmarkExactMatch(b *testing.B) {
	m := New()
	m.Handle("/v2/getServiceForFunction", ok("")).Methods("POST")
	m.Handle("/v2/tapServices", ok("")).Methods("POST")
	m.Handle("/healthz", ok("")).Methods("GET")
	m.Handle("/readyz", ok("")).Methods("GET")
	benchServe(b, m.Handler(), http.MethodGet, "/readyz")
}

// BenchmarkTemplateMatch: the router shape — a bare {var} segment, the common
// /fission-function/{name} / /accounts/{id} case.
func BenchmarkTemplateMatch(b *testing.B) {
	m := New()
	m.Handle("/accounts/{id}", ok("")).Methods("GET")
	benchServe(b, m.Handler(), http.MethodGet, "/accounts/abc123")
}

// BenchmarkTemplateMatchRegex: a {var:regex} that spans path segments — the
// heaviest matcher case (real Fission trigger from routeshape_test.go).
func BenchmarkTemplateMatchRegex(b *testing.B) {
	m := New()
	m.Handle(`/bank/{html:[a-zA-Z0-9\./]+}`, ok("")).Methods("GET")
	benchServe(b, m.Handler(), http.MethodGet, "/bank/foo/bar/baz.html")
}

// BenchmarkNoMatch404: the scan-everything-then-404 path (probes / scanners),
// confirming an unmatched request stays cheap and bounded.
func BenchmarkNoMatch404(b *testing.B) {
	m := New()
	m.Handle("/accounts/{id}", ok("")).Methods("GET")
	m.Handle("/healthz", ok("")).Methods("GET")
	benchServe(b, m.Handler(), http.MethodGet, "/nonexistent/path")
}

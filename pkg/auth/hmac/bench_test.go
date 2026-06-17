// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// BenchmarkDeriveServiceKey and BenchmarkDeriveServiceKeyNS quantify the cost of
// the master-scoped vs namespace-scoped HKDF-SHA256 derivation. They should be
// within noise of each other: namespace scoping only appends ":<ns>" to the HKDF
// info string, so the work is one HKDF-SHA256 either way. This is the unit the
// multi-namespace tenancy work added to the signing path.
func BenchmarkDeriveServiceKey(b *testing.B) {
	master := []byte(testMaster)
	for b.Loop() {
		_ = DeriveServiceKey(master, ServiceFetcher)
	}
}

func BenchmarkDeriveServiceKeyNS(b *testing.B) {
	master := []byte(testMaster)
	for b.Loop() {
		_ = DeriveServiceKeyNS(master, ServiceFetcher, "team-a")
	}
}

// BenchmarkVerifyNamespaceRequest measures the full per-request verification on
// the storagesvc dual-accept path (ServiceVerifierNamespaceFromHeader) for the
// common case: a tenant fetcher that sends its namespace header and signs with
// its namespace key. The namespace key is candidate index 0, so it matches before
// the two master-derived back-compat keys are needed — but the current
// implementation derives all candidates eagerly, so this also bounds the
// worst case. It is dominated by the HMAC verify + the candidate-key HKDFs, all
// single-digit microseconds and trivial next to the archive-body I/O this gates.
func BenchmarkVerifyNamespaceRequest(b *testing.B) {
	master := []byte(testMaster)
	now := func() time.Time { return time.Unix(1715000123, 0) }
	handler := ServiceVerifierNamespaceFromHeader(master, nil, ServiceStoragesvc, VerifierOpts{SkewSec: 60, Now: now})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	// Sign once as a tenant whose own namespace key is K(storagesvc, team-a). The
	// verifier is replay-stateless within the skew window, so the same signed
	// request can be served repeatedly.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/archive?id=x", nil)
	if _, err := NewSigner(DeriveServiceKeyNS(master, ServiceStoragesvc, "team-a"), &recordingTransport{rr: rr}, now).RoundTrip(req); err != nil {
		b.Fatalf("sign: %v", err)
	}
	signed := signedRequestFromRecorder(b, rr)
	signed.Header.Set(HeaderNamespace, "team-a")

	// Sanity-check the success path before timing, so a broken setup fails loudly
	// instead of benchmarking a 401.
	probe := httptest.NewRecorder()
	handler.ServeHTTP(probe, signed)
	if probe.Code != http.StatusOK {
		b.Fatalf("precondition: verify returned %d, want 200", probe.Code)
	}

	for b.Loop() {
		handler.ServeHTTP(httptest.NewRecorder(), signed)
	}
}

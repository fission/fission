package httpsecurity

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// echoHandler writes "ok" with a 200; tests use it as the inner handler
// when they only care about what the middleware does.
var echoHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("ok"))
})

// staleCORSHandler sets Access-Control-Allow-Origin: * and several other
// CORS headers before writing the response, simulating a future
// regression in an inner handler. The middleware under test must strip
// these.
var staleCORSHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Expose-Headers", "X-Test")
	_, _ = w.Write([]byte("ok"))
})

func TestSecurityHeaders_AddsNosniffAndVary(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	SecurityHeaders(echoHandler).ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options: got %q, want nosniff", got)
	}
	if !varyContains(rec.Header(), "Origin") {
		t.Errorf("Vary header missing Origin: %v", rec.Header().Values("Vary"))
	}
}

func TestSecurityHeaders_PreservesExistingVary(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept-Encoding")
		_, _ = w.Write([]byte("ok"))
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	SecurityHeaders(inner).ServeHTTP(rec, req)

	if !varyContains(rec.Header(), "Origin") {
		t.Errorf("Vary missing Origin: %v", rec.Header().Values("Vary"))
	}
	if !varyContains(rec.Header(), "Accept-Encoding") {
		t.Errorf("Vary missing pre-existing Accept-Encoding: %v", rec.Header().Values("Vary"))
	}
}

func TestSecurityHeaders_DoesNotDuplicateVary(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Origin, Accept-Encoding")
		_, _ = w.Write([]byte("ok"))
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	SecurityHeaders(inner).ServeHTTP(rec, req)

	count := 0
	for _, entry := range rec.Header().Values("Vary") {
		for part := range strings.SplitSeq(entry, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "Origin") {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("Vary Origin count: got %d, want 1 (header values: %v)", count, rec.Header().Values("Vary"))
	}
}

func TestSecurityHeaders_DoesNotOverrideNosniff(t *testing.T) {
	// Defense-in-depth: if some inner handler has an unusual reason to
	// emit a different X-Content-Type-Options, we don't clobber it.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff,noopen")
		_, _ = w.Write([]byte("ok"))
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	SecurityHeaders(inner).ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff,noopen" {
		t.Errorf("X-Content-Type-Options: got %q, want nosniff,noopen (preserved)", got)
	}
}

func TestDenyAllCORS_RejectsCrossOriginPreflight(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("Access-Control-Request-Method", "POST")

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	DenyAllCORS(inner).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", rec.Code)
	}
	if called {
		t.Error("inner handler should not be invoked on cross-origin preflight")
	}
}

func TestDenyAllCORS_PassesPlainOptionsThrough(t *testing.T) {
	// OPTIONS without Origin or Access-Control-Request-Method is not a
	// CORS preflight; some HTTP clients use it for discovery.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	DenyAllCORS(inner).ServeHTTP(rec, req)

	if !called {
		t.Error("inner handler should be invoked for non-CORS OPTIONS")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
}

func TestDenyAllCORS_StripsStaleAccessControlHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://attacker.example")

	DenyAllCORS(staleCORSHandler).ServeHTTP(rec, req)

	for _, h := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Credentials",
		"Access-Control-Expose-Headers",
	} {
		if got := rec.Header().Get(h); got != "" {
			t.Errorf("header %s should have been stripped, got %q", h, got)
		}
	}
	if body := rec.Body.String(); body != "ok" {
		t.Errorf("body: got %q, want %q", body, "ok")
	}
}

func TestDenyAllCORS_StripsHeadersOnImplicitWriteHeader(t *testing.T) {
	// Handlers that don't call WriteHeader still go through Write,
	// which our wrapper also intercepts.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = w.Write([]byte("payload"))
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	DenyAllCORS(inner).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin should have been stripped, got %q", got)
	}
	if body := rec.Body.String(); body != "payload" {
		t.Errorf("body: got %q, want payload", body)
	}
}

func TestCORSAllowlist_EchoesExactMatchOrigin(t *testing.T) {
	mw := CORSAllowlist(AllowlistConfig{
		AllowOrigins: []string{"https://app.example.com", "https://other.example"},
		AllowMethods: []string{"GET", "POST"},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	mw(echoHandler).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Allow-Origin: got %q, want https://app.example.com", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST" {
		t.Errorf("Allow-Methods: got %q, want %q", got, "GET, POST")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status: got %d, want 204", rec.Code)
	}
}

func TestCORSAllowlist_RejectsMismatchedOriginPreflight(t *testing.T) {
	mw := CORSAllowlist(AllowlistConfig{AllowOrigins: []string{"https://app.example.com"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	mw(echoHandler).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("mismatched origin preflight status: got %d, want 403", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("mismatched origin should not be echoed, got %q", got)
	}
}

func TestCORSAllowlist_ActualRequestMismatch(t *testing.T) {
	// Non-preflight request with mismatched Origin: handler runs (so
	// internal logic still serves the response) but no Allow-Origin is
	// echoed, so the browser SOP blocks the read.
	mw := CORSAllowlist(AllowlistConfig{AllowOrigins: []string{"https://app.example.com"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://attacker.example")
	mw(echoHandler).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin for mismatched origin: got %q, want empty", got)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body: got %q, want ok (handler still runs)", rec.Body.String())
	}
}

func TestCORSAllowlist_WildcardOrigin(t *testing.T) {
	mw := CORSAllowlist(AllowlistConfig{AllowOrigins: []string{"*"}, AllowMethods: []string{"GET"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://anything.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	mw(echoHandler).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Wildcard Allow-Origin: got %q, want *", got)
	}
}

func TestCORSAllowlist_MaxAgeAndCredentials(t *testing.T) {
	mw := CORSAllowlist(AllowlistConfig{
		AllowOrigins:     []string{"https://app.example.com"},
		AllowMethods:     []string{"GET"},
		AllowCredentials: true,
		MaxAge:           5 * time.Minute,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	mw(echoHandler).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials: got %q, want true", got)
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "300" {
		t.Errorf("Max-Age: got %q, want 300", got)
	}
}

func TestCORSAllowlist_EmptyOriginsBehavesLikeDeny(t *testing.T) {
	// The zero AllowlistConfig must not accidentally permit anything;
	// it must reject preflights like DenyAllCORS.
	mw := CORSAllowlist(AllowlistConfig{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	mw(echoHandler).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("empty allowlist preflight: got %d, want 403", rec.Code)
	}
}

func TestCORSAllowlist_PanicsOnWildcardWithCredentials(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for AllowOrigins=[\"*\"] + AllowCredentials=true")
		}
	}()
	CORSAllowlist(AllowlistConfig{
		AllowOrigins:     []string{"*"},
		AllowCredentials: true,
	})
}

func TestCORSAllowlist_ExposeHeadersOnActualRequest(t *testing.T) {
	mw := CORSAllowlist(AllowlistConfig{
		AllowOrigins:  []string{"https://app.example.com"},
		ExposeHeaders: []string{"X-Request-Id", "X-Fission-Build"},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://app.example.com")
	mw(echoHandler).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "X-Request-Id, X-Fission-Build" {
		t.Errorf("Expose-Headers: got %q, want %q", got, "X-Request-Id, X-Fission-Build")
	}
}

// Compose all three middlewares as they would be wired in production
// (SecurityHeaders outermost, DenyAllCORS inside, inner handler at the
// bottom) and check the headers an end-to-end request sees.
func TestComposed_SecurityHeadersOverDenyAllCORS(t *testing.T) {
	chain := SecurityHeaders(DenyAllCORS(staleCORSHandler))
	srv := httptest.NewServer(chain)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options: got %q, want nosniff", got)
	}
	if !varyContains(resp.Header, "Origin") {
		t.Errorf("Vary missing Origin: %v", resp.Header.Values("Vary"))
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("stale Allow-Origin should be stripped, got %q", got)
	}
}

func varyContains(h http.Header, value string) bool {
	for _, entry := range h.Values("Vary") {
		for part := range strings.SplitSeq(entry, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return true
			}
		}
	}
	return false
}

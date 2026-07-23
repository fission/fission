// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package asyncinvoke

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

func TestHTTPDelivererDelivers(t *testing.T) {
	t.Parallel()
	var (
		gotMethod, gotPath, gotQuery, gotBody string
		gotHeaders                            http.Header
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	d := NewHTTPDeliverer(srv.URL, nil, nil, logr.Discard())
	env := Envelope{
		Namespace: "ns", Function: "fn", Method: http.MethodPut, Query: "a=1",
		Headers: map[string]string{"Content-Type": "application/json", "X-Request-Id": "r1"},
		Body:    []byte("payload"), Depth: 2,
	}
	res := d.Deliver(context.Background(), env, "inv-123", 4)
	require.NoError(t, res.Err)
	assert.Equal(t, http.StatusAccepted, res.StatusCode)

	assert.Equal(t, http.MethodPut, gotMethod)
	assert.Equal(t, "/fission-function/ns/fn", gotPath, "delivers at UrlForFunction target")
	assert.Equal(t, "a=1", gotQuery, "query preserved")
	assert.Equal(t, "payload", gotBody)
	assert.Equal(t, "application/json", gotHeaders.Get("Content-Type"), "envelope headers replayed")
	assert.Equal(t, "r1", gotHeaders.Get("X-Request-Id"))
	assert.Equal(t, "inv-123", gotHeaders.Get(HeaderInvocationID), "invocation id replayed")
	assert.Equal(t, "4", gotHeaders.Get(HeaderInvocationAttempt), "attempt replayed")
	assert.Equal(t, "2", gotHeaders.Get(HeaderInvocationDepth), "depth replayed")
}

func TestHTTPDelivererCapturesAndTruncatesBody(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("a", MaxPayloadBytes+100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, big)
	}))
	defer srv.Close()

	d := NewHTTPDeliverer(srv.URL, nil, nil, logr.Discard())
	// A destination is declared, so the response body is captured for the result
	// envelope — and flagged truncated when it exceeds the cap.
	env := Envelope{Namespace: "ns", Function: "fn", OnSuccess: &Destination{FunctionName: "next"}}
	res := d.Deliver(context.Background(), env, "id", 1)
	require.NoError(t, res.Err)
	assert.Len(t, res.Body, MaxPayloadBytes, "response body captured and truncated at MaxPayloadBytes")
	assert.True(t, res.BodyTruncated, "over-cap body is flagged truncated")
}

// TestHTTPDelivererSkipsBodyWithoutDestination proves the no-destination fast-path:
// with neither OnSuccess nor OnFailure set the response body is drained, not
// captured, so a destination-less invocation never allocates up to 64KiB.
func TestHTTPDelivererSkipsBodyWithoutDestination(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "response-body")
	}))
	defer srv.Close()

	d := NewHTTPDeliverer(srv.URL, nil, nil, logr.Discard())
	res := d.Deliver(context.Background(), Envelope{Namespace: "ns", Function: "fn"}, "id", 1)
	require.NoError(t, res.Err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Nil(t, res.Body, "no destination → body not captured")
	assert.False(t, res.BodyTruncated)
}

// TestHTTPDelivererCapturesOnlyFiringDestinationBody proves the capture is
// status-aware: the body is read only for the destination this outcome fires (2xx
// → OnSuccess, non-2xx → OnFailure), so the non-firing destination's presence does
// not trigger a wasted read.
func TestHTTPDelivererCapturesOnlyFiringDestinationBody(t *testing.T) {
	t.Parallel()
	body := func(status int, env Envelope) []byte {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "resp")
		}))
		defer srv.Close()
		env.Namespace, env.Function = "ns", "fn"
		return NewHTTPDeliverer(srv.URL, nil, nil, logr.Discard()).Deliver(context.Background(), env, "id", 1).Body
	}
	onSuccess := Envelope{OnSuccess: &Destination{FunctionName: "s"}}
	onFailure := Envelope{OnFailure: &Destination{FunctionName: "f"}}

	assert.Equal(t, []byte("resp"), body(http.StatusOK, onSuccess), "2xx + OnSuccess → captured")
	assert.Nil(t, body(http.StatusOK, onFailure), "2xx + only OnFailure → not captured (OnFailure won't fire)")
	assert.Equal(t, []byte("resp"), body(http.StatusForbidden, onFailure), "4xx + OnFailure → captured")
	assert.Nil(t, body(http.StatusForbidden, onSuccess), "4xx + only OnSuccess → not captured (OnSuccess won't fire)")
}

func TestHTTPDelivererDefaultNamespaceFold(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := NewHTTPDeliverer(srv.URL, nil, nil, logr.Discard())
	d.Deliver(context.Background(), Envelope{Namespace: "default", Function: "fn"}, "id", 1)
	assert.Equal(t, "/fission-function/fn", gotPath, "default namespace folds (matches the registered route)")
}

func TestHTTPDelivererMethodDefaultsPost(t *testing.T) {
	t.Parallel()
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := NewHTTPDeliverer(srv.URL, nil, nil, logr.Discard())
	d.Deliver(context.Background(), Envelope{Namespace: "ns", Function: "fn"}, "id", 1) // Method empty
	assert.Equal(t, http.MethodPost, gotMethod)
}

func TestHTTPDelivererTransportError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // now unreachable → dial error

	d := NewHTTPDeliverer(url, nil, nil, logr.Discard())
	res := d.Deliver(context.Background(), Envelope{Namespace: "ns", Function: "fn"}, "id", 1)
	assert.Error(t, res.Err, "a transport failure sets Err")
	assert.Zero(t, res.StatusCode)
}

// TestHTTPDelivererHMACAcceptedByVerifier proves the delivery leg's signature is
// byte-compatible with the router's ServiceRouterInternal verifier: a signed
// delivery is accepted and an unsigned one is rejected before the handler.
func TestHTTPDelivererHMACAcceptedByVerifier(t *testing.T) {
	t.Parallel()
	master := []byte("test-master-secret")
	verifier := hmacauth.ServiceVerifier(master, nil, hmacauth.ServiceRouterInternal, hmacauth.VerifierOpts{SkewSec: 60})
	var reached bool
	srv := httptest.NewServer(verifier(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	env := Envelope{Namespace: "ns", Function: "fn", Body: []byte("x")}

	signed := NewHTTPDeliverer(srv.URL, master, nil, logr.Discard())
	res := signed.Deliver(context.Background(), env, "id", 1)
	require.NoError(t, res.Err)
	assert.Equal(t, http.StatusOK, res.StatusCode, "signed delivery accepted")
	assert.True(t, reached)

	reached = false
	unsigned := NewHTTPDeliverer(srv.URL, nil, nil, logr.Discard())
	res = unsigned.Deliver(context.Background(), env, "id", 1)
	require.NoError(t, res.Err) // a rejection is an HTTP status, not a transport error
	assert.NotEqual(t, http.StatusOK, res.StatusCode, "unsigned delivery rejected")
	assert.False(t, reached, "unsigned delivery blocked before the handler")
}

// TestHTTPDelivererVersionPinned_TargetsVersionedRoute proves a
// FunctionVersion-pinned envelope (RFC-0025 Task 5) is delivered at the
// `:<version>` suffixed internal route, not the bare function route.
func TestHTTPDelivererVersionPinned_TargetsVersionedRoute(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewHTTPDeliverer(srv.URL, nil, nil, logr.Discard())
	env := Envelope{Namespace: "ns", Function: "hello", FunctionVersion: "hello-v1"}
	res := d.Deliver(context.Background(), env, "id", 1)
	require.NoError(t, res.Err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "/fission-function/ns/hello:hello-v1", gotPath, "delivers at the versioned route")
}

// TestHTTPDelivererVersionPinned_FallsBackOnNotFound proves the RFC-0025
// Task 5 GC'd-route recovery: a 404 on the versioned route retries
// immediately against the bare function route, within the SAME attempt (the
// caller sees exactly one DeliveryResult, and it reflects the bare route's
// outcome, not the 404).
func TestHTTPDelivererVersionPinned_FallsBackOnNotFound(t *testing.T) {
	t.Parallel()
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		if strings.Contains(r.URL.Path, ":") {
			w.WriteHeader(http.StatusNotFound) // versioned route GC'd
			return
		}
		w.WriteHeader(http.StatusOK) // bare route still serves
	}))
	defer srv.Close()

	d := NewHTTPDeliverer(srv.URL, nil, nil, logr.Discard())
	env := Envelope{Namespace: "ns", Function: "hello", FunctionVersion: "hello-vGONE"}
	res := d.Deliver(context.Background(), env, "id", 1)
	require.NoError(t, res.Err)
	assert.Equal(t, http.StatusOK, res.StatusCode, "the fallback attempt's outcome is what the caller sees")
	require.Len(t, gotPaths, 2, "exactly two HTTP attempts: versioned then bare, within one Deliver call")
	assert.Equal(t, "/fission-function/ns/hello:hello-vGONE", gotPaths[0])
	assert.Equal(t, "/fission-function/ns/hello", gotPaths[1])
}

// TestHTTPDelivererVersionPinned_NoFallbackOnOtherStatus proves the fallback
// is scoped to exactly a 404 on the versioned route -- any other status
// (including a 4xx that isn't 404, or the function's own legitimate 404) is
// relayed as-is, with only ONE attempt.
func TestHTTPDelivererVersionPinned_NoFallbackOnOtherStatus(t *testing.T) {
	t.Parallel()
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := NewHTTPDeliverer(srv.URL, nil, nil, logr.Discard())
	env := Envelope{Namespace: "ns", Function: "hello", FunctionVersion: "hello-v1"}
	res := d.Deliver(context.Background(), env, "id", 1)
	require.NoError(t, res.Err)
	assert.Equal(t, http.StatusInternalServerError, res.StatusCode)
	assert.Equal(t, 1, attempts, "a non-404 status is relayed without a fallback attempt")
}

// TestHTTPDelivererUnversioned_NoFallbackMachinery proves an unversioned
// envelope (FunctionVersion == "", the pre-Task-5 shape) is unaffected: one
// attempt at the bare route, even on a 404 (nothing to fall back to).
func TestHTTPDelivererUnversioned_NoFallbackMachinery(t *testing.T) {
	t.Parallel()
	var attempts int
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	d := NewHTTPDeliverer(srv.URL, nil, nil, logr.Discard())
	res := d.Deliver(context.Background(), Envelope{Namespace: "ns", Function: "hello"}, "id", 1)
	require.NoError(t, res.Err)
	assert.Equal(t, http.StatusNotFound, res.StatusCode)
	assert.Equal(t, 1, attempts)
	assert.Equal(t, "/fission-function/ns/hello", gotPath)
}

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

	d := NewHTTPDeliverer(srv.URL, nil, nil)
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

	d := NewHTTPDeliverer(srv.URL, nil, nil)
	res := d.Deliver(context.Background(), Envelope{Namespace: "ns", Function: "fn"}, "id", 1)
	require.NoError(t, res.Err)
	assert.Len(t, res.Body, MaxPayloadBytes, "response body captured and truncated at MaxPayloadBytes")
}

func TestHTTPDelivererDefaultNamespaceFold(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := NewHTTPDeliverer(srv.URL, nil, nil)
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
	d := NewHTTPDeliverer(srv.URL, nil, nil)
	d.Deliver(context.Background(), Envelope{Namespace: "ns", Function: "fn"}, "id", 1) // Method empty
	assert.Equal(t, http.MethodPost, gotMethod)
}

func TestHTTPDelivererTransportError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // now unreachable → dial error

	d := NewHTTPDeliverer(url, nil, nil)
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

	signed := NewHTTPDeliverer(srv.URL, master, nil)
	res := signed.Deliver(context.Background(), env, "id", 1)
	require.NoError(t, res.Err)
	assert.Equal(t, http.StatusOK, res.StatusCode, "signed delivery accepted")
	assert.True(t, reached)

	reached = false
	unsigned := NewHTTPDeliverer(srv.URL, nil, nil)
	res = unsigned.Deliver(context.Background(), env, "id", 1)
	require.NoError(t, res.Err) // a rejection is an HTTP status, not a transport error
	assert.NotEqual(t, http.StatusOK, res.StatusCode, "unsigned delivery rejected")
	assert.False(t, reached, "unsigned delivery blocked before the handler")
}

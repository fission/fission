// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/router/asyncinvoke"
)

// fakeTestInput overrides only the accessors testQueryValues reads.
type fakeTestInput struct {
	fakeDLQInput
	ss map[string][]string
}

func (f fakeTestInput) StringSlice(k string) []string { return f.ss[k] }
func (f fakeTestInput) Context() context.Context      { return context.Background() }

func TestTestQueryValues(t *testing.T) {
	t.Parallel()
	in := fakeTestInput{ss: map[string][]string{flagkey.FnTestQuery: {"a=1", "b=2", "novalue", "=skip"}}}
	q := testQueryValues(in)
	assert.Equal(t, "1", q.Get("a"))
	assert.Equal(t, "2", q.Get("b"))
	assert.Equal(t, "", q.Get("novalue"), "a key with no = still parses to an empty value")
	assert.True(t, q.Has("novalue"))
	assert.False(t, q.Has(""), "an empty key is dropped")
}

// TestCombinedHTTPRequestForwardsRequest verifies the shared request-building
// helper forwards method/body/headers verbatim and, by default, attaches
// neither an invoke-mode header nor a bearer token nor an HMAC signature.
func TestCombinedHTTPRequestForwardsRequest(t *testing.T) {
	var got struct {
		method, body, customHdr, invokeMode, authHdr, sigHdr string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method = r.Method
		got.body = string(b)
		got.customHdr = r.Header.Get("X-Custom")
		got.invokeMode = r.Header.Get(asyncinvoke.HeaderInvokeMode)
		got.authHdr = r.Header.Get("Authorization")
		got.sigHdr = r.Header.Get(hmacauth.HeaderSignature)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := combinedHTTPRequest(context.Background(), invokeOptions{
		Method:  http.MethodPost,
		URL:     srv.URL + "/fission-function/ns/fn",
		Body:    "req-body",
		Headers: []string{"X-Custom: v"},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "req-body", got.body)
	assert.Equal(t, "v", got.customHdr, "user headers forwarded")
	assert.Empty(t, got.invokeMode, "no invoke-mode header unless requested")
	assert.Empty(t, got.authHdr, "no bearer token unless requested")
	assert.Empty(t, got.sigHdr, "no HMAC signature unless requested")
}

// TestCombinedHTTPRequestInvokeModeSetLast guards the "set LAST" rule: even if
// the user passes -H "X-Fission-Invoke-Mode: sync", opts.InvokeModeHeader must
// win so --async stays authoritative.
func TestCombinedHTTPRequestInvokeModeSetLast(t *testing.T) {
	var gotInvokeMode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotInvokeMode = r.Header.Get(asyncinvoke.HeaderInvokeMode)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	resp, err := combinedHTTPRequest(context.Background(), invokeOptions{
		Method:           http.MethodPost,
		URL:              srv.URL,
		Headers:          []string{asyncinvoke.HeaderInvokeMode + ": sync"},
		InvokeModeHeader: asyncinvoke.InvokeModeAsync,
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, asyncinvoke.InvokeModeAsync, gotInvokeMode,
		"async header must overwrite a user-supplied sync header")
}

// TestCombinedHTTPRequestHMACSigning covers both sides of the pass-through
// contract: signed when SignWithHMAC is set AND a secret is configured,
// silently unsigned when SignWithHMAC is set but no secret is present
// (matches the chart's internalAuth.enabled=false default). The
// "requested but no secret" case guards against a regression where this path
// briefly became a hard error instead of a pass-through.
func TestCombinedHTTPRequestHMACSigning(t *testing.T) {
	cases := []struct {
		name         string
		signWithHMAC bool
		secret       string
		wantSigned   bool
	}{
		{"signed when requested and secret set", true, "test-secret", true},
		{"pass-through when requested but no secret", true, "", false},
		{"never signed when not requested", false, "test-secret", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sig string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sig = r.Header.Get(hmacauth.HeaderSignature)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			t.Setenv("FISSION_INTERNAL_AUTH_SECRET", tc.secret)

			resp, err := combinedHTTPRequest(context.Background(), invokeOptions{
				Method: http.MethodGet,
				// The signing transport only signs paths under this prefix
				// (pkg/auth/hmac/transport.go) — matches how do() builds URLs.
				URL:          srv.URL + "/fission-function/ns/fn",
				SignWithHMAC: tc.signWithHMAC,
			})
			require.NoError(t, err)
			defer resp.Body.Close()

			if tc.wantSigned {
				assert.NotEmpty(t, sig)
			} else {
				assert.Empty(t, sig)
			}
		})
	}
}

// TestCombinedHTTPRequestAttachesBearerToken covers both sides: attached when
// requested and FISSION_AUTH_TOKEN is set, never attached when not requested
// — fn test intentionally omits this since the internal listener
// authenticates via HMAC, not the JWT gateway.
func TestCombinedHTTPRequestAttachesBearerToken(t *testing.T) {
	cases := []struct {
		name       string
		attach     bool
		wantHeader string
	}{
		{"attached when requested", true, "Bearer test-token"},
		{"omitted when not requested", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var authHdr string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authHdr = r.Header.Get("Authorization")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			t.Setenv(util.FISSION_AUTH_TOKEN, "test-token")

			resp, err := combinedHTTPRequest(context.Background(), invokeOptions{
				Method:            http.MethodGet,
				URL:               srv.URL,
				AttachBearerToken: tc.attach,
			})
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tc.wantHeader, authHdr)
		})
	}
}

// TestCombinedHTTPRequestMalformedHeaderErrors guards the header-parsing
// contract chosen when the sync/async request builders were merged: a header
// with no ":" is a hard error (matching the old DoHTTPRequest behavior used
// by `fn run`), not a silent drop.
func TestCombinedHTTPRequestMalformedHeaderErrors(t *testing.T) {
	_, err := combinedHTTPRequest(context.Background(), invokeOptions{
		Method:  http.MethodGet,
		URL:     "http://example.invalid",
		Headers: []string{"NoColonHere"},
	})
	require.Error(t, err)
}

// TestCombinedHTTPRequestInvalidMethod guards method validation, which
// combinedHTTPRequest must do itself since invokeLocal (fn run) does not
// validate before calling in.
func TestCombinedHTTPRequestInvalidMethod(t *testing.T) {
	_, err := combinedHTTPRequest(context.Background(), invokeOptions{
		Method: "NOT-A-METHOD",
		URL:    "http://example.invalid",
	})
	require.Error(t, err)
}

// fakeResponse builds an *http.Response for handleAsyncResponse tests without
// a real round trip — it only ever inspects status/header/body.
func fakeResponse(status int, header http.Header, body string) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestHandleAsyncResponseAccepted(t *testing.T) {
	t.Run("invocation id from header", func(t *testing.T) {
		hdr := http.Header{}
		hdr.Set(asyncinvoke.HeaderInvocationID, "asyncinv/9")
		err := handleAsyncResponse(fakeResponse(http.StatusAccepted, hdr, ""))
		require.NoError(t, err)
	})
	t.Run("invocation id falls back to JSON body", func(t *testing.T) {
		err := handleAsyncResponse(fakeResponse(http.StatusAccepted, nil, `{"invocationId":"asyncinv/5"}`))
		require.NoError(t, err)
	})
	t.Run("no id anywhere still succeeds with a warning", func(t *testing.T) {
		err := handleAsyncResponse(fakeResponse(http.StatusAccepted, nil, ""))
		require.NoError(t, err)
	})
}

func TestHandleAsyncResponseErrorStatuses(t *testing.T) {
	cases := map[string]struct {
		status int
		errSub string
	}{
		"disabled 501":     {http.StatusNotImplemented, "not enabled"},
		"unauthorized 401": {http.StatusUnauthorized, "FISSION_INTERNAL_AUTH_SECRET"},
		"forbidden 403":    {http.StatusForbidden, "FISSION_INTERNAL_AUTH_SECRET"},
		"server 500":       {http.StatusInternalServerError, "500"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := handleAsyncResponse(fakeResponse(tc.status, nil, ""))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errSub)
		})
	}
}

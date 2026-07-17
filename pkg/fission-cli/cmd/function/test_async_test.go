// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/router/asyncinvoke"
)

// fakeTestInput overrides only the accessors invokeAsync/testQueryValues read.
type fakeTestInput struct {
	fakeDLQInput
	ss map[string][]string
}

func (f fakeTestInput) StringSlice(k string) []string { return f.ss[k] }
func (f fakeTestInput) Context() context.Context      { return context.Background() }

func TestInvokeAsyncSendsAsyncHeaderAndPrintsID(t *testing.T) {
	var got struct {
		method, path, body, invokeMode, hdr string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.path, got.body = r.Method, r.URL.Path, string(b)
		got.invokeMode = r.Header.Get(asyncinvoke.HeaderInvokeMode)
		got.hdr = r.Header.Get("X-Custom")
		w.Header().Set(asyncinvoke.HeaderInvocationID, "asyncinv/9")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	t.Setenv("FISSION_ROUTER_INTERNAL_URL", srv.URL)

	in := fakeTestInput{
		fakeDLQInput: fakeDLQInput{s: map[string]string{flagkey.FnTestBody: "hello"}},
		ss:           map[string][]string{flagkey.FnTestHeader: {"X-Custom: v"}},
	}
	err := (&TestSubCommand{}).invokeAsync(context.Background(), in, &metav1.ObjectMeta{Name: "fn", Namespace: "ns"}, http.MethodPost)
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/fission-function/ns/fn", got.path)
	assert.Equal(t, "hello", got.body)
	assert.Equal(t, asyncinvoke.InvokeModeAsync, got.invokeMode, "async mode header is set")
	assert.Equal(t, "v", got.hdr, "user headers are forwarded")
}

func TestInvokeAsyncDisabledAndErrorStatuses(t *testing.T) {
	cases := map[string]struct {
		status int
		errSub string
	}{
		"disabled 501":     {http.StatusNotImplemented, "not enabled"},
		"unauthorized 401": {http.StatusUnauthorized, "FISSION_INTERNAL_AUTH_SECRET"},
		"server 500":       {http.StatusInternalServerError, "500"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			t.Setenv("FISSION_ROUTER_INTERNAL_URL", srv.URL)
			err := (&TestSubCommand{}).invokeAsync(context.Background(), fakeTestInput{}, &metav1.ObjectMeta{Name: "fn", Namespace: "ns"}, http.MethodPost)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errSub)
		})
	}
}

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

// TestBuildInternalRequestSync verifies the shared helper used by the sync
// `fission fn test` path: it must hit /fission-function/<ns>/<fn> on the
// internal listener, forward user headers/body/query, set NO invoke-mode
// header, and skip HMAC signing when FISSION_INTERNAL_AUTH_SECRET is unset.
func TestBuildInternalRequestSync(t *testing.T) {
	var got struct {
		method, path, query, body, invokeMode, customHdr string
		sigHdr                                           string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method = r.Method
		got.path = r.URL.Path
		got.query = r.URL.RawQuery
		got.body = string(b)
		got.invokeMode = r.Header.Get(asyncinvoke.HeaderInvokeMode)
		got.customHdr = r.Header.Get("X-Custom")
		got.sigHdr = r.Header.Get(hmacauth.HeaderSignature)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "Hello, Fission")
	}))
	defer srv.Close()
	t.Setenv("FISSION_ROUTER_INTERNAL_URL", srv.URL)

	in := fakeTestInput{
		fakeDLQInput: fakeDLQInput{
			s:   map[string]string{flagkey.FnTestBody: "req-body"},
			set: map[string]bool{flagkey.FnSubPath: true},
		},
		ss: map[string][]string{
			flagkey.FnTestHeader: {"X-Custom: v"},
			flagkey.FnTestQuery:  {"q=42"},
		},
	}
	// fakeDLQInput.String returns "" for unset keys, so set the subpath value.
	in.s[flagkey.FnSubPath] = "sub"

	req, hc, cleanup, err := (&TestSubCommand{}).buildInternalRequest(
		context.Background(), in, &metav1.ObjectMeta{Name: "fn", Namespace: "ns"},
		http.MethodPost, "")
	require.NoError(t, err)
	defer cleanup()

	resp, err := hc.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/fission-function/ns/fn/sub", got.path, "subpath appended to fn URI")
	assert.Equal(t, "q=42", got.query, "query params encoded")
	assert.Equal(t, "req-body", got.body)
	assert.Equal(t, "v", got.customHdr, "user headers forwarded")
	assert.Empty(t, got.invokeMode, "sync path must NOT set invoke-mode header")
	assert.Empty(t, got.sigHdr, "no HMAC signature when secret is unset")
}

// TestBuildInternalRequestAsyncHeaderRegression guards the "set LAST" rule:
// even if the user passes -H "X-Fission-Invoke-Mode: sync", the helper must
// overwrite it with async when invokeModeHeader=InvokeModeAsync.
func TestBuildInternalRequestAsyncHeaderRegression(t *testing.T) {
	var gotInvokeMode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotInvokeMode = r.Header.Get(asyncinvoke.HeaderInvokeMode)
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"invocationId":"asyncinv/1"}`)
	}))
	defer srv.Close()
	t.Setenv("FISSION_ROUTER_INTERNAL_URL", srv.URL)

	in := fakeTestInput{
		ss: map[string][]string{
			flagkey.FnTestHeader: {asyncinvoke.HeaderInvokeMode + ": sync"},
		},
	}
	req, hc, cleanup, err := (&TestSubCommand{}).buildInternalRequest(
		context.Background(), in, &metav1.ObjectMeta{Name: "fn", Namespace: "ns"},
		http.MethodPost, asyncinvoke.InvokeModeAsync)
	require.NoError(t, err)
	defer cleanup()

	resp, err := hc.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, asyncinvoke.InvokeModeAsync, gotInvokeMode,
		"async header must overwrite a user-supplied sync header")
}

// TestBuildInternalRequestHMACSigned asserts that when
// FISSION_INTERNAL_AUTH_SECRET is set, the helper's transport adds the
// X-Fission-Auth-Signature + X-Fission-Auth-Timestamp headers.
func TestBuildInternalRequestHMACSigned(t *testing.T) {
	var sig, ts string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sig = r.Header.Get(hmacauth.HeaderSignature)
		ts = r.Header.Get(hmacauth.HeaderTimestamp)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("FISSION_ROUTER_INTERNAL_URL", srv.URL)
	t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "test-master-secret")

	in := fakeTestInput{}
	req, hc, cleanup, err := (&TestSubCommand{}).buildInternalRequest(
		context.Background(), in, &metav1.ObjectMeta{Name: "fn", Namespace: "ns"},
		http.MethodGet, "")
	require.NoError(t, err)
	defer cleanup()

	resp, err := hc.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.NotEmpty(t, sig, "HMAC signature header present when secret is set")
	assert.NotEmpty(t, ts, "HMAC timestamp header present when secret is set")
}

// TestBuildInternalRequestDefaultNamespaceFolding checks that
// utils.UrlForFunction folds the default namespace out of the path —
// /fission-function/<fn> not /fission-function/default/<fn>.
func TestBuildInternalRequestDefaultNamespaceFolding(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("FISSION_ROUTER_INTERNAL_URL", srv.URL)

	in := fakeTestInput{}
	req, hc, cleanup, err := (&TestSubCommand{}).buildInternalRequest(
		context.Background(), in, &metav1.ObjectMeta{Name: "fn", Namespace: "default"},
		http.MethodGet, "")
	require.NoError(t, err)
	defer cleanup()

	resp, err := hc.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "/fission-function/fn", gotPath,
		"default namespace is folded out of the direct-invoke path")
}

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

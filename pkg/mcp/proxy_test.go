// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

// resultText returns the concatenated text content of a CallToolResult.
func resultText(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range r.Content {
		tc, ok := c.(*mcp.TextContent)
		require.True(t, ok, "expected TextContent")
		b.WriteString(tc.Text)
	}
	return b.String()
}

func TestProxyInvokeSignsAndForwards(t *testing.T) {
	t.Parallel()
	master := []byte("master-secret")

	var gotPath, gotBody, gotTS, gotSig, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTS = r.Header.Get(hmacauth.HeaderTimestamp)
		gotSig = r.Header.Get(hmacauth.HeaderSignature)
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from function"))
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, master, logr.Discard())
	e := ToolEntry{ToolName: "t", Namespace: "ns", FnName: "fn"}
	res, err := p.Invoke(context.Background(), e, []byte(`{"q":"hi"}`))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsError)
	assert.Equal(t, "hello from function", resultText(t, res))

	assert.Equal(t, "/fission-function/ns/fn", gotPath)
	assert.Equal(t, `{"q":"hi"}`, gotBody)
	assert.Equal(t, "application/json", gotCT)
	assert.NotEmpty(t, gotTS, "request must be HMAC-signed (timestamp header)")
	assert.NotEmpty(t, gotSig, "request must be HMAC-signed (signature header)")
}

// TestProxyInvokeFoldsDefaultNamespace asserts a default-namespace function maps
// to the folded /fission-function/<name> route (not /fission-function/default/<name>).
func TestProxyInvokeFoldsDefaultNamespace(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard())
	_, err := p.Invoke(context.Background(), ToolEntry{Namespace: "default", FnName: "fn"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "/fission-function/fn", gotPath, "default namespace must be folded out of the path")
}

// TestProxyInvokeAliasSuffix asserts an alias-addressed ToolEntry (RFC-0025)
// proxies to the ":<alias>" internal route instead of the live function's
// bare route.
func TestProxyInvokeAliasSuffix(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard())
	_, err := p.Invoke(context.Background(), ToolEntry{Namespace: "ns", FnName: "fn", Alias: "blue"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "/fission-function/ns/fn:blue", gotPath)
}

func TestProxyInvokeUnsignedWhenNoMaster(t *testing.T) {
	t.Parallel()
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get(hmacauth.HeaderSignature)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard())
	_, err := p.Invoke(context.Background(), ToolEntry{Namespace: "ns", FnName: "fn"}, nil)
	require.NoError(t, err)
	assert.Empty(t, gotSig, "no master ⇒ unsigned (pass-through)")
}

func TestProxyInvokeStatusMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		status       int
		body         string
		wantIsError  bool
		wantContains string
	}{
		{"2xx success", http.StatusOK, "ok-body", false, "ok-body"},
		{"4xx surfaces body", http.StatusBadRequest, "bad input", true, "function returned 400"},
		{"5xx generic", http.StatusInternalServerError, "stacktrace-leak", true, "function invocation failed"},
		{"404 surfaces body", http.StatusNotFound, "no route", true, "function returned 404"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			p := NewProxy(srv.URL, nil, logr.Discard())
			res, err := p.Invoke(context.Background(), ToolEntry{Namespace: "ns", FnName: "fn"}, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.wantIsError, res.IsError)
			assert.Contains(t, resultText(t, res), tc.wantContains)
			if tc.status >= 500 {
				assert.NotContains(t, resultText(t, res), "stacktrace-leak", "5xx body must not leak to the agent")
			}
		})
	}
}

func TestProxyInvokeOversizedResponseCapped(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()

	p := NewProxy(srv.URL, nil, logr.Discard())
	p.maxBody = 10 // force the cap
	res, err := p.Invoke(context.Background(), ToolEntry{Namespace: "ns", FnName: "fn"}, nil)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "exceeded 10 bytes")
}

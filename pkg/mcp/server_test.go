// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	reg := NewRegistry()
	reg.Upsert(entry("ns-a", "fn1", "tool-a"))
	reg.Upsert(entry("ns-b", "fn2", "tool-b"))
	return NewServer(reg, NewProxy("http://router-internal", nil, logr.Discard()), NewAuthorizer([]byte("k")), logr.Discard())
}

func TestServerToolVisible(t *testing.T) {
	t.Parallel()
	s := testServer(t)

	assert.True(t, s.toolVisible("tool-a", AuthScope{Namespaces: []string{"ns-a"}}))
	assert.False(t, s.toolVisible("tool-b", AuthScope{Namespaces: []string{"ns-a"}}))
	assert.True(t, s.toolVisible("tool-b", AuthScope{Wildcard: true}))
	assert.False(t, s.toolVisible("does-not-exist", AuthScope{Wildcard: true}))
}

func TestServerFilterTools(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	all := []*mcp.Tool{{Name: "tool-a"}, {Name: "tool-b"}}

	scoped := s.filterTools(all, AuthScope{Namespaces: []string{"ns-a"}})
	assert.Len(t, scoped, 1)
	assert.Equal(t, "tool-a", scoped[0].Name)

	wild := s.filterTools([]*mcp.Tool{{Name: "tool-a"}, {Name: "tool-b"}}, AuthScope{Wildcard: true})
	assert.Len(t, wild, 2)
}

// TestServerHTTPHandlerLoopbackHostOverLoopback pins the client contract the
// SDK's DNS-rebinding protection imposes: port-forwarded traffic reaches the
// pod via loopback, so clients must present a loopback Host header — which is
// then accepted. (A non-loopback Host over loopback is rejected 403 by the
// SDK; the integration framework satisfies this via a per-route Host rewrite.)
func TestServerHTTPHandlerLoopbackHostOverLoopback(t *testing.T) {
	t.Parallel()
	// Pass-through authorizer (nil key): authz is not what's under test.
	s := NewServer(NewRegistry(), NewProxy("http://router-internal", nil, logr.Discard()), NewAuthorizer(nil), logr.Discard())
	srv := httptest.NewServer(s.HTTPHandler())
	t.Cleanup(srv.Close)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	do := func(host string) int {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, strings.NewReader(body))
		require.NoError(t, err)
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	assert.Equal(t, http.StatusOK, do("127.0.0.1"),
		"loopback Host over a loopback connection must be accepted")
	assert.Equal(t, http.StatusForbidden, do("mcp.fission"),
		"non-loopback Host over a loopback connection is rejected by the SDK's DNS-rebinding protection (clients behind forwards must present a loopback Host)")
}

// TestServerStreamingToolCall drives the full wire: a streaming-enabled tool
// whose backing function writes two flushed chunks, called through a real SDK
// client over the streamable HTTP transport. With a progress token the chunks
// arrive as progress notifications AND the final result is intact; without a
// token nothing is streamed (spec: progress notifications require a token).
func TestServerStreamingToolCall(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	fnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f := w.(http.Flusher)
		_, _ = w.Write([]byte("hello "))
		f.Flush()
		select {
		case <-release: // first notification observed client-side
		case <-time.After(10 * time.Second): // fail-safe: don't wedge the suite
		}
		_, _ = w.Write([]byte("world"))
	}))
	t.Cleanup(fnSrv.Close)

	reg := NewRegistry()
	e := entry("ns-a", "fn1", "stream-tool")
	e.Streaming = true
	reg.Upsert(e)
	s := NewServer(reg, NewProxy(fnSrv.URL, nil, logr.Discard()), NewAuthorizer(nil), logr.Discard())
	s.ApplyToolDelta([]ToolEntry{e}, nil) // what the reconciler does on upsert
	srv := httptest.NewServer(s.HTTPHandler())
	t.Cleanup(srv.Close)

	var mu sync.Mutex
	var msgs []string
	var once sync.Once
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			msgs = append(msgs, req.Params.Message)
			mu.Unlock()
			once.Do(func() { close(release) })
		},
	})
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	params := &mcp.CallToolParams{Name: "stream-tool"}
	params.SetProgressToken("tok-1")
	res, err := sess.CallTool(t.Context(), params)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "hello world", res.Content[0].(*mcp.TextContent).Text)

	// Notifications are asynchronous: the tail chunk may still be in flight
	// when CallTool returns.
	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(msgs, "") == "hello world"
	}, 5*time.Second, 10*time.Millisecond, "progress messages must concatenate to the final text")

	mu.Lock()
	seen := len(msgs)
	mu.Unlock()

	// Without a progress token the same tool call must stay fully buffered.
	res, err = sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "stream-tool"})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Equal(t, "hello world", res.Content[0].(*mcp.TextContent).Text)
	mu.Lock()
	assert.Equal(t, seen, len(msgs), "token-less call must not emit progress")
	mu.Unlock()
}

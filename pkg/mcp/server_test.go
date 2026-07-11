// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

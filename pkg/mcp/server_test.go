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

// TestServerHTTPHandlerAllowsHostnameOverLoopback pins the DNS-rebinding
// posture: this is a cluster service (not a local dev MCP server), and
// port-forwarded traffic always reaches the pod via loopback — a client using
// a hostname (e.g. the integration framework's mcp.fission route) must not be
// rejected by the SDK's localhost heuristic with 403.
func TestServerHTTPHandlerAllowsHostnameOverLoopback(t *testing.T) {
	t.Parallel()
	// Pass-through authorizer (nil key): authz is not what's under test.
	s := NewServer(NewRegistry(), NewProxy("http://router-internal", nil, logr.Discard()), NewAuthorizer(nil), logr.Discard())
	srv := httptest.NewServer(s.HTTPHandler())
	t.Cleanup(srv.Close)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, strings.NewReader(body))
	require.NoError(t, err)
	req.Host = "mcp.fission" // non-loopback Host over a loopback connection
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
		"initialize with a hostname Host header must not trip localhost DNS-rebinding protection")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

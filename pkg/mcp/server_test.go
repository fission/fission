// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
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

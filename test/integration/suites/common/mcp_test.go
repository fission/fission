// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestMCPToolsListAndCall exercises the RFC-0011 Part A MCP path end-to-end
// against a real Node.js runtime: a function created with --expose-as-mcp is
// (1) advertised by tools/list with its declared description and input schema,
// and (2) callable via tools/call, returning the same body as a direct
// invocation on the router internal listener.
//
// The MCP subsystem is enabled in the kind/kind-ci skaffold profiles and
// port-forwarded to FISSION_MCP_BASE_URL by the suite bootstrap; the test skips
// when the endpoint is unreachable (MCP off in this install). In CI the server
// runs in pass-through auth mode (authentication disabled), so no bearer token
// is needed; per-namespace JWT scoping is covered by the pkg/mcp unit tests.
func TestMCPToolsListAndCall(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	base := f.MCPBaseURL()
	requireMCPReachable(t, ctx, base)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-mcp-" + ns.ID
	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	fnName := "mcp-hello-" + ns.ID
	toolName := fnName // explicit, unique across the cluster-wide registry
	schemaFile := framework.WriteTestData(t, "mcp/schema.json")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name:            fnName,
		Env:             envName,
		Code:            framework.WriteTestData(t, "nodejs/hello/hello.js"),
		ExposeAsMCP:     true,
		ToolDescription: "greets the caller",
		ToolName:        toolName,
		ToolInputSchema: schemaFile,
	})
	ns.WaitForFunction(t, ctx, fnName)

	session := connectMCP(t, ctx, base)
	defer func() { _ = session.Close() }()

	// tools/list eventually includes our tool (the reconciler registers it
	// asynchronously after the Function is observed).
	t.Run("tools/list advertises the function", func(t *testing.T) {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			res, err := session.ListTools(ctx, nil)
			if !assert.NoError(c, err) {
				return
			}
			tool := findTool(res.Tools, toolName)
			if !assert.NotNil(c, tool, "tool %q should be listed", toolName) {
				return
			}
			assert.Equal(c, "greets the caller", tool.Description)
			assert.Contains(c, schemaTypes(c, tool), "name", "advertised input schema should carry our property")
		}, 90*time.Second, 3*time.Second)
	})

	// tools/call returns the same body as a direct internal-listener invocation.
	t.Run("tools/call invokes the function", func(t *testing.T) {
		_, direct, err := f.Router(t).Post(ctx, "/fission-function/"+ns.Name+"/"+fnName, "application/json", []byte(`{"name":"world"}`))
		require.NoError(t, err)

		require.EventuallyWithT(t, func(c *assert.CollectT) {
			res, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name:      toolName,
				Arguments: map[string]any{"name": "world"},
			})
			if !assert.NoError(c, err) {
				return
			}
			if !assert.False(c, res.IsError, "tool call should succeed: %s", callText(res)) {
				return
			}
			assert.Equal(c, strings.TrimSpace(direct), strings.TrimSpace(callText(res)),
				"tools/call body should match a direct internal-listener invocation")
		}, 90*time.Second, 3*time.Second)
	})
}

// requireMCPReachable skips the test when the MCP endpoint isn't serving (MCP
// disabled in this install, or no port-forward).
func requireMCPReachable(t *testing.T, ctx context.Context, base string) {
	t.Helper()
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimRight(base, "/")+"/healthz", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("MCP endpoint %s not reachable (%v); skipping", base, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("MCP endpoint %s returned %d; skipping", base, resp.StatusCode)
	}
}

func connectMCP(t *testing.T, ctx context.Context, base string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "fission-it", Version: "test"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: strings.TrimRight(base, "/") + "/mcp"}
	session, err := client.Connect(ctx, transport, nil)
	require.NoError(t, err, "connect MCP client to %s", base)
	return session
}

func findTool(tools []*mcp.Tool, name string) *mcp.Tool {
	for _, t := range tools {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// schemaTypes returns the property names declared in a tool's input schema.
func schemaTypes(c *assert.CollectT, tool *mcp.Tool) []string {
	raw, err := json.Marshal(tool.InputSchema)
	if !assert.NoError(c, err) {
		return nil
	}
	var parsed struct {
		Properties map[string]any `json:"properties"`
	}
	if !assert.NoError(c, json.Unmarshal(raw, &parsed)) {
		return nil
	}
	keys := make([]string, 0, len(parsed.Properties))
	for k := range parsed.Properties {
		keys = append(keys, k)
	}
	return keys
}

func callText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

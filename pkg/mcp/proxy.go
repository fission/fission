// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
)

const (
	// defaultCallTimeout bounds a single tool-call invocation. Tools are one-shot
	// (no streaming result), so this is a hard ceiling on how long the agent
	// waits for the function.
	defaultCallTimeout = 60 * time.Second

	// defaultMaxResponseBytes caps the buffered function response. The MCP tool
	// model returns a single result with no incremental streaming, so the whole
	// upstream body is buffered; this bounds memory per call.
	defaultMaxResponseBytes int64 = 1 << 20 // 1 MiB
)

// Proxy invokes an MCP tool's backing function over the router internal
// listener, signing the request with the ServiceRouterInternal HMAC key exactly
// as the other internal publishers do. It buffers the response (the MCP tool
// model has no streamed result) into a single CallToolResult.
type Proxy struct {
	baseURL string
	client  *http.Client
	maxBody int64
	timeout time.Duration
	logger  logr.Logger
}

// NewProxy builds a proxy targeting routerInternalURL. When hmacMaster is
// non-empty the outbound transport signs requests for ServiceRouterInternal
// (HKDF-derived key); empty leaves requests unsigned, matching the verifier's
// pass-through mode. The transport stack mirrors publisher.newWebhookHTTPClient.
func NewProxy(routerInternalURL string, hmacMaster []byte, logger logr.Logger) *Proxy {
	var rt http.RoundTripper = otelhttp.NewTransport(http.DefaultTransport)
	if len(hmacMaster) > 0 {
		rt = hmacauth.ServiceSigner(hmacMaster, hmacauth.ServiceRouterInternal, rt, time.Now)
	}
	return &Proxy{
		baseURL: strings.TrimRight(routerInternalURL, "/"),
		client:  &http.Client{Transport: rt},
		maxBody: defaultMaxResponseBytes,
		timeout: defaultCallTimeout,
		logger:  logger.WithName("proxy"),
	}
}

// Invoke proxies a tool call to the function's internal endpoint and maps the
// response into a CallToolResult. Tool-level failures (a function 4xx/5xx, a
// timeout, an oversized body) are returned as a CallToolResult with IsError set
// so the agent can see and self-correct; a non-nil error is returned only for
// failures the MCP layer should surface as a protocol error.
func (p *Proxy) Invoke(ctx context.Context, e ToolEntry, args []byte) (*mcp.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	url := p.baseURL + "/fission-function/" + e.Namespace + "/" + e.FnName
	if len(args) == 0 {
		args = []byte("{}")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(args))
	if err != nil {
		return nil, fmt.Errorf("building tool request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		// Distinguish a deadline (the function ran too long) from other transport
		// failures; neither leaks internal detail to the agent.
		if errors.Is(err, context.DeadlineExceeded) {
			p.logger.V(1).Info("tool call timed out", "tool", e.ToolName, "function", e.FnName, "namespace", e.Namespace)
			return toolError("function invocation timed out"), nil
		}
		p.logger.Error(err, "tool call transport error", "tool", e.ToolName, "function", e.FnName, "namespace", e.Namespace)
		return toolError("function invocation failed"), nil
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, p.maxBody+1))
	if readErr != nil {
		p.logger.Error(readErr, "reading tool response", "tool", e.ToolName)
		return toolError("function invocation failed"), nil
	}
	if int64(len(body)) > p.maxBody {
		p.logger.V(1).Info("tool response exceeded cap", "tool", e.ToolName, "cap", p.maxBody)
		return toolError(fmt.Sprintf("function response exceeded %d bytes", p.maxBody)), nil
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// Client error from the function: surface a truncated body so the agent
		// can adjust its arguments.
		return toolError(fmt.Sprintf("function returned %d: %s", resp.StatusCode, truncate(string(body), 512))), nil
	default:
		// 5xx (incl. the router's 404 while a route propagates): generic message,
		// detail stays in the server log. No retry — agents retry themselves.
		p.logger.V(1).Info("tool call upstream error", "tool", e.ToolName, "status", resp.StatusCode)
		return toolError("function invocation failed"), nil
	}
}

// toolError builds an error CallToolResult visible to the model.
func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

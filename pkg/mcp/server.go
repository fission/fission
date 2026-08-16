// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fission/fission/pkg/info"
)

const (
	methodToolsList = "tools/list"
	methodToolsCall = "tools/call"

	// maxRequestBytes caps a single inbound /mcp request body (the JSON-RPC
	// envelope, incl. tools/call arguments) to bound per-request memory.
	// Passed explicitly: the SDK's own default is 4 MiB.
	maxRequestBytes int64 = 1 << 20 // 1 MiB
)

// errUnauthorized is returned when a request carries no valid scope (verification
// is enabled but no token info reached the handler). errToolNotFound is returned
// for both unknown and out-of-scope tools so a caller cannot probe tools in
// namespaces it is not authorized for. Both surface as MCP protocol errors.
var (
	errUnauthorized = errors.New("unauthorized")
	errToolNotFound = errors.New("tool not found")
)

// tokenInfoOf extracts the bearer token info the transport attached to a request
// (nil when authz is in pass-through mode).
func tokenInfoOf(req mcp.Request) *auth.TokenInfo {
	if extra := req.GetExtra(); extra != nil {
		return extra.TokenInfo
	}
	return nil
}

// Server is the MCP protocol server: one shared *mcp.Server whose tool set the
// reconciler mutates, fronted by namespace-scoping authz. A single long-lived
// server (not a per-request rebuild) is what lets the SDK emit
// notifications/tools/list_changed when the CRD-driven tool set changes.
type Server struct {
	mcp    *mcp.Server
	reg    *Registry
	proxy  *Proxy
	authz  *Authorizer
	logger logr.Logger
}

// NewServer constructs the MCP server over the given registry, proxy, and
// authorizer. It installs the receiving middleware that filters tools/list and
// gates tools/call by namespace scope.
func NewServer(reg *Registry, proxy *Proxy, authz *Authorizer, logger logr.Logger) *Server {
	impl := &mcp.Implementation{Name: "fission-mcp", Version: info.BuildInfo().GitCommit}
	s := &Server{
		mcp:    mcp.NewServer(impl, nil),
		reg:    reg,
		proxy:  proxy,
		authz:  authz,
		logger: logger.WithName("server"),
	}
	s.mcp.AddReceivingMiddleware(s.scopeMiddleware)
	return s
}

// ApplyToolDelta registers added/changed tools and removes deleted ones on the
// shared server. AddTool overwrites a same-named registration, so a changed tool
// is re-added. Called only from the reconciler (serialized by the workqueue).
func (s *Server) ApplyToolDelta(add []ToolEntry, removeNames []string) {
	if len(removeNames) > 0 {
		s.mcp.RemoveTools(removeNames...)
	}
	for _, e := range add {
		s.mcp.AddTool(&mcp.Tool{
			Name:        e.ToolName,
			Description: e.Description,
			InputSchema: e.InputSchema,
		}, s.callTool)
	}
}

// HTTPHandler returns the http.Handler for the MCP Streamable HTTP transport,
// wrapped with bearer-token authz (pass-through when no signing key is set).
//
// The transport runs STATELESS (SEP-2567, protocol 2026-07-28): no
// Mcp-Session-Id is issued or honored, each request is self-describing, and
// GET/DELETE return 405. This is what makes `mcp.replicas > 1` behind the
// plain ClusterIP Service actually correct — a stateful session pinned to
// one replica's memory never survived the Service's round-robin, whereas
// stateless requests are served identically by any replica (every replica
// reconciles the full tool registry). Everything this server does fits the
// model: tools/list and tools/call are one-shot, and streaming progress
// notifications ride the POST's own SSE response stream, which the SDK
// delivers in stateless mode. Legacy (<= 2025-11-25) clients negotiate down
// and keep working; the SDK rejects the session-bound calls they no longer
// need. Tool-list change notifications are best-effort in this model (a
// client learns of changes on its next tools/list, or via
// subscriptions/listen on new-protocol clients).
func (s *Server) HTTPHandler() http.Handler {
	// The SDK's localhost DNS-rebinding protection stays ON: port-forwarded
	// traffic lands on the pod's loopback, so clients reaching svc/mcp
	// through a forward must present a loopback Host header (127.0.0.1, the
	// documented form; the integration framework rewrites its route's Host
	// accordingly). Non-loopback Hosts over a forward are rejected 403.
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s.mcp },
		&mcp.StreamableHTTPOptions{
			Stateless: true,
			// The SDK enforces the cap during the read (Content-Length,
			// chunked, and HTTP/2 alike) and answers 413.
			MaxRequestBodyBytes: maxRequestBytes,
			// Tie the handler ctx to the POST's lifecycle: once the caller
			// is gone the response can't be delivered, so an in-flight
			// tools/call (a long stream especially) is cancelled instead of
			// running the function to completion for nobody. The proxy
			// classifies that as "canceled", not a function failure.
			PropagateRequestCancellation: true,
		},
	)
	return s.authz.HTTPMiddleware(streamable)
}

// scopeMiddleware enforces the caller's namespace scope: it drops out-of-scope
// tools from tools/list and rejects out-of-scope (or unknown) tools/call as a
// not-found protocol error, before the SDK dispatches the call. The tool handler
// re-checks as defense in depth.
func (s *Server) scopeMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		scope, ok := s.scopeFor(req)
		if !ok {
			return nil, errUnauthorized
		}

		switch method {
		case methodToolsCall:
			// Behave as "tool not found" — both for out-of-scope tools (so a
			// caller cannot probe which tools exist in namespaces it isn't
			// authorized for) and for an unexpected params shape (don't fall
			// through to dispatch on a failed assertion).
			params, ok := req.GetParams().(*mcp.CallToolParamsRaw)
			if !ok || !s.toolVisible(params.Name, scope) {
				return nil, errToolNotFound
			}
		}

		res, err := next(ctx, method, req)
		if err != nil {
			return res, err
		}

		if method == methodToolsList {
			if lr, isList := res.(*mcp.ListToolsResult); isList {
				lr.Tools = s.filterTools(lr.Tools, scope)
			}
		}
		return res, nil
	}
}

// callTool is the shared handler for every registered tool. It resolves the tool
// by name, re-checks the caller's namespace scope (defense in depth), and proxies
// to the function's internal endpoint.
func (s *Server) callTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	scope, ok := s.scopeFor(req)
	if !ok {
		return nil, errUnauthorized
	}
	entry, found := s.reg.Lookup(req.Params.Name)
	if !found || !scope.Allows(entry.Namespace) {
		return nil, errToolNotFound
	}
	// Streaming needs BOTH sides opted in: the function declares Streaming,
	// and the caller sent a progress token (the MCP spec permits progress
	// notifications only for requests that carried one). Everything else
	// takes the buffered path unchanged.
	//
	// Contract boundary: the chunks ride ProgressNotificationParams.Message,
	// which the MCP spec frames as a progress DESCRIPTION — a cooperating
	// client concatenates the messages for live output, but a conforming
	// client is free to truncate or ignore them. The final CallToolResult is
	// authoritative either way; the SDK offers no other server→client
	// tool-output stream today.
	if token := req.Params.GetProgressToken(); entry.Streaming && token != nil {
		session := req.Session
		notify := func(ctx context.Context, chunk string, progressBytes int64) {
			// Best-effort: a failed notification (client gone mid-stream)
			// must not fail the call — the final result still returns.
			err := session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token,
				Progress:      float64(progressBytes),
				Message:       chunk,
			})
			if err != nil {
				s.logger.V(1).Info("progress notify failed", "tool", entry.ToolName, "error", err)
			}
		}
		return s.proxy.InvokeStreaming(ctx, entry, req.Params.Arguments, notify)
	}
	return s.proxy.Invoke(ctx, entry, req.Params.Arguments)
}

// scopeFor derives the caller's namespace scope from the request's token info.
func (s *Server) scopeFor(req mcp.Request) (AuthScope, bool) {
	var ti = tokenInfoOf(req)
	return s.authz.ScopeFromTokenInfo(ti)
}

// toolVisible reports whether a tool name resolves to a namespace the scope
// allows.
func (s *Server) toolVisible(name string, scope AuthScope) bool {
	entry, found := s.reg.Lookup(name)
	return found && scope.Allows(entry.Namespace)
}

// filterTools drops tools whose namespace the scope does not allow.
func (s *Server) filterTools(tools []*mcp.Tool, scope AuthScope) []*mcp.Tool {
	out := tools[:0]
	for _, t := range tools {
		if s.toolVisible(t.Name, scope) {
			out = append(out, t)
		}
	}
	return out
}

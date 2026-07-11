// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fission/fission/pkg/info"
)

const (
	methodToolsList = "tools/list"
	methodToolsCall = "tools/call"

	// sessionTimeout closes idle MCP sessions so abandoned connections don't
	// accumulate unbounded server-side state. Clients reconnect transparently.
	sessionTimeout = 30 * time.Minute

	// maxRequestBytes caps a single inbound /mcp request body (the JSON-RPC
	// envelope, incl. tools/call arguments) to bound per-request memory.
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
// wrapped with bearer-token authz (pass-through when no signing key is set) and
// a per-request body cap. SessionTimeout bounds idle-session accumulation.
func (s *Server) HTTPHandler() http.Handler {
	// The SDK's localhost DNS-rebinding protection stays ON: port-forwarded
	// traffic lands on the pod's loopback, so clients reaching svc/mcp
	// through a forward must present a loopback Host header (127.0.0.1, the
	// documented form; the integration framework rewrites its route's Host
	// accordingly). Non-loopback Hosts over a forward are rejected 403.
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s.mcp },
		&mcp.StreamableHTTPOptions{SessionTimeout: sessionTimeout},
	)
	return s.authz.HTTPMiddleware(limitRequestBody(streamable))
}

// limitRequestBody caps each request body so a single oversized JSON-RPC
// envelope can't exhaust memory. The SDK surfaces the read error as a protocol
// error to the caller.
func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		next.ServeHTTP(w, r)
	})
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

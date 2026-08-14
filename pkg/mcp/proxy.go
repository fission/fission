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
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/httpx"
)

const (
	// defaultCallTimeout bounds a single BUFFERED tool-call invocation: a hard
	// ceiling on how long the agent waits for a one-shot function response.
	// Streaming calls (InvokeStreaming) escape this ceiling by design and are
	// bounded by their idle timeout (and optional StreamMaxDuration) instead.
	defaultCallTimeout = 60 * time.Second

	// defaultStreamIdleTimeout bounds the wait between chunks on the streaming
	// path when the function's StreamingConfig leaves IdleTimeoutSeconds zero.
	// Matches the buffered path's ceiling so an idle stream and a slow one-shot
	// call time out on the same clock.
	defaultStreamIdleTimeout = 60 * time.Second

	// defaultMaxResponseBytes caps the function response buffered into the
	// final CallToolResult — on the streaming path too, where the same bytes
	// are additionally forwarded incrementally; this bounds memory per call.
	defaultMaxResponseBytes int64 = 1 << 20 // 1 MiB

	// defaultMaxConcurrent bounds in-flight tool calls so the per-call buffers
	// (defaultMaxResponseBytes each) can't exhaust memory under load. Excess
	// calls wait for a slot until their context deadline.
	defaultMaxConcurrent = 64
)

// Proxy invokes an MCP tool's backing function over the router internal
// listener, signing the request with the ServiceRouterInternal HMAC key exactly
// as the other internal publishers do. Invoke buffers the response into a
// single CallToolResult; InvokeStreaming additionally forwards the response
// body incrementally (MCP progress notifications) while accumulating the same
// final result.
type Proxy struct {
	baseURL string
	client  *http.Client
	maxBody int64
	timeout time.Duration
	sem     chan struct{} // bounds concurrent in-flight calls
	logger  logr.Logger
}

// NewProxy builds a proxy targeting routerInternalURL. When hmacMaster is
// non-empty the outbound transport signs requests for ServiceRouterInternal
// (HKDF-derived key); empty leaves requests unsigned, matching the verifier's
// pass-through mode.
func NewProxy(routerInternalURL string, hmacMaster []byte, logger logr.Logger) *Proxy {
	// Pooled transport sized to defaultMaxConcurrent in-flight tool calls, all to
	// the single router-internal host (see httpx.PooledTransport).
	var rt http.RoundTripper = otelhttp.NewTransport(httpx.PooledTransport(defaultMaxConcurrent))
	if len(hmacMaster) > 0 {
		rt = hmacauth.ServiceSigner(hmacMaster, hmacauth.ServiceRouterInternal, rt, time.Now)
	}
	return &Proxy{
		baseURL: strings.TrimRight(routerInternalURL, "/"),
		client:  &http.Client{Transport: rt},
		maxBody: defaultMaxResponseBytes,
		timeout: defaultCallTimeout,
		sem:     make(chan struct{}, defaultMaxConcurrent),
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

	// Bound concurrent calls so per-call response buffers can't exhaust memory.
	// Wait for a slot until the context deadline rather than failing immediately.
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		p.logger.V(1).Info("tool call shed: concurrency limit", "tool", e.ToolName)
		return toolError("function invocation failed: server busy"), nil
	}

	req, err := p.buildRequest(ctx, e, args)
	if err != nil {
		return nil, err
	}
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
	return p.mapResponse(e, resp)
}

// buildRequest builds the signed-transport POST to the function's internal
// route. UrlForFunctionRef folds the default namespace
// (→ /fission-function/<name>), matching how the router registers the internal
// route; a hardcoded /fission-function/<ns>/<name> would not resolve for
// default-namespace functions. e.Alias, when set (RFC-0025), appends the
// ":<alias>" suffix so the call is proxied through the alias's
// currently-resolved version rather than straight to the live function --
// resolution of what that routes to stays entirely router-side.
func (p *Proxy) buildRequest(ctx context.Context, e ToolEntry, args []byte) (*http.Request, error) {
	url := p.baseURL + utils.UrlForFunctionRef(e.FnName, e.Namespace, e.Alias)
	if len(args) == 0 {
		args = []byte("{}")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(args))
	if err != nil {
		return nil, fmt.Errorf("building tool request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// mapResponse reads the capped body and maps status → CallToolResult: the
// buffered contract. Streaming 2xx bodies never come here — InvokeStreaming
// routes only its non-2xx responses through this mapping.
func (p *Proxy) mapResponse(e ToolEntry, resp *http.Response) (*mcp.CallToolResult, error) {
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, p.maxBody+1))
	if readErr != nil {
		p.logger.Error(readErr, "reading tool response", "tool", e.ToolName, "function", e.FnName, "namespace", e.Namespace)
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
		// Client error from the function (incl. a transient router 404 while a
		// route propagates): surface a truncated body so the agent can adjust.
		return toolError(fmt.Sprintf("function returned %d: %s", resp.StatusCode, truncate(string(body), 512))), nil
	default:
		// 5xx: generic message, detail stays in the server log. No retry — agents
		// retry themselves.
		p.logger.V(1).Info("tool call upstream error", "tool", e.ToolName, "status", resp.StatusCode)
		return toolError("function invocation failed"), nil
	}
}

// streamNotify delivers one UTF-8-complete chunk of function output to the
// caller; progressBytes is the cumulative byte count (monotonic, so it can
// feed the MCP progress field directly). Called only from the read loop, in
// order.
type streamNotify func(ctx context.Context, chunk string, progressBytes int64)

// InvokeStreaming proxies a tool call like Invoke but forwards the response
// body incrementally through notify while accumulating the final
// CallToolResult. Timeouts mirror StreamingConfig semantics instead of the
// buffered path's hard ceiling: an idle timer (reset per chunk) governs, and
// StreamMaxDuration, when non-zero, caps total lifetime. The response cap is
// unchanged — a body over maxBody cancels the upstream read and returns the
// same IsError result as the buffered path (the final result is the content
// of record; a truncated one would let the agent act on incomplete data).
func (p *Proxy) InvokeStreaming(ctx context.Context, e ToolEntry, args []byte, notify streamNotify) (*mcp.CallToolResult, error) {
	var cancel context.CancelFunc
	if e.StreamMaxDuration > 0 {
		ctx, cancel = context.WithTimeout(ctx, e.StreamMaxDuration)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	// Same slot accounting as Invoke: streaming calls hold their slot for the
	// stream's lifetime — that is the point of the bound.
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		p.logger.V(1).Info("tool call shed: concurrency limit", "tool", e.ToolName)
		return toolError("function invocation failed: server busy"), nil
	}

	idle := e.StreamIdleTimeout
	if idle <= 0 {
		idle = defaultStreamIdleTimeout
	}
	// The idle timer cancels the request context when no bytes flow for
	// `idle`; idleFired distinguishes that from a caller cancel, since both
	// surface as context.Canceled.
	var idleFired atomic.Bool
	idleTimer := time.AfterFunc(idle, func() { idleFired.Store(true); cancel() })
	defer idleTimer.Stop()

	req, err := p.buildRequest(ctx, e, args)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if idleFired.Load() || errors.Is(err, context.DeadlineExceeded) {
			p.logger.V(1).Info("tool call timed out", "tool", e.ToolName, "function", e.FnName, "namespace", e.Namespace)
			return toolError("function invocation timed out"), nil
		}
		p.logger.Error(err, "tool call transport error", "tool", e.ToolName, "function", e.FnName, "namespace", e.Namespace)
		return toolError("function invocation failed"), nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Error statuses take the buffered mapping; nothing is streamed.
		return p.mapResponse(e, resp)
	}

	var acc bytes.Buffer
	var pending []byte // partial-rune holdback so every chunk is valid UTF-8
	var total int64
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			idleTimer.Reset(idle)
			total += int64(n)
			if total > p.maxBody {
				cancel() // stop the upstream body, don't drain past the cap
				p.logger.V(1).Info("tool response exceeded cap", "tool", e.ToolName, "cap", p.maxBody)
				return toolError(fmt.Sprintf("function response exceeded %d bytes", p.maxBody)), nil
			}
			acc.Write(buf[:n])
			if notify != nil {
				pending = append(pending, buf[:n]...)
				emit, rest := splitRuneBoundary(pending)
				if len(emit) > 0 {
					notify(ctx, string(emit), total)
				}
				pending = append(pending[:0], rest...)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if idleFired.Load() || errors.Is(readErr, context.DeadlineExceeded) || ctx.Err() != nil {
				p.logger.V(1).Info("tool call timed out mid-stream", "tool", e.ToolName, "function", e.FnName, "namespace", e.Namespace)
				return toolError("function invocation timed out"), nil
			}
			p.logger.Error(readErr, "reading tool response", "tool", e.ToolName, "function", e.FnName, "namespace", e.Namespace)
			return toolError("function invocation failed"), nil
		}
	}
	if notify != nil && len(pending) > 0 {
		// A trailing partial rune at EOF is the function's own invalid UTF-8;
		// forward it so progress and the final result stay byte-identical.
		notify(ctx, string(pending), total)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: acc.String()}}}, nil
}

// splitRuneBoundary splits b so emit ends on a complete UTF-8 rune and rest
// holds a trailing partial rune (at most utf8.UTFMax-1 bytes). Bytes that are
// not valid rune starts are emitted as-is: the buffered final result carries
// them identically, so the two views never diverge.
func splitRuneBoundary(b []byte) (emit, rest []byte) {
	start := len(b)
	for start > 0 && len(b)-start < utf8.UTFMax && !utf8.RuneStart(b[start-1]) {
		start--
	}
	if start == 0 || utf8.FullRune(b[start-1:]) {
		return b, nil
	}
	return b[:start-1], b[start-1:]
}

// toolError builds an error CallToolResult visible to the model.
func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// truncate clips s to at most n bytes without splitting a multi-byte rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

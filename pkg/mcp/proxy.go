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
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/router/streaming"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/httpx"
)

const (
	// defaultCallTimeout bounds a single BUFFERED tool-call invocation: a hard
	// ceiling on how long the agent waits for a one-shot function response.
	// Streaming calls (InvokeStreaming) escape this ceiling by design and are
	// bounded by their idle timeout (and optional StreamMaxDuration) instead.
	defaultCallTimeout = 60 * time.Second

	// streamDrainTimeout bounds how long InvokeStreaming waits after upstream
	// EOF for the delivery goroutine to flush pending progress notifications
	// to the client. A healthy client drains instantly (ordering preserved:
	// notifications before the final result); a stalled one forfeits its tail
	// notifications — the final buffered result is authoritative either way.
	streamDrainTimeout = 5 * time.Second

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
	// streamIdleDefault is the idle timeout applied to a streaming call whose
	// function leaves StreamingConfig.IdleTimeoutSeconds zero. It defaults to
	// fv1.DefaultStreamIdleSeconds and is overridable via
	// WithStreamIdleDefault so the MCP path honors the same cluster-wide
	// ROUTER_STREAM_IDLE_TIMEOUT policy the router applies to the identical
	// IdleTimeoutSeconds=0 case (see Start) — one function, one idle policy,
	// regardless of entry path.
	streamIdleDefault time.Duration
	sem               chan struct{} // bounds concurrent in-flight calls
	logger            logr.Logger
}

// NewProxy builds a proxy targeting routerInternalURL. When hmacMaster is
// non-empty the outbound transport signs requests for ServiceRouterInternal
// (HKDF-derived key); empty leaves requests unsigned, matching the verifier's
// pass-through mode. streamIdleDefault <= 0 keeps the package default
// (fv1.DefaultStreamIdleSeconds); Start passes the operator's
// ROUTER_STREAM_IDLE_TIMEOUT override.
func NewProxy(routerInternalURL string, hmacMaster []byte, logger logr.Logger, streamIdleDefault time.Duration) *Proxy {
	// Pooled transport sized to defaultMaxConcurrent in-flight tool calls, all to
	// the single router-internal host (see httpx.PooledTransport).
	var rt http.RoundTripper = otelhttp.NewTransport(httpx.PooledTransport(defaultMaxConcurrent))
	if len(hmacMaster) > 0 {
		rt = hmacauth.ServiceSigner(hmacMaster, hmacauth.ServiceRouterInternal, rt, time.Now)
	}
	if streamIdleDefault <= 0 {
		streamIdleDefault = time.Duration(fv1.DefaultStreamIdleSeconds) * time.Second
	}
	return &Proxy{
		baseURL:           strings.TrimRight(routerInternalURL, "/"),
		client:            &http.Client{Transport: rt},
		maxBody:           defaultMaxResponseBytes,
		timeout:           defaultCallTimeout,
		streamIdleDefault: streamIdleDefault,
		sem:               make(chan struct{}, defaultMaxConcurrent),
		logger:            logger.WithName("proxy"),
	}
}

// acquireSlot takes a concurrency slot, trying without blocking first (the
// common uncontended case allocates no timer) and otherwise waiting at most
// defaultCallTimeout — streaming callers carry no context deadline of their
// own, and in-flight streams can hold slots indefinitely, so an unbounded
// wait would park queued callers forever. The caller releases with
// `<-p.sem` on success.
func (p *Proxy) acquireSlot(ctx context.Context) bool {
	select {
	case p.sem <- struct{}{}:
		return true
	default:
	}
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	select {
	case p.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
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
	if !p.acquireSlot(ctx) {
		p.logger.V(1).Info("tool call shed: concurrency limit", "tool", e.ToolName)
		return toolError("function invocation failed: server busy"), nil
	}
	defer func() { <-p.sem }()

	req, err := p.buildRequest(ctx, e, args)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// Same classifier as the streaming path: a deadline is the function
		// running too long, a caller cancel (the transport propagates client
		// disconnects into the handler ctx) is benign, and neither leaks
		// internal detail to the agent.
		return p.streamAbort(ctx, e, err, "connecting"), nil
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
//
// No explicit propagation.Inject here: VERIFIED (TestCallTool_
// OutboundProxyRequestCarriesExtractedTraceparent, tracing_test.go) that
// http.NewRequestWithContext(ctx, ...) below is sufficient by itself -- the
// client's transport (NewProxy) is otelhttp.NewTransport(...), which reads
// the span off ctx and injects via the process-wide otel.GetTextMapPropagator()
// on every RoundTrip automatically. callTool (server.go) starts the
// execute_tool span (parented on the _meta-extracted context, when present)
// BEFORE calling Invoke/InvokeStreaming, so by the time ctx reaches here it
// already carries that span -- an explicit Inject would be redundant with
// what the transport already does.
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
// caller; progressBytes is the cumulative delivered byte count (monotonic, so
// it can feed the MCP progress field directly). Called from the delivery
// goroutine, in order. A non-nil error means the caller can no longer be
// reached (its response stream is closed) and is the proxy's ONLY
// client-gone signal for legacy-protocol callers: the SDK propagates HTTP
// request cancellation into the handler ctx solely for protocol
// >= 2026-07-28, so without this a 2025-11-25 client that disconnects
// mid-stream would leave the call — and its semaphore slot, upstream stream,
// and pod — running until the function EOFs.
type streamNotify func(ctx context.Context, chunk string, progressBytes int64) error

// InvokeStreaming proxies a tool call like Invoke but forwards the response
// body incrementally through notify while accumulating the final
// CallToolResult. Timeouts mirror StreamingConfig semantics instead of the
// buffered path's hard ceiling: an idle watchdog (fed by upstream activity)
// governs, and StreamMaxDuration, when non-zero, caps total lifetime — an
// active stream with no ceiling legitimately holds its semaphore slot for its
// whole lifetime, which is the accepted cost of serving long streams from the
// shared bounded pool. The response cap is unchanged — a body over maxBody
// cancels the upstream read and returns the same IsError result as the
// buffered path (the final result is the content of record; a truncated one
// would let the agent act on incomplete data).
func (p *Proxy) InvokeStreaming(ctx context.Context, e ToolEntry, args []byte, notify streamNotify) (*mcp.CallToolResult, error) {
	if e.StreamMaxDuration > 0 {
		var cancelMax context.CancelFunc
		ctx, cancelMax = context.WithTimeout(ctx, e.StreamMaxDuration)
		defer cancelMax()
	}
	// Cancel-with-cause so streamAbort can tell a watchdog abort from a
	// caller cancel — both otherwise surface as context.Canceled. Mirrors
	// pkg/router/stream.go's errStreamIdleTimeout pattern.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	// Same slot accounting as Invoke; the wait is bounded inside acquireSlot.
	if !p.acquireSlot(ctx) {
		p.logger.V(1).Info("tool call shed: concurrency limit", "tool", e.ToolName)
		return toolError("function invocation failed: server busy"), nil
	}
	defer func() { <-p.sem }()

	idle := e.StreamIdleTimeout
	if idle <= 0 {
		idle = p.streamIdleDefault
	}
	// Idle watchdog shared with the router's streaming path: re-armed on each
	// upstream chunk, and safe against the AfterFunc dispatch race (a fire
	// racing a fresh Reset re-arms instead of aborting; see
	// streaming.Watchdog).
	wd := streaming.NewWatchdog(idle, func() { cancel(errStreamIdleTimeout) })
	wd.Start()
	defer wd.Stop()

	req, err := p.buildRequest(ctx, e, args)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return p.streamAbort(ctx, e, err, "connecting"), nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Error statuses take the buffered mapping; nothing is streamed.
		return p.mapResponse(e, resp)
	}

	// Notification delivery is decoupled from the read loop (see delivery):
	// NotifyProgress ultimately writes to the client socket with no deadline,
	// so a slow or stalled client must not stall the upstream read, pin the
	// semaphore slot at client pace, or trip the idle watchdog for a stream
	// whose FUNCTION is healthy. When delivery reports the caller gone, it
	// cancels this call with errCallerGone so the upstream read unwinds.
	d := newDelivery(ctx, notify, func() { cancel(errCallerGone) })
	// On every abort path the delivery goroutine is stopped (its ctx is
	// cancelled) and drained BEFORE the error result returns, so no orphaned
	// progress for content absent from the final result can trail it; on
	// success the drain is bounded so a stalled client forfeits its tail
	// instead of holding this call's slot open. Either way the delivery
	// goroutine has exited by the time the result is written.
	defer d.finish(streamDrainTimeout)

	// Upstream activity re-arms the idle watchdog through the same
	// ActivityReadCloser the router's streaming path uses — one definition
	// of "activity" for a function regardless of entry path.
	body := streaming.NewActivityReadCloser(resp.Body, wd.Reset, nil)

	var acc bytes.Buffer
	var total int64
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > p.maxBody {
				// Stop the upstream body, don't drain past the cap.
				cancel(errResponseCapExceeded)
				p.logger.V(1).Info("tool response exceeded cap", "tool", e.ToolName, "cap", p.maxBody)
				return toolError(fmt.Sprintf("function response exceeded %d bytes", p.maxBody)), nil
			}
			acc.Write(buf[:n])
			d.push(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return p.streamAbort(ctx, e, readErr, "mid-stream"), nil
		}
	}
	// The function is done; only the client is being waited on from here.
	// Disarm the watchdog first, or an idle window shorter than the drain
	// bound (IdleTimeoutSeconds 1-4 is admissible) would cut the drain short
	// after the body was already fully read.
	wd.Stop()
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: acc.String()}}}, nil
}

// Stream-abort causes attached via context.WithCancelCause, so streamAbort
// can distinguish who ended the stream (mirrors pkg/router/stream.go).
var (
	errStreamIdleTimeout   = errors.New("stream aborted: idle timeout")
	errResponseCapExceeded = errors.New("stream aborted: response cap exceeded")
	errCallerGone          = errors.New("stream aborted: caller unreachable")
)

// streamAbort is the single transport-failure classifier for every failure
// site (buffered connect, streaming connect, mid-stream), so the same cause
// gets the same label wherever it lands: a watchdog, call-ceiling, or
// max-duration abort is "timed out", a caller cancel is "canceled", anything
// else is a generic failure with the detail kept server-side.
func (p *Proxy) streamAbort(ctx context.Context, e ToolEntry, err error, phase string) *mcp.CallToolResult {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, errStreamIdleTimeout) || errors.Is(cause, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		p.logger.V(1).Info("tool call timed out", "phase", phase, "tool", e.ToolName, "function", e.FnName, "namespace", e.Namespace)
		return toolError("function invocation timed out")
	case ctx.Err() != nil:
		// The caller went away (MCP cancellation / client disconnect); the
		// function did nothing wrong.
		p.logger.V(1).Info("tool call canceled by caller", "phase", phase, "tool", e.ToolName, "function", e.FnName, "namespace", e.Namespace)
		return toolError("function invocation canceled")
	default:
		p.logger.Error(err, "tool call transport error", "phase", phase, "tool", e.ToolName, "function", e.FnName, "namespace", e.Namespace)
		return toolError("function invocation failed")
	}
}

// delivery decouples progress-notification writes from the upstream read
// loop. Chunks accumulate under mu while a single sender goroutine drains
// them; backpressure from a slow client coalesces pending chunks into one
// larger notification instead of dropping content, so the notified chunks
// always concatenate to the final result text. progressBytes reported to
// notify is the cumulative DELIVERED byte count — monotonic by construction,
// including the EOF flush of a trailing partial rune.
type delivery struct {
	notify streamNotify
	// onGone is invoked (once) when notify reports the caller unreachable;
	// InvokeStreaming wires it to cancel the call so the upstream read
	// unwinds — the proxy's client-gone signal for legacy-protocol callers.
	onGone  func()
	ctx     context.Context
	mu      sync.Mutex
	raw     []byte // bytes pushed but not yet handed to notify
	done    bool
	wake    chan struct{}
	drained chan struct{}
}

// newDelivery returns nil when notify is nil; a nil *delivery's methods are
// no-ops, so the read loop calls them unconditionally.
func newDelivery(ctx context.Context, notify streamNotify, onGone func()) *delivery {
	if notify == nil {
		return nil
	}
	d := &delivery{
		notify:  notify,
		onGone:  onGone,
		ctx:     ctx,
		wake:    make(chan struct{}, 1),
		drained: make(chan struct{}),
	}
	go d.run()
	return d
}

// push hands bytes to the sender without blocking on the client.
func (d *delivery) push(b []byte) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.raw = append(d.raw, b...)
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// finish marks the stream complete and waits up to timeout for the sender to
// flush what remains.
func (d *delivery) finish(timeout time.Duration) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.done = true
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
	// Wait for the sender to exit, bounded by timeout. Deliberately NOT
	// short-circuited on ctx.Done(): on an abort the ctx is already
	// cancelled, and the point is to have the sender gone (run() exits
	// promptly on a cancelled ctx) before the error result is written.
	wait := time.NewTimer(timeout)
	defer wait.Stop()
	select {
	case <-d.drained:
	case <-wait.C:
	}
}

// run is the sender goroutine: it emits complete-rune prefixes as they
// accumulate, holds back a trailing partial rune until its continuation
// bytes arrive, and flushes any remaining bytes verbatim once the stream is
// done (a trailing partial rune at EOF is the function's own invalid UTF-8;
// forwarding it keeps progress and the final result byte-identical).
func (d *delivery) run() {
	defer close(d.drained)
	var delivered int64
	for {
		d.mu.Lock()
		emit, rest := splitRuneBoundary(d.raw)
		if len(emit) == 0 && d.done {
			emit, rest = d.raw, nil
		}
		var out string
		if len(emit) > 0 {
			// The string conversion copies; take it BEFORE the compaction
			// below overwrites emit's backing region.
			out = string(emit)
			d.raw = append(d.raw[:0], rest...)
		}
		done := d.done
		d.mu.Unlock()

		if out != "" {
			// An aborted call (ctx cancelled) must not emit progress for
			// content that will not be in the (error) result.
			if d.ctx.Err() != nil {
				return
			}
			delivered += int64(len(out))
			if err := d.notify(d.ctx, out, delivered); err != nil {
				// The caller's stream is closed: nothing further can be
				// delivered, so stop sending and let InvokeStreaming unwind
				// the upstream call instead of running it for nobody.
				if d.onGone != nil {
					d.onGone()
				}
				return
			}
			continue // there may be more already buffered
		}
		if done {
			return
		}
		select {
		case <-d.wake:
		case <-d.ctx.Done():
			return
		}
	}
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

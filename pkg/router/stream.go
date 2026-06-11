// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/streaming"
)

// Stream-abort causes, attached to the request context via context.WithCancelCause
// so the proxy error handler can distinguish a server-initiated stream abort from
// a genuine client disconnect (which also surfaces as context.Canceled).
var (
	errStreamIdleTimeout = errors.New("stream aborted: idle timeout")
	errStreamMaxDuration = errors.New("stream aborted: max duration")
)

// setupStreamContext scopes a streaming request to (a) a max-duration ceiling
// (if any) and (b) an idle Watchdog re-armed on each upstream chunk. Both
// cancel the request context, which tears the upstream connection down. The
// returned request carries the stream context; the caller must Stop the
// watchdog and cancel the context when ServeHTTP returns.
func (fh *functionHandler) setupStreamContext(request *http.Request, policy proxyPolicy) (*http.Request, *streaming.Watchdog, context.CancelCauseFunc) {
	ctx, cancel := context.WithCancelCause(request.Context())
	// The cancel callbacks log the abort at Info — this is the authoritative
	// signal, and the only one for a mid-stream abort (once headers are
	// flushed the status is already 200 and the proxy error handler never
	// runs, so without this a cut LLM/SSE stream would be silent).
	fnMeta := &fh.function.ObjectMeta
	if policy.maxDuration > 0 {
		timer := time.AfterFunc(policy.maxDuration, func() {
			fh.logger.Info("stream aborted: max duration exceeded",
				"function", fnMeta.Name, "namespace", fnMeta.Namespace, "maxDuration", policy.maxDuration)
			cancel(fmt.Errorf("%w (%s)", errStreamMaxDuration, policy.maxDuration))
		})
		context.AfterFunc(ctx, func() { timer.Stop() })
	}
	watchdog := streaming.NewWatchdog(policy.idleTimeout, func() {
		fh.logger.Info("stream aborted: idle timeout exceeded",
			"function", fnMeta.Name, "namespace", fnMeta.Namespace, "idleTimeout", policy.idleTimeout)
		cancel(fmt.Errorf("%w (%s)", errStreamIdleTimeout, policy.idleTimeout))
	})
	// Arm now (not at headers) so the idle timeout also bounds time-to-first-byte:
	// a streaming function that accepts the connection but never responds is
	// aborted at the idle window rather than hanging until the client disconnects.
	watchdog.Start()
	return request.WithContext(ctx), watchdog, cancel
}

// onStreamResponse wires the streaming response: it arms the idle Watchdog, wraps
// resp.Body so each upstream chunk re-arms the idle window, and (for poolmgr)
// launches a keepalive heartbeat so the pod is not idle-reaped mid-stream. ctx is
// the stream context (cancelled on idle/max/client-disconnect), which also stops
// the heartbeat.
func (fh *functionHandler) onStreamResponse(ctx context.Context, rrt *RetryingRoundTripper, w *streaming.Watchdog, resp *http.Response) {
	// Keep the poolmgr pod tapped for the connection's lifetime — the router-driven,
	// environment-agnostic replacement for the legacy WebsocketFsvc reaper skip.
	// Covers SSE/chunked and WebSocket alike (ServeHTTP blocks until the socket
	// closes, so the handler defer untaps at the right time).
	if fh.function.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypePoolmgr {
		interval := rrt.policy.idleTimeout / 2
		if interval <= 0 || interval > 30*time.Second {
			interval = 30 * time.Second
		}
		fh.startKeepaliveHeartbeat(ctx, fh.function, rrt.tapURL, interval)
	}

	// A hijacked WebSocket (101) keeps resp.Body as an io.ReadWriteCloser that
	// ReverseProxy hijacks to pipe bytes both ways — we must NOT wrap it (the
	// wrapper is read-only and would break the hijack). Idle is not observable
	// without body reads, so rely on the heartbeat + TCP keepalive + the optional
	// max-duration ceiling.
	if resp.StatusCode == http.StatusSwitchingProtocols {
		if w != nil {
			w.Stop()
		}
		return
	}

	// SSE/chunked: the idle Watchdog was already armed in handler (so it also
	// covers time-to-first-byte); wrap resp.Body so each upstream chunk re-arms it.
	if resp.Body == nil {
		return
	}
	resp.Body = streaming.NewActivityReadCloser(
		resp.Body,
		func() {
			if w != nil {
				w.Reset()
			}
		},
		func() {}, // untap is handled by the handler defer
	)
}

// startKeepaliveHeartbeat re-taps the poolmgr service on an interval so the
// executor's idle reaper sees a fresh Atime for the lifetime of a stream. Stops
// when ctx is done (handler defer / client disconnect / idle/max cancel).
func (fh *functionHandler) startKeepaliveHeartbeat(ctx context.Context, fn *fv1.Function, serviceURL *url.URL, interval time.Duration) {
	if interval <= 0 || serviceURL == nil {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				fh.tapper.Tap(fn, serviceURL)
			}
		}
	}()
}

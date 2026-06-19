// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpmux

import (
	"net/http"
	"strings"
	"time"
)

// Recorder records per-request HTTP metrics. The mux invokes it for each
// matched route with the route's registered pattern as the (low-cardinality)
// label. The interface is declared here — where it is consumed — so httpmux
// stays metrics-agnostic; an implementation lives in pkg/utils/metrics and
// satisfies it structurally (no import back into httpmux).
type Recorder interface {
	InFlightInc(pattern, method string)
	InFlightDec(pattern, method string)
	Observe(pattern, method string, statusCode int, duration time.Duration)
}

// responseRecorder captures the status code (and forwards Flush for streaming
// responses) so the metrics Observe can label by code. It deliberately does
// NOT implement http.Hijacker: websocket upgrades bypass instrumentation (see
// instrument) and reach the unwrapped ResponseWriter, which keeps Hijack.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *responseRecorder) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Instrument wraps next to record metrics via rec, labelling each request by
// patternOf(r) — evaluated BEFORE serving so the in-flight gauge inc/dec and
// the observation share one label series. A nil rec returns next unchanged.
// Websocket upgrades bypass instrumentation: the hijacked connection never
// returns until the socket closes, so an in-flight gauge / duration timer
// around it would be meaningless.
//
// The mux uses this internally with a constant per-route pattern (see
// instrument). It is exported for callers that still route with another library
// during migration (e.g. the gorilla-backed router) and must compute the label
// from that library at request time.
func Instrument(rec Recorder, patternOf func(*http.Request) string, next http.Handler) http.Handler {
	if rec == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWebSocketUpgrade(r) {
			next.ServeHTTP(w, r)
			return
		}
		pattern := patternOf(r)
		start := time.Now()
		rec.InFlightInc(pattern, r.Method)
		rw := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			rec.InFlightDec(pattern, r.Method)
			rec.Observe(pattern, r.Method, rw.status, time.Since(start))
		}()
		next.ServeHTTP(rw, r)
	})
}

// instrument is Instrument bound to a constant pattern (the matched route's).
func instrument(rec Recorder, pattern string, h http.Handler) http.Handler {
	if rec == nil {
		return h
	}
	return Instrument(rec, func(*http.Request) string { return pattern }, h)
}

// isWebSocketUpgrade reports whether r is a websocket upgrade handshake
// (Upgrade: websocket + Connection: Upgrade, per RFC 6455).
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		headerTokenContains(r.Header.Get("Connection"), "upgrade")
}

// headerTokenContains reports whether the comma-separated header value contains
// token (case-insensitive), e.g. Connection: keep-alive, Upgrade.
func headerTokenContains(header, token string) bool {
	for v := range strings.SplitSeq(header, ",") {
		if strings.EqualFold(strings.TrimSpace(v), token) {
			return true
		}
	}
	return false
}

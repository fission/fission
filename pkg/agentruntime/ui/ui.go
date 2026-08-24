// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package ui embeds the agent runtime's boardroom UI (Task 22, RFC-0032
// slice 4): a single self-contained HTML page (inline CSS/JS, no external
// assets, no build step) that visualizes the SESSIONS/POOL split view from
// distributed-agent-runtime/06-demo-plan.md, driven by the registry API's
// SSE feed (GET /registry/events) with a REST polling fallback.
//
// The embed lives in its own package, mirroring crds/crds.go's precedent —
// the embed directive cannot reach outside its own package directory — and
// it is the first STATIC-serving embed in this repo (crds/crds.go embeds
// data files consumed programmatically, not served as an HTTP response
// body).
package ui

import (
	_ "embed"
	"net/http"
)

// boardroom is the embedded page. Served byte-for-byte on every request; it
// carries no template placeholders, so nothing here needs the request.
//
//go:embed boardroom.html
var boardroom []byte

// Handler returns an http.Handler that serves the boardroom page at any
// path it is mounted on, with Content-Type text/html and no-cache headers —
// the page is small, self-contained, and expected to change across
// releases, so a browser or intermediary must never serve a stale cached
// copy of it.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(boardroom)
	})
}

// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpmux

import (
	"net/http"

	"golang.org/x/net/http/httpguts"
)

// IsWebSocketUpgrade reports whether r is a websocket upgrade handshake
// (Upgrade: websocket + Connection: Upgrade, per RFC 6455). Connection is a
// comma-separated token list, so it is parsed with the canonical httpguts
// tokenizer rather than a string compare — a naive Get(...) == "Upgrade" misses
// the common "Connection: keep-alive, Upgrade" form and any case variation.
// Used both by the mux (to skip instrumenting hijacked connections, see
// instrument) and by the router's data plane, which share this one detector.
func IsWebSocketUpgrade(r *http.Request) bool {
	return httpguts.HeaderValuesContainsToken(r.Header["Upgrade"], "websocket") &&
		httpguts.HeaderValuesContainsToken(r.Header["Connection"], "upgrade")
}

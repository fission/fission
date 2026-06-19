// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpmux

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsWebSocketUpgrade pins the RFC-6455 token-list semantics that a naive
// Get("Connection") == "Upgrade" gets wrong — notably the "keep-alive, Upgrade"
// form browsers send and header-case variation.
func TestIsWebSocketUpgrade(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		upgrade    string
		connection string
		want       bool
	}{
		{"plain", "websocket", "Upgrade", true},
		{"connection token list", "websocket", "keep-alive, Upgrade", true},
		{"lowercase connection", "websocket", "upgrade", true},
		{"mixed case upgrade", "WebSocket", "Upgrade", true},
		{"token list with spaces", "websocket", "keep-alive,  Upgrade", true},
		{"not websocket upgrade", "h2c", "Upgrade", false},
		{"no upgrade token in connection", "websocket", "keep-alive", false},
		{"no headers", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.upgrade != "" {
				r.Header.Set("Upgrade", tc.upgrade)
			}
			if tc.connection != "" {
				r.Header.Set("Connection", tc.connection)
			}
			assert.Equal(t, tc.want, IsWebSocketUpgrade(r))
		})
	}
}

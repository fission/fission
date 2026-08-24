// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandler_ServesBoardroom pins Handler's contract: GET / returns 200
// text/html with a stable DOM marker (id="boardroom", the root element of
// boardroom.html) present in the body, and a no-cache response so a
// kubectl-port-forwarded browser session never serves a stale copy of the
// page across a redeploy. The embed itself compiling is exercised for free
// by this package building at all.
func TestHandler_ServesBoardroom(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	Handler().ServeHTTP(w, req)

	resp := w.Result()
	require.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")
	assert.Equal(t, "no-cache, no-store, must-revalidate", resp.Header.Get("Cache-Control"))

	body := w.Body.String()
	assert.True(t, strings.Contains(body, `id="boardroom"`), "body must contain the stable id=\"boardroom\" marker")
	assert.Contains(t, body, "<title>Fission Boardroom</title>")
}

// TestHandler_ServesOnAnyMountedPath confirms the handler is path-agnostic
// (it always writes the same embedded bytes), matching how main.go mounts
// it at both "GET /ui" and "GET /ui/" without any internal routing of its
// own.
func TestHandler_ServesOnAnyMountedPath(t *testing.T) {
	for _, path := range []string{"/", "/ui", "/ui/", "/ui/anything"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()

		Handler().ServeHTTP(w, req)

		require.Equal(t, 200, w.Result().StatusCode, "path %s", path)
		assert.Contains(t, w.Body.String(), `id="boardroom"`, "path %s", path)
	}
}
